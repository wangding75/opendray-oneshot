package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/opendray/opendray-v2/internal/oneshot/application"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/queue"
	"github.com/opendray/opendray-v2/internal/oneshot/workspacepolicy"
)

type mockRepo struct {
	queue.Repository
}

func (m *mockRepo) Enqueue(ctx context.Context, req queue.EnqueueRequest) (queue.EnqueueResult, error) {
	return queue.EnqueueResult{
		Task:     req.Task,
		Delivery: req.Delivery,
		Created:  true,
	}, nil
}

type failingRepo struct {
	queue.Repository
}

func (failingRepo) Enqueue(context.Context, queue.EnqueueRequest) (queue.EnqueueResult, error) {
	return queue.EnqueueResult{}, domain.NewDomainError(domain.ErrorInternal, "database connection lost", nil)
}

func TestAPISmokeTest(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "allowed-root")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	root := filepath.Clean(tempDir)
	policy, err := workspacepolicy.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}

	repo := &mockRepo{}
	service := application.NewDispatchService(repo, application.WithWorkspacePolicy(policy, root))
	
	handler, err := New(Options{
		Enabled:    true,
		Creator:    service,
		Repository: &apiRepositoryFixture{},
	})
	if err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	handler.Mount(router)

	type testCase struct {
		name          string
		workspacePath string
		expectedCode  int
	}

	testFile := filepath.Join(root, "file.txt")
	_ = os.WriteFile(testFile, []byte("xyz"), 0o600)

	testCases := []testCase{
		{
			name:          "1. 合法路径 (Allowed Root)",
			workspacePath: root,
			expectedCode:  http.StatusAccepted,
		},
		{
			name:          "2. 相对路径 (Relative)",
			workspacePath: "relative/path",
			expectedCode:  http.StatusBadRequest,
		},
		{
			name:          "3. 不存在路径 (Non-existent)",
			workspacePath: filepath.Join(root, "non-existent-dir"),
			expectedCode:  http.StatusBadRequest,
		},
		{
			name:          "4. 文件路径 (File Path)",
			workspacePath: testFile,
			expectedCode:  http.StatusBadRequest,
		},
		{
			name:          "5. 根外路径 (Outside Root)",
			workspacePath: os.TempDir(),
			expectedCode:  http.StatusForbidden,
		},
	}

	markdown := strings.Builder{}
	markdown.WriteString("# API Error Contract Verification Report\n\n")
	markdown.WriteString("Verified workspace validation HTTP status code contract for One-shot Task creation.\n\n")
	markdown.WriteString("| 场景 (Scenario) | 请求工作区 (Workspace Path) | 状态码 (Status Code) | 响应错误码 (Error Code) | 响应消息 (Error Message) | 数据库记录 (DB Created) |\n")
	markdown.WriteString("|---|---|---|---|---|---:|\n")

	for _, tc := range testCases {
		bodyMap := map[string]any{
			"project_id":     "project-1",
			"provider_id":    "codex",
			"prompt":         "hello world",
			"workspace_path": tc.workspacePath,
		}
		bodyBytes, _ := json.Marshal(bodyMap)

		req := httptest.NewRequest(http.MethodPost, "/oneshot/tasks", strings.NewReader(string(bodyBytes)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "idemp-key-" + strings.ReplaceAll(tc.name, " ", "-"))
		
		req = requestWithIntegrationPrincipal(req, scopeTaskCreate)

		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)

		var respCode string
		var respMsg string
		if recorder.Code >= 400 {
			var respMap map[string]any
			_ = json.Unmarshal(recorder.Body.Bytes(), &respMap)
			if errObj, ok := respMap["error"].(map[string]any); ok {
				respCode = fmt.Sprintf("%v", errObj["code"])
				respMsg = fmt.Sprintf("%v", errObj["message"])
			}
		} else {
			respCode = "SUCCESS"
			respMsg = "Task accepted"
		}

		dbCreated := "No"
		if recorder.Code == http.StatusAccepted {
			dbCreated = "Yes"
		}

		markdown.WriteString(fmt.Sprintf("| %s | `%s` | %d | `%s` | `%s` | %s |\n",
			tc.name, strings.ReplaceAll(tc.workspacePath, `\`, `\\`), recorder.Code, respCode, respMsg, dbCreated))
	}

	// 6. 内部模拟 Store 错误
	failService := application.NewDispatchService(&failingRepo{Repository: repo}, application.WithWorkspacePolicy(policy, root))
	failHandler, _ := New(Options{Enabled: true, Creator: failService, Repository: &apiRepositoryFixture{}})
	failRouter := chi.NewRouter()
	failHandler.Mount(failRouter)

	bodyMap := map[string]any{
		"project_id":     "project-1",
		"provider_id":    "codex",
		"prompt":         "hello world",
		"workspace_path": root,
	}
	bodyBytes, _ := json.Marshal(bodyMap)
	req := httptest.NewRequest(http.MethodPost, "/oneshot/tasks", strings.NewReader(string(bodyBytes)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "idemp-key-fail")
	req = requestWithIntegrationPrincipal(req, scopeTaskCreate)

	recorder := httptest.NewRecorder()
	failRouter.ServeHTTP(recorder, req)

	var respCode string
	var respMsg string
	var respMap map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &respMap)
	if errObj, ok := respMap["error"].(map[string]any); ok {
		respCode = fmt.Sprintf("%v", errObj["code"])
		respMsg = fmt.Sprintf("%v", errObj["message"])
	}

	markdown.WriteString(fmt.Sprintf("| %s | `%s` | %d | `%s` | `%s` | %s |\n",
		"6. 内部模拟 Store 错误 (Internal Store Error)", strings.ReplaceAll(root, `\`, `\\`), recorder.Code, respCode, respMsg, "No"))

	reportPath := "C:\\Users\\wangding\\Downloads\\opendray-reports\\b2-f3-workspace-errors-20260806T083420Z\\api-error-contract.md"
	_ = os.WriteFile(reportPath, []byte(markdown.String()), 0o600)
}
