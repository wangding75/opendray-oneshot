package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/opendray/opendray-v2/internal/integration"
	"github.com/opendray/opendray-v2/internal/oneshot/application"
	attachmentservice "github.com/opendray/opendray-v2/internal/oneshot/attachment"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/queue"
	"github.com/opendray/opendray-v2/internal/oneshot/store"
	"github.com/opendray/opendray-v2/internal/oneshot/workspacepolicy"
)

func TestRequestIDMiddlewareStabilizesGeneratedID(t *testing.T) {
	h := &Handler{}
	var first, second string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		first = requestID(r)
		second = requestID(r)
	})
	recorder := httptest.NewRecorder()
	h.requestIDMiddleware(next).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if first == "" || first != second || recorder.Header().Get("X-Request-ID") != first {
		t.Fatalf("request id is not stable: first=%q second=%q header=%q", first, second, recorder.Header().Get("X-Request-ID"))
	}
}

func TestDigestBase64ConvertsHexSHA256(t *testing.T) {
	const hexDigest = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	decoded, _ := base64.StdEncoding.DecodeString(digestBase64(hexDigest))
	if len(decoded) != 32 || decoded[0] != 0 || decoded[31] != 31 {
		t.Fatalf("unexpected digest bytes: %v", decoded)
	}
}

func TestReplayCursorRoundTripAndRejectsUnknownKinds(t *testing.T) {
	original := store.ReplayCursor{OccurredAt: time.Date(2026, 7, 28, 12, 0, 0, 123, time.UTC), Kind: "o", ID: "ose_42"}
	encoded := encodeReplayCursor(original)
	decoded, err := decodeReplayCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.OccurredAt.Equal(original.OccurredAt) || decoded.Kind != original.Kind || decoded.ID != original.ID {
		t.Fatalf("cursor changed during round trip: got=%+v want=%+v", decoded, original)
	}

	invalidRaw, _ := json.Marshal(store.ReplayCursor{OccurredAt: original.OccurredAt, Kind: "session", ID: "1"})
	if _, err := decodeReplayCursor(base64.RawURLEncoding.EncodeToString(invalidRaw)); err == nil {
		t.Fatal("unknown replay cursor kind was accepted")
	}
}

func TestSameOriginRejectsCrossOriginWebSocketUpgrade(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://opendray.example/api/v1/oneshot/tasks/stream", nil)
	req.Host = "opendray.example"
	req.Header.Set("Origin", "https://evil.example")
	if sameOrigin(req) {
		t.Fatal("cross-origin websocket upgrade was accepted")
	}
	req.Header.Set("Origin", "https://opendray.example")
	if !sameOrigin(req) {
		t.Fatal("same-origin websocket upgrade was rejected")
	}
}

type auditFixture struct{ record AuditRecord }

func (a *auditFixture) Record(_ context.Context, record AuditRecord) { a.record = record }

func TestAuditHashesIdempotencyKeyAndUsesFrozenAction(t *testing.T) {
	fixture := &auditFixture{}
	h := &Handler{auditor: fixture, now: func() time.Time { return time.Unix(1, 0).UTC() }}
	principal := integration.Principal{Kind: integration.KindIntegration, ID: "int-1"}
	h.auditSuccess(context.Background(), principal, actionTaskCreate, "task", "otk_1", "project-1", "secret-key", nil)
	if fixture.record.Action != "oneshot.task.create" {
		t.Fatalf("unexpected audit action: %q", fixture.record.Action)
	}
	if fixture.record.IdempotencyKey == "secret-key" || !strings.HasPrefix(fixture.record.IdempotencyKey, "sha256:") {
		t.Fatalf("idempotency key was not irreversibly hashed: %q", fixture.record.IdempotencyKey)
	}
}

func TestAuthorizeRejectsMissingIntegrationScopeAndAuditsDenial(t *testing.T) {
	fixture := &auditFixture{}
	h := &Handler{auditor: fixture, now: func() time.Time { return time.Unix(1, 0).UTC() }}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/oneshot/tasks", nil)
	req = req.WithContext(integration.WithPrincipal(req.Context(), integration.Principal{Kind: integration.KindIntegration, ID: "int-1", Scopes: []string{"oneshot:task:read"}}))
	recorder := httptest.NewRecorder()
	_, _, ok := h.authorize(recorder, req, scopeTaskCreate, actionTaskCreate, "task", "")
	if ok || recorder.Code != http.StatusForbidden {
		t.Fatalf("missing scope was not rejected: ok=%v status=%d", ok, recorder.Code)
	}
	if fixture.record.Result != "failure" || fixture.record.Action != actionTaskCreate {
		t.Fatalf("denied scope was not audited: %+v", fixture.record)
	}
}

func TestEventCursorIsOpaqueAndStable(t *testing.T) {
	encoded := encodeEventCursor(42)
	if encoded == "42" {
		t.Fatal("event cursor exposed the raw sequence")
	}
	decoded, err := decodeEventCursor(encoded)
	if err != nil || decoded != 42 {
		t.Fatalf("event cursor did not round trip: sequence=%d err=%v", decoded, err)
	}
	if _, err := decodeEventCursor("42"); err == nil {
		t.Fatal("constructible numeric event cursor was accepted")
	}
}

func TestProjectMismatchIsMaskedAndAudited(t *testing.T) {
	fixture := &auditFixture{}
	h := &Handler{auditor: fixture, now: func() time.Time { return time.Unix(1, 0).UTC() }}
	principal := integration.Principal{Kind: integration.KindIntegration, ID: "int-1"}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/oneshot/tasks/otk_1?project_id=project-other", nil)
	recorder := httptest.NewRecorder()
	if h.projectAllowed(recorder, req, principal, actionTaskRead, "task", "otk_1", "project-owned") {
		t.Fatal("project mismatch was accepted")
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("project mismatch status=%d want=%d", recorder.Code, http.StatusNotFound)
	}
	if fixture.record.Result != "failure" || fixture.record.Action != actionTaskRead || fixture.record.ProjectID != "project-other" {
		t.Fatalf("project mismatch was not audited: %+v", fixture.record)
	}
}

func TestRejectedWriteAuditHashesOptionalKey(t *testing.T) {
	fixture := &auditFixture{}
	h := &Handler{auditor: fixture, now: func() time.Time { return time.Unix(1, 0).UTC() }}
	principal := integration.Principal{Kind: integration.KindIntegration, ID: "int-1"}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/oneshot/tasks/otk_1/retry", nil)
	h.auditRejectedWrite(req, principal, actionTaskRetry, "task", "otk_1", "project-1", "raw-key", domain.ErrorInvalidRequest, "invalid JSON request")
	if fixture.record.Result != "failure" || fixture.record.Action != actionTaskRetry {
		t.Fatalf("rejected write was not audited: %+v", fixture.record)
	}
	if fixture.record.IdempotencyKey == "raw-key" || !strings.HasPrefix(fixture.record.IdempotencyKey, "sha256:") {
		t.Fatalf("rejected write leaked raw idempotency key: %q", fixture.record.IdempotencyKey)
	}
	if got := fixture.record.Details["error_code"]; got != domain.ErrorInvalidRequest {
		t.Fatalf("unexpected error code in audit: %v", got)
	}
}

func TestSourceFromRESTRequestRejectsTransportControlledRouting(t *testing.T) {
	for _, input := range []*domain.Source{
		{Kind: domain.SourceTelegram, ChannelID: "telegram"},
		{Kind: domain.SourceMobile, ReplyAddress: &domain.ReplyAddress{ChannelID: "telegram", ConversationID: "chat-1"}},
		{Kind: domain.SourceWeb, SourceMessageID: "message-1"},
	} {
		if _, err := sourceFromRESTRequest(input, "request-1"); err == nil {
			t.Fatalf("accepted transport-controlled REST source: %+v", input)
		}
	}
}

func TestSourceFromRESTRequestAllowsNonRoutingClientOrigin(t *testing.T) {
	input := &domain.Source{
		Kind: domain.SourceMobile, ClientRequestID: "mobile-request-1",
		Metadata: map[string]string{"client_version": "2.3.3"},
	}
	got, err := sourceFromRESTRequest(input, "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != domain.SourceMobile || got.ClientRequestID != "mobile-request-1" {
		t.Fatalf("unexpected source: %+v", got)
	}
	if got.ReplyAddress != nil || got.ChannelID != "" || got.SourceMessageID != "" {
		t.Fatalf("REST source retained routing fields: %+v", got)
	}
	input.Metadata["client_version"] = "mutated"
	if got.Metadata["client_version"] != "2.3.3" {
		t.Fatal("REST source metadata was not defensively copied")
	}
}

func TestDecodeJSONRejectsTrailingJSONValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"project_id":"project-1"} {"second":true}`))
	recorder := httptest.NewRecorder()
	var target map[string]any
	if decodeJSON(recorder, req, &target) {
		t.Fatal("trailing JSON value was accepted")
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want=%d", recorder.Code, http.StatusBadRequest)
	}
}

func TestMountRegistersFrozenSixteenRouteControlPlane(t *testing.T) {
	router := chi.NewRouter()
	(&Handler{}).Mount(router)
	got := map[string]struct{}{}
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		got[method+" "+route] = struct{}{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"POST /oneshot/tasks", "GET /oneshot/tasks", "GET /oneshot/tasks/stream", "GET /oneshot/tasks/{task_id}",
		"POST /oneshot/tasks/{task_id}/continue", "POST /oneshot/tasks/{task_id}/cancel", "POST /oneshot/tasks/{task_id}/retry",
		"GET /oneshot/tasks/{task_id}/runs", "GET /oneshot/runs/{run_id}", "GET /oneshot/runs/{run_id}/events",
		"GET /oneshot/runs/{run_id}/stream", "GET /oneshot/runs/{run_id}/artifacts", "GET /oneshot/artifacts/{artifact_id}",
		"POST /oneshot/attachments", "GET /oneshot/attachments/{attachment_id}", "DELETE /oneshot/attachments/{attachment_id}",
	}
	for _, route := range expected {
		if _, ok := got[route]; !ok {
			t.Errorf("missing route %s", route)
		}
	}
	if len(got) != len(expected) {
		t.Fatalf("registered routes=%d want=%d: %+v", len(got), len(expected), got)
	}
}

type apiCreatorFixture struct {
	calls   int
	command application.CreateTaskCommand
	err     error
}

func (f *apiCreatorFixture) CreateTask(_ context.Context, command application.CreateTaskCommand) (application.CreateTaskResult, error) {
	f.calls++
	f.command = command
	if f.err != nil {
		return application.CreateTaskResult{}, f.err
	}
	return application.CreateTaskResult{
		Task:     domain.TaskSnapshot{ID: "otk_01J00000000000000000000000", ProjectID: command.ProjectID, ProviderID: command.ProviderID, Source: command.Source},
		Delivery: domain.DeliverySnapshot{ID: "odl_01J00000000000000000000000"},
		Created:  true,
	}, nil
}

func requestWithIntegrationPrincipal(req *http.Request, scopes ...string) *http.Request {
	principal := integration.Principal{Kind: integration.KindIntegration, ID: "integration-1", Scopes: scopes}
	return req.WithContext(integration.WithPrincipal(req.Context(), principal))
}

func TestCreateTaskRouteRejectsSpoofedTelegramSourceBeforeApplication(t *testing.T) {
	creator := &apiCreatorFixture{}
	h := &Handler{enabled: true, creator: creator, now: func() time.Time { return time.Unix(1, 0).UTC() }}
	router := chi.NewRouter()
	h.Mount(router)
	body := `{"project_id":"project-1","provider_id":"codex","prompt":"hello","source":{"kind":"telegram","channel_id":"victim"}}`
	req := requestWithIntegrationPrincipal(httptest.NewRequest(http.MethodPost, "/oneshot/tasks", strings.NewReader(body)), scopeTaskCreate)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "create-1")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if creator.calls != 0 {
		t.Fatalf("spoofed source reached application service: calls=%d", creator.calls)
	}
}

func TestCreateTaskRouteRequiresIdempotencyKey(t *testing.T) {
	creator := &apiCreatorFixture{}
	h := &Handler{enabled: true, creator: creator, now: func() time.Time { return time.Unix(1, 0).UTC() }}
	router := chi.NewRouter()
	h.Mount(router)
	req := requestWithIntegrationPrincipal(httptest.NewRequest(http.MethodPost, "/oneshot/tasks", strings.NewReader(`{"project_id":"project-1","provider_id":"codex","prompt":"hello"}`)), scopeTaskCreate)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), string(domain.ErrorIdempotencyRequired)) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if creator.calls != 0 {
		t.Fatalf("request without idempotency key reached application service: calls=%d", creator.calls)
	}
}

func TestCreateTaskRouteNormalizesTrustedRESTSource(t *testing.T) {
	creator := &apiCreatorFixture{}
	h := &Handler{enabled: true, creator: creator, now: func() time.Time { return time.Unix(1, 0).UTC() }}
	router := chi.NewRouter()
	h.Mount(router)
	body := `{"project_id":"project-1","provider_id":"codex","prompt":"hello","source":{"kind":"mobile","client_request_id":"mobile-1","metadata":{"client_version":"2.3.3"}}}`
	req := requestWithIntegrationPrincipal(httptest.NewRequest(http.MethodPost, "/oneshot/tasks", strings.NewReader(body)), scopeTaskCreate)
	req.Header.Set("Idempotency-Key", "create-mobile-1")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if creator.calls != 1 || creator.command.Source.Kind != domain.SourceMobile || creator.command.Source.ReplyAddress != nil {
		t.Fatalf("unexpected application command: calls=%d source=%+v", creator.calls, creator.command.Source)
	}
}

func TestCreateTaskRouteRejectsWorkspaceOutsideAllowedRoots(t *testing.T) {
	root := t.TempDir()
	policy, err := workspacepolicy.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	creator := application.NewDispatchService(queue.NewMemoryQueue(nil), application.WithWorkspacePolicy(policy, root))
	h, err := New(Options{Enabled: true, Creator: creator, Repository: &apiRepositoryFixture{}})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"project_id":"project-1","provider_id":"codex","prompt":"hello","workspace_path":"` + strings.ReplaceAll(t.TempDir(), `\`, `\\`) + `"}`
	req := requestWithIntegrationPrincipal(httptest.NewRequest(http.MethodPost, "/oneshot/tasks", strings.NewReader(body)), scopeTaskCreate)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "workspace-invalid-1")
	recorder := httptest.NewRecorder()
	router := chi.NewRouter()
	h.Mount(router)
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

type contractTestQueueRepository struct {
	calls       int
	lastRequest queue.EnqueueRequest
	err         error
}

func (r *contractTestQueueRepository) Enqueue(ctx context.Context, req queue.EnqueueRequest) (queue.EnqueueResult, error) {
	r.calls++
	r.lastRequest = req
	if r.err != nil {
		return queue.EnqueueResult{}, r.err
	}
	return queue.EnqueueResult{
		Task:     req.Task,
		Delivery: req.Delivery,
		Created:  true,
	}, nil
}

func (r *contractTestQueueRepository) ClaimDue(ctx context.Context, workerID string, limit int, lease time.Duration) ([]queue.Claim, error) {
	return nil, nil
}

func (r *contractTestQueueRepository) RenewLease(ctx context.Context, deliveryID, workerID string, lease time.Duration) (domain.DeliverySnapshot, error) {
	return domain.DeliverySnapshot{}, nil
}

func (r *contractTestQueueRepository) Ack(ctx context.Context, deliveryID, workerID string) (domain.DeliverySnapshot, error) {
	return domain.DeliverySnapshot{}, nil
}

func (r *contractTestQueueRepository) Nack(ctx context.Context, deliveryID, workerID string, code domain.ErrorCode, policy queue.RetryPolicy) (domain.DeliverySnapshot, error) {
	return domain.DeliverySnapshot{}, nil
}

func (r *contractTestQueueRepository) DeadLetter(ctx context.Context, deliveryID, workerID string, code domain.ErrorCode) (domain.DeliverySnapshot, error) {
	return domain.DeliverySnapshot{}, nil
}

func (r *contractTestQueueRepository) Cancel(ctx context.Context, deliveryID string, owner domain.Owner, workerID string) (domain.DeliverySnapshot, error) {
	return domain.DeliverySnapshot{}, nil
}

func (r *contractTestQueueRepository) AcknowledgeRecovered(ctx context.Context, deliveryID, runID string) (domain.DeliverySnapshot, error) {
	return domain.DeliverySnapshot{}, nil
}

func assertNoSensitiveInfo(t *testing.T, body string, root, outside, home string) {
	t.Helper()
	checkContains := func(sens string, desc string) {
		if sens == "" {
			return
		}
		// Raw check
		if strings.Contains(body, sens) {
			t.Errorf("sensitive %s %q exposed in response: %q", desc, sens, body)
		}
		// Escaped JSON check
		escaped, _ := json.Marshal(sens)
		escapedStr := string(escaped[1 : len(escaped)-1]) // strip quotes
		if strings.Contains(body, escapedStr) {
			t.Errorf("escaped sensitive %s %q exposed in response: %q", desc, escapedStr, body)
		}
		// Normal/forward slash check
		normalized := strings.ReplaceAll(sens, `\`, `/`)
		if strings.Contains(body, normalized) {
			t.Errorf("normalized sensitive %s %q exposed in response: %q", desc, normalized, body)
		}
	}

	checkContains(root, "root path")
	checkContains(outside, "outside path")
	checkContains(home, "user home")

	for _, envName := range []string{"APPDATA", "USERPROFILE", "HOME"} {
		val := os.Getenv(envName)
		if val != "" {
			checkContains(val, "environment variable "+envName)
		}
	}
}

func TestWorkspacePolicyAPIErrorContractDetailed(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	home, _ := os.UserHomeDir()

	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	policy, err := workspacepolicy.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name           string
		workspacePath  string
		repoErr        error
		emptyPolicy    bool
		expectedStatus int
		expectedCode   domain.ErrorCode
		expectEnqueue  int
	}{
		{
			name:           "legal workspace path",
			workspacePath:  root,
			expectedStatus: http.StatusAccepted,
			expectEnqueue:  1,
		},
		{
			name:           "relative path",
			workspacePath:  "relative",
			expectedStatus: http.StatusBadRequest,
			expectedCode:   domain.ErrorInvalidRequest,
			expectEnqueue:  0,
		},
		{
			name:           "does not exist",
			workspacePath:  filepath.Join(root, "missing"),
			expectedStatus: http.StatusBadRequest,
			expectedCode:   domain.ErrorInvalidRequest,
			expectEnqueue:  0,
		},
		{
			name:           "ordinary file",
			workspacePath:  file,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   domain.ErrorInvalidRequest,
			expectEnqueue:  0,
		},
		{
			name:           "outside root",
			workspacePath:  outside,
			expectedStatus: http.StatusForbidden,
			expectedCode:   domain.ErrorForbidden,
			expectEnqueue:  0,
		},
		{
			name:           "unconfigured roots",
			workspacePath:  root,
			emptyPolicy:    true,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   domain.ErrorInvalidRequest,
			expectEnqueue:  0,
		},
		{
			name:           "repository internal failure",
			workspacePath:  root,
			repoErr:        errors.New("dreaded database connection failure"),
			expectedStatus: http.StatusInternalServerError,
			expectedCode:   domain.ErrorInternal,
			expectEnqueue:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &contractTestQueueRepository{err: tt.repoErr}
			var activePolicy *workspacepolicy.Policy
			if tt.emptyPolicy {
				activePolicy, _ = workspacepolicy.New(nil)
			} else {
				activePolicy = policy
			}

			service := application.NewDispatchService(repo, application.WithWorkspacePolicy(activePolicy, root))
			h, err := New(Options{
				Enabled:    true,
				Creator:    service,
				Repository: &apiRepositoryFixture{},
			})
			if err != nil {
				t.Fatal(err)
			}

			router := chi.NewRouter()
			h.Mount(router)

			body := map[string]any{
				"project_id":     "project-1",
				"provider_id":    "codex",
				"prompt":         "hello world",
				"workspace_path": tt.workspacePath,
			}
			bodyBytes, _ := json.Marshal(body)

			req := requestWithIntegrationPrincipal(httptest.NewRequest(http.MethodPost, "/oneshot/tasks", strings.NewReader(string(bodyBytes))), scopeTaskCreate)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "test-key-id")

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)

			if recorder.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d. Body: %s", tt.expectedStatus, recorder.Code, recorder.Body.String())
			}

			if repo.calls != tt.expectEnqueue {
				t.Errorf("expected %d enqueue calls, got %d", tt.expectEnqueue, repo.calls)
			}

			bodyStr := recorder.Body.String()

			if tt.expectedStatus >= 400 {
				// Assert error JSON structure
				var resp struct {
					Error struct {
						Code      string         `json:"code"`
						Message   string         `json:"message"`
						RequestID string         `json:"request_id"`
						Retryable bool           `json:"retryable"`
						Details   map[string]any `json:"details"`
					} `json:"error"`
				}
				if err := json.Unmarshal([]byte(bodyStr), &resp); err != nil {
					t.Fatalf("failed to unmarshal JSON: %v, body: %q", err, bodyStr)
				}

				if resp.Error.Code != string(tt.expectedCode) {
					t.Errorf("expected error code %q, got %q", tt.expectedCode, resp.Error.Code)
				}
				if resp.Error.Message == "" {
					t.Error("expected error message to be not empty")
				}

				// Make sure sensitive data is not leaked
				assertNoSensitiveInfo(t, bodyStr, root, outside, home)

				// For repo internal failure, check that the cause is not leaked
				if tt.repoErr != nil {
					if strings.Contains(bodyStr, tt.repoErr.Error()) {
						t.Errorf("internal repository cause leaked in body: %s", bodyStr)
					}
				}
			} else {
				// Success (202)
				var resp struct {
					Task     domain.TaskSnapshot     `json:"task"`
					Delivery domain.DeliverySnapshot `json:"delivery"`
					Created  bool                    `json:"created"`
				}
				if err := json.Unmarshal([]byte(bodyStr), &resp); err != nil {
					t.Fatalf("failed to unmarshal success JSON: %v, body: %q", err, bodyStr)
				}
				if !resp.Created {
					t.Error("expected created to be true")
				}
				if resp.Task.ID == "" {
					t.Error("expected task ID to be populated")
				}
			}
		})
	}

	t.Run("symlink escape", func(t *testing.T) {
		escapeTarget := t.TempDir()
		escapeLink := filepath.Join(root, "escape-link")
		if err := os.Symlink(escapeTarget, escapeLink); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		defer os.Remove(escapeLink)

		repo := &contractTestQueueRepository{}
		service := application.NewDispatchService(repo, application.WithWorkspacePolicy(policy, root))
		h, err := New(Options{
			Enabled:    true,
			Creator:    service,
			Repository: &apiRepositoryFixture{},
		})
		if err != nil {
			t.Fatal(err)
		}

		router := chi.NewRouter()
		h.Mount(router)

		body := map[string]any{
			"project_id":     "project-1",
			"provider_id":    "codex",
			"prompt":         "hello world",
			"workspace_path": escapeLink,
		}
		bodyBytes, _ := json.Marshal(body)

		req := requestWithIntegrationPrincipal(httptest.NewRequest(http.MethodPost, "/oneshot/tasks", strings.NewReader(string(bodyBytes))), scopeTaskCreate)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "test-key-symlink")

		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusForbidden {
			t.Errorf("expected status %d, got %d. Body: %s", http.StatusForbidden, recorder.Code, recorder.Body.String())
		}
		if repo.calls != 0 {
			t.Errorf("expected 0 enqueue calls, got %d", repo.calls)
		}

		bodyStr := recorder.Body.String()
		var resp struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal([]byte(bodyStr), &resp)
		if resp.Error.Code != string(domain.ErrorForbidden) {
			t.Errorf("expected error code %q, got %q", domain.ErrorForbidden, resp.Error.Code)
		}

		assertNoSensitiveInfo(t, bodyStr, root, escapeTarget, home)
	})
}

type apiErrorReader struct{}

func (apiErrorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestRequestIDEntropyFailureUsesUniqueNonEmptyFallback(t *testing.T) {
	var counter atomic.Uint64
	now := func() time.Time { return time.Unix(123, 456).UTC() }
	first := newRequestIDFrom(apiErrorReader{}, now, &counter)
	second := newRequestIDFrom(apiErrorReader{}, now, &counter)
	if first == "" || second == "" || first == second {
		t.Fatalf("fallback request IDs are not unique: %q %q", first, second)
	}
}

type apiRepositoryFixture struct {
	task          domain.TaskSnapshot
	run           domain.RunSnapshot
	tasks         store.Page[domain.TaskSnapshot]
	runs          store.Page[domain.RunSnapshot]
	events        []domain.StandardEventSnapshot
	artifacts     store.Page[domain.ArtifactSnapshot]
	artifact      domain.ArtifactSnapshot
	lastTaskQuery store.TaskListFilter
}

func (f *apiRepositoryFixture) GetTask(_ context.Context, _ domain.Owner, _ string) (domain.TaskSnapshot, error) {
	return f.task, nil
}
func (f *apiRepositoryFixture) ListTasksFiltered(_ context.Context, _ domain.Owner, filter store.TaskListFilter) (store.Page[domain.TaskSnapshot], error) {
	f.lastTaskQuery = filter
	return f.tasks, nil
}
func (f *apiRepositoryFixture) ListRuns(_ context.Context, _ domain.Owner, _ string, _ store.PageRequest) (store.Page[domain.RunSnapshot], error) {
	return f.runs, nil
}
func (f *apiRepositoryFixture) GetRun(_ context.Context, _ domain.Owner, _ string) (domain.RunSnapshot, error) {
	return f.run, nil
}
func (f *apiRepositoryFixture) ListStandardEvents(_ context.Context, _ domain.Owner, _ string, _ int64, _ int) ([]domain.StandardEventSnapshot, error) {
	return f.events, nil
}
func (f *apiRepositoryFixture) ListTaskReplayEvents(context.Context, domain.Owner, string, string, store.ReplayCursor, int) ([]store.ReplayEvent, error) {
	return nil, nil
}
func (f *apiRepositoryFixture) ListRunReplayEvents(context.Context, domain.Owner, string, string, store.ReplayCursor, int) ([]store.ReplayEvent, error) {
	return nil, nil
}
func (f *apiRepositoryFixture) ListArtifacts(_ context.Context, _ domain.Owner, _, _ string, _ store.PageRequest) (store.Page[domain.ArtifactSnapshot], error) {
	return f.artifacts, nil
}
func (f *apiRepositoryFixture) GetArtifact(_ context.Context, _ domain.Owner, _ string) (domain.ArtifactSnapshot, error) {
	return f.artifact, nil
}

type apiContinuerFixture struct {
	calls   int
	command application.ContinueTaskCommand
	result  application.ContinueTaskResult
}

func (f *apiContinuerFixture) Continue(_ context.Context, command application.ContinueTaskCommand) (application.ContinueTaskResult, error) {
	f.calls++
	f.command = command
	return f.result, nil
}

type apiControllerFixture struct {
	cancelCalls int
	retryCalls  int
	cancel      application.CancelTaskResult
	retry       application.RetryTaskResult
}

func (f *apiControllerFixture) CancelTask(_ context.Context, _ application.CancelTaskCommand) (application.CancelTaskResult, error) {
	f.cancelCalls++
	return f.cancel, nil
}
func (f *apiControllerFixture) RetryTask(_ context.Context, _ application.RetryTaskCommand) (application.RetryTaskResult, error) {
	f.retryCalls++
	return f.retry, nil
}

type apiArtifactStorageFixture struct{ body string }

func (f apiArtifactStorageFixture) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.body)), nil
}

type apiAttachmentFixture struct {
	item        domain.StagedAttachmentSnapshot
	stageCalls  int
	getCalls    int
	deleteCalls int
}

func (f *apiAttachmentFixture) Stage(_ context.Context, request attachmentservice.StageRequest) (domain.StagedAttachmentSnapshot, error) {
	f.stageCalls++
	if request.ProjectID == "" || request.Reader == nil {
		return domain.StagedAttachmentSnapshot{}, domain.InvalidRequestf("invalid stage request")
	}
	return f.item, nil
}
func (f *apiAttachmentFixture) Get(context.Context, domain.Owner, string) (domain.StagedAttachmentSnapshot, error) {
	f.getCalls++
	return f.item, nil
}
func (f *apiAttachmentFixture) Delete(context.Context, domain.Owner, string) (domain.StagedAttachmentSnapshot, error) {
	f.deleteCalls++
	item := f.item
	item.Status = domain.StagedAttachmentDeleted
	return item, nil
}

func apiRouteFixtures() (*apiRepositoryFixture, domain.TaskSnapshot, domain.RunSnapshot) {
	now := time.Unix(100, 0).UTC()
	task := domain.TaskSnapshot{
		ID: "otk_test", PrincipalKind: domain.PrincipalIntegration, PrincipalID: "integration-1",
		ProjectID: "project-1", ProviderID: "codex", Source: domain.Source{Kind: domain.SourceAPI},
		Prompt: "test", Status: domain.TaskCompleted, Version: 3, CreatedAt: now, UpdatedAt: now,
	}
	run := domain.RunSnapshot{ID: "oru_test", TaskID: task.ID, DeliveryID: "odl_test", ProviderID: "codex", Status: domain.RunCompleted, CreatedAt: now}
	repo := &apiRepositoryFixture{task: task, run: run, tasks: store.Page[domain.TaskSnapshot]{Items: []domain.TaskSnapshot{task}}, runs: store.Page[domain.RunSnapshot]{Items: []domain.RunSnapshot{run}}}
	return repo, task, run
}

func serveAPIRoute(t *testing.T, h *Handler, method, target, body string, scopes ...string) *httptest.ResponseRecorder {
	t.Helper()
	router := chi.NewRouter()
	h.Mount(router)
	req := requestWithIntegrationPrincipal(httptest.NewRequest(method, target, strings.NewReader(body)), scopes...)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func TestTaskAndRunReadRoutesReturnOwnerScopedResources(t *testing.T) {
	repo, task, run := apiRouteFixtures()
	h := &Handler{enabled: true, repository: repo, now: func() time.Time { return time.Unix(1, 0).UTC() }}

	list := serveAPIRoute(t, h, http.MethodGet, "/oneshot/tasks?project_id=project-1&status=completed&limit=25", "", scopeTaskRead)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), task.ID) {
		t.Fatalf("list tasks status=%d body=%s", list.Code, list.Body.String())
	}
	if repo.lastTaskQuery.ProjectID != "project-1" || repo.lastTaskQuery.Status != domain.TaskCompleted || repo.lastTaskQuery.Page.Limit != 25 {
		t.Fatalf("unexpected task filter: %+v", repo.lastTaskQuery)
	}

	getTask := serveAPIRoute(t, h, http.MethodGet, "/oneshot/tasks/"+task.ID+"?project_id=project-1", "", scopeTaskRead)
	if getTask.Code != http.StatusOK || !strings.Contains(getTask.Body.String(), task.ID) {
		t.Fatalf("get task status=%d body=%s", getTask.Code, getTask.Body.String())
	}
	listRuns := serveAPIRoute(t, h, http.MethodGet, "/oneshot/tasks/"+task.ID+"/runs?project_id=project-1", "", scopeRunRead)
	if listRuns.Code != http.StatusOK || !strings.Contains(listRuns.Body.String(), run.ID) {
		t.Fatalf("list runs status=%d body=%s", listRuns.Code, listRuns.Body.String())
	}
	getRun := serveAPIRoute(t, h, http.MethodGet, "/oneshot/runs/"+run.ID+"?project_id=project-1", "", scopeRunRead)
	if getRun.Code != http.StatusOK || !strings.Contains(getRun.Body.String(), run.ID) {
		t.Fatalf("get run status=%d body=%s", getRun.Code, getRun.Body.String())
	}
}

func TestContinueCancelAndRetryRoutesCallApplicationServices(t *testing.T) {
	repo, task, _ := apiRouteFixtures()
	delivery := domain.DeliverySnapshot{ID: "odl_test", TaskID: task.ID}
	continuer := &apiContinuerFixture{result: application.ContinueTaskResult{Task: task, Delivery: delivery, Created: true}}
	controller := &apiControllerFixture{
		cancel: application.CancelTaskResult{Task: func() domain.TaskSnapshot { out := task; out.Status = domain.TaskCancelled; return out }()},
		retry:  application.RetryTaskResult{Task: task, Delivery: delivery, Created: true},
	}
	h := &Handler{enabled: true, repository: repo, continuer: continuer, controller: controller, now: func() time.Time { return time.Unix(1, 0).UTC() }}

	continueReq := httptest.NewRequest(http.MethodPost, "/oneshot/tasks/"+task.ID+"/continue", strings.NewReader(`{"project_id":"project-1","provider_id":"codex","prompt_delta":"next"}`))
	continueReq = requestWithIntegrationPrincipal(continueReq, scopeTaskContinue)
	continueReq.Header.Set("Idempotency-Key", "continue-1")
	router := chi.NewRouter()
	h.Mount(router)
	continueRes := httptest.NewRecorder()
	router.ServeHTTP(continueRes, continueReq)
	if continueRes.Code != http.StatusAccepted || continuer.calls != 1 || continuer.command.PromptDelta != "next" {
		t.Fatalf("continue status=%d calls=%d command=%+v body=%s", continueRes.Code, continuer.calls, continuer.command, continueRes.Body.String())
	}

	cancel := serveAPIRoute(t, h, http.MethodPost, "/oneshot/tasks/"+task.ID+"/cancel?project_id=project-1", "", scopeTaskCancel)
	if cancel.Code != http.StatusOK || controller.cancelCalls != 1 {
		t.Fatalf("cancel status=%d calls=%d body=%s", cancel.Code, controller.cancelCalls, cancel.Body.String())
	}

	retryReq := httptest.NewRequest(http.MethodPost, "/oneshot/tasks/"+task.ID+"/retry", strings.NewReader(`{"project_id":"project-1","prompt_delta":"retry"}`))
	retryReq = requestWithIntegrationPrincipal(retryReq, scopeTaskRetry)
	retryReq.Header.Set("Idempotency-Key", "retry-1")
	retryRes := httptest.NewRecorder()
	router.ServeHTTP(retryRes, retryReq)
	if retryRes.Code != http.StatusAccepted || controller.retryCalls != 1 {
		t.Fatalf("retry status=%d calls=%d body=%s", retryRes.Code, controller.retryCalls, retryRes.Body.String())
	}
}

func TestRunEventsArtifactsAndDownloadRoutes(t *testing.T) {
	repo, task, run := apiRouteFixtures()
	now := time.Unix(100, 0).UTC()
	repo.events = []domain.StandardEventSnapshot{{ID: "oev_test", RunID: run.ID, Sequence: 1, Type: "provider.output", AdapterID: "codex", AdapterVersion: "1.0.0", Content: map[string]any{"text": "done"}, OccurredAt: now}}
	runID := run.ID
	repo.artifact = domain.ArtifactSnapshot{ID: "oar_test", TaskID: task.ID, RunID: &runID, Kind: domain.ArtifactFinalResult, Name: "result.txt", ContentType: "text/plain", SizeBytes: 4, SHA256: strings.Repeat("ab", 32), StorageKey: "runs/result.txt", CreatedAt: now}
	repo.artifacts = store.Page[domain.ArtifactSnapshot]{Items: []domain.ArtifactSnapshot{repo.artifact}}
	h := &Handler{enabled: true, repository: repo, artifacts: apiArtifactStorageFixture{body: "done"}, now: func() time.Time { return time.Unix(1, 0).UTC() }}

	events := serveAPIRoute(t, h, http.MethodGet, "/oneshot/runs/"+run.ID+"/events?project_id=project-1&limit=10", "", scopeRunRead)
	if events.Code != http.StatusOK || !strings.Contains(events.Body.String(), "provider.output") {
		t.Fatalf("events status=%d body=%s", events.Code, events.Body.String())
	}
	artifacts := serveAPIRoute(t, h, http.MethodGet, "/oneshot/runs/"+run.ID+"/artifacts?project_id=project-1", "", scopeArtifactRead)
	if artifacts.Code != http.StatusOK || !strings.Contains(artifacts.Body.String(), repo.artifact.ID) {
		t.Fatalf("artifacts status=%d body=%s", artifacts.Code, artifacts.Body.String())
	}
	download := serveAPIRoute(t, h, http.MethodGet, "/oneshot/artifacts/"+repo.artifact.ID+"?project_id=project-1", "", scopeArtifactRead)
	if download.Code != http.StatusOK || download.Body.String() != "done" || download.Header().Get("Digest") == "" || download.Header().Get("ETag") == "" {
		t.Fatalf("download status=%d headers=%v body=%s", download.Code, download.Header(), download.Body.String())
	}
}

func TestAttachmentStageReadAndDeleteRoutes(t *testing.T) {
	repo, _, _ := apiRouteFixtures()
	now := time.Unix(100, 0).UTC()
	attachment := &apiAttachmentFixture{item: domain.StagedAttachmentSnapshot{
		ID: "oat_test", PrincipalKind: domain.PrincipalIntegration, PrincipalID: "integration-1", ProjectID: "project-1",
		SourceKind: domain.SourceAPI, Name: "input.txt", DetectedMIME: "text/plain", SizeBytes: 5,
		SHA256: strings.Repeat("ab", 32), Status: domain.StagedAttachmentReady, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}}
	h := &Handler{enabled: true, repository: repo, attachments: attachment, attachmentMaxBytes: 1024, now: func() time.Time { return time.Unix(1, 0).UTC() }}
	router := chi.NewRouter()
	h.Mount(router)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("project_id", "project-1"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "input.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := requestWithIntegrationPrincipal(httptest.NewRequest(http.MethodPost, "/oneshot/attachments", &body), scopeTaskCreate)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	stage := httptest.NewRecorder()
	router.ServeHTTP(stage, req)
	if stage.Code != http.StatusCreated || attachment.stageCalls != 1 {
		t.Fatalf("stage status=%d calls=%d body=%s", stage.Code, attachment.stageCalls, stage.Body.String())
	}
	get := serveAPIRoute(t, h, http.MethodGet, "/oneshot/attachments/oat_test?project_id=project-1", "", scopeArtifactRead)
	if get.Code != http.StatusOK || attachment.getCalls == 0 {
		t.Fatalf("get attachment status=%d calls=%d body=%s", get.Code, attachment.getCalls, get.Body.String())
	}
	deleted := serveAPIRoute(t, h, http.MethodDelete, "/oneshot/attachments/oat_test?project_id=project-1", "", scopeTaskCreate)
	if deleted.Code != http.StatusNoContent || attachment.deleteCalls != 1 {
		t.Fatalf("delete attachment status=%d calls=%d body=%s", deleted.Code, attachment.deleteCalls, deleted.Body.String())
	}
}
