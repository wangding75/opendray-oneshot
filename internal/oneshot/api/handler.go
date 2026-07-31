// Package api exposes the frozen One-shot REST and WebSocket control plane.
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/opendray/opendray-v2/internal/integration"
	"github.com/opendray/opendray-v2/internal/oneshot/application"
	attachmentservice "github.com/opendray/opendray-v2/internal/oneshot/attachment"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/store"
)

const basePath = "/oneshot"

type requestIDContextKey struct{}

const (
	scopeTaskCreate     = "oneshot:task:create"
	scopeTaskRead       = "oneshot:task:read"
	scopeTaskContinue   = "oneshot:task:continue"
	scopeTaskCancel     = "oneshot:task:cancel"
	scopeTaskRetry      = "oneshot:task:retry"
	scopeRunRead        = "oneshot:run:read"
	scopeArtifactRead   = "oneshot:artifact:read"
	scopeTaskSubscribe  = "event:subscribe:oneshot.task.*"
	scopeRunSubscribe   = "event:subscribe:oneshot.run.*"
	defaultStreamBuffer = 64

	actionTaskCreate       = "oneshot.task.create"
	actionTaskList         = "oneshot.task.list"
	actionTaskRead         = "oneshot.task.read"
	actionTaskContinue     = "oneshot.task.continue"
	actionTaskCancel       = "oneshot.task.cancel"
	actionTaskRetry        = "oneshot.task.retry"
	actionRunList          = "oneshot.run.list"
	actionRunRead          = "oneshot.run.read"
	actionRunEventsRead    = "oneshot.run.events.read"
	actionRunArtifactsRead = "oneshot.run.artifacts.read"
	actionArtifactRead     = "oneshot.artifact.read"
	actionAttachmentStage  = "oneshot.attachment.stage"
	actionAttachmentRead   = "oneshot.attachment.read"
	actionAttachmentDelete = "oneshot.attachment.delete"
	actionTaskStream       = "oneshot.task.stream"
	actionRunStream        = "oneshot.run.stream"
)

type TaskCreator interface {
	CreateTask(context.Context, application.CreateTaskCommand) (application.CreateTaskResult, error)
}

type TaskContinuer interface {
	Continue(context.Context, application.ContinueTaskCommand) (application.ContinueTaskResult, error)
}

type TaskController interface {
	CancelTask(context.Context, application.CancelTaskCommand) (application.CancelTaskResult, error)
	RetryTask(context.Context, application.RetryTaskCommand) (application.RetryTaskResult, error)
}

type Repository interface {
	GetTask(context.Context, domain.Owner, string) (domain.TaskSnapshot, error)
	ListTasksFiltered(context.Context, domain.Owner, store.TaskListFilter) (store.Page[domain.TaskSnapshot], error)
	ListRuns(context.Context, domain.Owner, string, store.PageRequest) (store.Page[domain.RunSnapshot], error)
	GetRun(context.Context, domain.Owner, string) (domain.RunSnapshot, error)
	ListStandardEvents(context.Context, domain.Owner, string, int64, int) ([]domain.StandardEventSnapshot, error)
	ListTaskReplayEvents(context.Context, domain.Owner, string, string, store.ReplayCursor, int) ([]store.ReplayEvent, error)
	ListRunReplayEvents(context.Context, domain.Owner, string, string, store.ReplayCursor, int) ([]store.ReplayEvent, error)
	ListArtifacts(context.Context, domain.Owner, string, string, store.PageRequest) (store.Page[domain.ArtifactSnapshot], error)
	GetArtifact(context.Context, domain.Owner, string) (domain.ArtifactSnapshot, error)
}

type ArtifactStorage interface {
	Open(context.Context, string) (io.ReadCloser, error)
}

type AttachmentService interface {
	Stage(context.Context, attachmentservice.StageRequest) (domain.StagedAttachmentSnapshot, error)
	Get(context.Context, domain.Owner, string) (domain.StagedAttachmentSnapshot, error)
	Delete(context.Context, domain.Owner, string) (domain.StagedAttachmentSnapshot, error)
}

type AuditRecord struct {
	ActorKind      string         `json:"actor_kind"`
	ActorID        string         `json:"actor_id"`
	Action         string         `json:"action"`
	ResourceKind   string         `json:"resource_kind"`
	ResourceID     string         `json:"resource_id,omitempty"`
	ProjectID      string         `json:"project_id,omitempty"`
	Result         string         `json:"result"`
	RequestID      string         `json:"request_id"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	AdminBypass    bool           `json:"admin_bypass"`
	OccurredAt     time.Time      `json:"occurred_at"`
	Details        map[string]any `json:"details,omitempty"`
}

type Auditor interface {
	Record(context.Context, AuditRecord)
}

type Handler struct {
	enabled            bool
	creator            TaskCreator
	continuer          TaskContinuer
	controller         TaskController
	repository         Repository
	artifacts          ArtifactStorage
	attachments        AttachmentService
	attachmentMaxBytes int64
	auditor            Auditor
	log                *slog.Logger
	now                func() time.Time
	upgrader           websocket.Upgrader
	streamPoll         time.Duration
}

type Options struct {
	Enabled            bool
	Creator            TaskCreator
	Continuer          TaskContinuer
	Controller         TaskController
	Repository         Repository
	Artifacts          ArtifactStorage
	Attachments        AttachmentService
	AttachmentMaxBytes int64
	Auditor            Auditor
	Log                *slog.Logger
	StreamPoll         time.Duration
}

func New(options Options) (*Handler, error) {
	if options.Repository == nil {
		return nil, domain.InvalidRequestf("One-shot API repository is required")
	}
	if options.Log == nil {
		options.Log = slog.Default()
	}
	if options.StreamPoll <= 0 {
		options.StreamPoll = 250 * time.Millisecond
	}
	if options.AttachmentMaxBytes <= 0 {
		options.AttachmentMaxBytes = attachmentservice.DefaultMaxBytes
	}
	return &Handler{
		enabled: options.Enabled, creator: options.Creator, continuer: options.Continuer,
		controller: options.Controller, repository: options.Repository, artifacts: options.Artifacts, attachments: options.Attachments, attachmentMaxBytes: options.AttachmentMaxBytes,
		auditor: options.Auditor, log: options.Log.With("component", "oneshot-api"),
		now: func() time.Time { return time.Now().UTC() }, streamPoll: options.StreamPoll,
		upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" || sameOrigin(r) }},
	}, nil
}

func sameOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Host, r.Host)
}

// Mount registers the frozen control-plane routes plus the OD-OS-24 attachment extension.
func (h *Handler) Mount(r chi.Router) {
	r.Route(basePath, func(r chi.Router) {
		r.Use(h.requestIDMiddleware)
		r.Post("/tasks", h.createTask)
		r.Get("/tasks", h.listTasks)
		r.Get("/tasks/stream", h.streamTasks)
		r.Get("/tasks/{task_id}", h.getTask)
		r.Post("/tasks/{task_id}/continue", h.continueTask)
		r.Post("/tasks/{task_id}/cancel", h.cancelTask)
		r.Post("/tasks/{task_id}/retry", h.retryTask)
		r.Get("/tasks/{task_id}/runs", h.listRuns)
		r.Get("/runs/{run_id}", h.getRun)
		r.Get("/runs/{run_id}/events", h.listRunEvents)
		r.Get("/runs/{run_id}/stream", h.streamRun)
		r.Get("/runs/{run_id}/artifacts", h.listArtifacts)
		r.Get("/artifacts/{artifact_id}", h.downloadArtifact)
		r.Post("/attachments", h.stageAttachment)
		r.Get("/attachments/{attachment_id}", h.getStagedAttachment)
		r.Delete("/attachments/{attachment_id}", h.deleteStagedAttachment)
	})
}

type createTaskRequest struct {
	ProjectID      string         `json:"project_id"`
	ProviderID     string         `json:"provider_id"`
	Prompt         string         `json:"prompt"`
	WorkspacePath  string         `json:"workspace_path"`
	AttachmentRefs []string       `json:"attachment_refs"`
	Attachments    []string       `json:"attachments"`
	Options        map[string]any `json:"options"`
	Source         *domain.Source `json:"source,omitempty"`
	MaxAttempts    int            `json:"max_attempts"`
	TimeoutSeconds int            `json:"timeout_seconds"`
}

func (h *Handler) createTask(w http.ResponseWriter, r *http.Request) {
	principal, owner, ok := h.authorize(w, r, scopeTaskCreate, actionTaskCreate, "task", "")
	if !ok {
		return
	}
	if !h.requireEnabled(w, r) {
		h.auditRejectedWrite(r, principal, actionTaskCreate, "task", "", "", "", domain.ErrorDisabled, "One-shot execution is disabled")
		return
	}
	if h.creator == nil {
		err := domain.NewDomainError(domain.ErrorDisabled, "One-shot task creation is unavailable", nil)
		h.auditFailure(r.Context(), principal, actionTaskCreate, "task", "", "", "", err)
		h.writeError(w, r, err)
		return
	}
	key, ok := requireIdempotencyKey(w, r)
	if !ok {
		h.auditRejectedWrite(r, principal, actionTaskCreate, "task", "", "", "", domain.ErrorIdempotencyRequired, "Idempotency-Key is required")
		return
	}
	var req createTaskRequest
	if !decodeJSON(w, r, &req) {
		h.auditRejectedWrite(r, principal, actionTaskCreate, "task", "", "", key, domain.ErrorInvalidRequest, "invalid JSON request")
		return
	}
	source, sourceErr := sourceFromRESTRequest(req.Source, requestID(r))
	if sourceErr != nil {
		h.auditFailure(r.Context(), principal, actionTaskCreate, "task", "", req.ProjectID, key, sourceErr)
		h.writeError(w, r, sourceErr)
		return
	}
	options := cloneMap(req.Options)
	if strings.TrimSpace(req.WorkspacePath) != "" {
		options["workspace_path"] = strings.TrimSpace(req.WorkspacePath)
	}
	if req.TimeoutSeconds > 0 {
		options["timeout_seconds"] = req.TimeoutSeconds
	}
	if boolOption(options, "telegram_notify") && source.ReplyAddress == nil {
		reader, available := h.repository.(notificationPreferenceReader)
		if !available {
			err := domain.NewDomainError(domain.ErrorDisabled, "Telegram notification preferences are unavailable", nil)
			h.auditFailure(r.Context(), principal, actionTaskCreate, "task", "", req.ProjectID, key, err)
			h.writeError(w, r, err)
			return
		}
		pref, prefErr := reader.GetNotificationPreference(r.Context(), owner, strings.TrimSpace(req.ProjectID))
		if prefErr != nil {
			err := domain.InvalidRequestf("Telegram notification was requested but no owner/project destination is registered")
			h.auditFailure(r.Context(), principal, actionTaskCreate, "task", "", req.ProjectID, key, err)
			h.writeError(w, r, err)
			return
		}
		source.ReplyAddress = &domain.ReplyAddress{
			ChannelID: pref.ChannelID, ConversationID: pref.ConversationID, ThreadID: pref.ThreadID,
			MessageID: pref.MessageID, Metadata: pref.Metadata,
		}
	}
	attachments := append([]string(nil), req.AttachmentRefs...)
	attachments = append(attachments, req.Attachments...)
	result, err := h.creator.CreateTask(r.Context(), application.CreateTaskCommand{Owner: owner, ProjectID: req.ProjectID, ProviderID: req.ProviderID, Source: source, Prompt: req.Prompt, Input: domain.DeliveryInput{AttachmentRefs: attachments, Options: options}, IdempotencyKey: key, MaxAttempts: req.MaxAttempts})
	if err != nil {
		h.auditFailure(r.Context(), principal, actionTaskCreate, "task", "", req.ProjectID, key, err)
		h.writeError(w, r, err)
		return
	}
	details := map[string]any{"created": result.Created, "provider_id": result.Task.ProviderID, "source": result.Task.Source.Kind}
	if result.Task.Source.ReplyAddress != nil {
		details["reply_channel_id"] = result.Task.Source.ReplyAddress.ChannelID
		details["reply_conversation_hash"] = auditKeyHash(result.Task.Source.ReplyAddress.ConversationID)
		details["cross_device_notification"] = true
	}
	h.auditSuccess(r.Context(), principal, actionTaskCreate, "task", result.Task.ID, result.Task.ProjectID, key, details)
	status := http.StatusAccepted
	if !result.Created {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"task": result.Task, "delivery": result.Delivery, "created": result.Created})
}

func (h *Handler) listTasks(w http.ResponseWriter, r *http.Request) {
	_, owner, ok := h.authorize(w, r, scopeTaskRead, actionTaskList, "task", "")
	if !ok {
		return
	}
	page, err := h.repository.ListTasksFiltered(r.Context(), owner, store.TaskListFilter{ProjectID: r.URL.Query().Get("project_id"), Status: domain.TaskStatus(r.URL.Query().Get("status")), Page: pageRequest(r)})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *Handler) getTask(w http.ResponseWriter, r *http.Request) {
	principal, owner, ok := h.authorize(w, r, scopeTaskRead, actionTaskRead, "task", chi.URLParam(r, "task_id"))
	if !ok {
		return
	}
	taskID := chi.URLParam(r, "task_id")
	task, err := h.repository.GetTask(r.Context(), owner, taskID)
	if err != nil {
		masked := maskForbidden(err, domain.ErrorTaskNotFound)
		h.auditFailure(r.Context(), principal, actionTaskRead, "task", taskID, r.URL.Query().Get("project_id"), "", masked)
		h.writeError(w, r, masked)
		return
	}
	if !h.projectAllowed(w, r, principal, actionTaskRead, "task", task.ID, task.ProjectID) {
		return
	}
	writeJSON(w, http.StatusOK, task)
}

type continueTaskRequest struct {
	ProjectID      string         `json:"project_id"`
	ProviderID     string         `json:"provider_id"`
	WorkspacePath  string         `json:"workspace_path"`
	PromptDelta    string         `json:"prompt_delta"`
	AttachmentRefs []string       `json:"attachment_refs"`
	Options        map[string]any `json:"options"`
	MaxAttempts    int            `json:"max_attempts"`
}

func (h *Handler) continueTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "task_id")
	principal, owner, ok := h.authorize(w, r, scopeTaskContinue, actionTaskContinue, "task", taskID)
	if !ok {
		return
	}
	if !h.requireEnabled(w, r) {
		h.auditRejectedWrite(r, principal, actionTaskContinue, "task", taskID, "", "", domain.ErrorDisabled, "One-shot execution is disabled")
		return
	}
	if h.continuer == nil {
		err := domain.NewDomainError(domain.ErrorDisabled, "One-shot continuation is unavailable", nil)
		h.auditFailure(r.Context(), principal, actionTaskContinue, "task", taskID, "", "", err)
		h.writeError(w, r, err)
		return
	}
	key, ok := requireIdempotencyKey(w, r)
	if !ok {
		h.auditRejectedWrite(r, principal, actionTaskContinue, "task", taskID, "", "", domain.ErrorIdempotencyRequired, "Idempotency-Key is required")
		return
	}
	var req continueTaskRequest
	if !decodeJSON(w, r, &req) {
		h.auditRejectedWrite(r, principal, actionTaskContinue, "task", taskID, "", key, domain.ErrorInvalidRequest, "invalid JSON request")
		return
	}
	result, err := h.continuer.Continue(r.Context(), application.ContinueTaskCommand{Owner: owner, ProjectID: req.ProjectID, TaskID: taskID, ProviderID: req.ProviderID, WorkspacePath: req.WorkspacePath, PromptDelta: req.PromptDelta, AttachmentRefs: req.AttachmentRefs, Options: req.Options, IdempotencyKey: key, MaxAttempts: req.MaxAttempts})
	if err != nil {
		h.auditFailure(r.Context(), principal, actionTaskContinue, "task", taskID, req.ProjectID, key, err)
		h.writeError(w, r, maskForbidden(err, domain.ErrorTaskNotFound))
		return
	}
	h.auditSuccess(r.Context(), principal, actionTaskContinue, "task", taskID, result.Task.ProjectID, key, map[string]any{"created": result.Created, "provider_id": result.Task.ProviderID})
	status := http.StatusAccepted
	if !result.Created {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

type projectRequest struct {
	ProjectID string `json:"project_id"`
}

func (h *Handler) cancelTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "task_id")
	principal, owner, ok := h.authorize(w, r, scopeTaskCancel, actionTaskCancel, "task", taskID)
	if !ok {
		return
	}
	if !h.requireEnabled(w, r) {
		h.auditRejectedWrite(r, principal, actionTaskCancel, "task", taskID, "", r.Header.Get("Idempotency-Key"), domain.ErrorDisabled, "One-shot execution is disabled")
		return
	}
	if h.controller == nil {
		err := domain.NewDomainError(domain.ErrorDisabled, "One-shot control is unavailable", nil)
		h.auditFailure(r.Context(), principal, actionTaskCancel, "task", taskID, "", r.Header.Get("Idempotency-Key"), err)
		h.writeError(w, r, err)
		return
	}
	var req projectRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		h.auditRejectedWrite(r, principal, actionTaskCancel, "task", taskID, "", r.Header.Get("Idempotency-Key"), domain.ErrorInvalidRequest, "invalid JSON request")
		return
	}
	if req.ProjectID == "" {
		req.ProjectID = r.URL.Query().Get("project_id")
	}
	result, err := h.controller.CancelTask(r.Context(), application.CancelTaskCommand{Owner: owner, ProjectID: req.ProjectID, TaskID: taskID})
	if err != nil {
		h.auditFailure(r.Context(), principal, actionTaskCancel, "task", taskID, req.ProjectID, r.Header.Get("Idempotency-Key"), err)
		h.writeError(w, r, maskForbidden(err, domain.ErrorTaskNotFound))
		return
	}
	h.auditSuccess(r.Context(), principal, actionTaskCancel, "task", taskID, result.Task.ProjectID, r.Header.Get("Idempotency-Key"), map[string]any{"noop": result.Noop, "provider_id": result.Task.ProviderID})
	status := http.StatusAccepted
	if result.Noop || result.Task.Status == domain.TaskCancelled {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

type retryTaskRequest struct {
	ProjectID      string         `json:"project_id"`
	PromptDelta    string         `json:"prompt_delta,omitempty"`
	AttachmentRefs []string       `json:"attachment_refs"`
	Options        map[string]any `json:"options"`
	MaxAttempts    int            `json:"max_attempts"`
}

func (h *Handler) retryTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "task_id")
	principal, owner, ok := h.authorize(w, r, scopeTaskRetry, actionTaskRetry, "task", taskID)
	if !ok {
		return
	}
	if !h.requireEnabled(w, r) {
		h.auditRejectedWrite(r, principal, actionTaskRetry, "task", taskID, "", "", domain.ErrorDisabled, "One-shot execution is disabled")
		return
	}
	if h.controller == nil {
		err := domain.NewDomainError(domain.ErrorDisabled, "One-shot control is unavailable", nil)
		h.auditFailure(r.Context(), principal, actionTaskRetry, "task", taskID, "", "", err)
		h.writeError(w, r, err)
		return
	}
	key, ok := requireIdempotencyKey(w, r)
	if !ok {
		h.auditRejectedWrite(r, principal, actionTaskRetry, "task", taskID, "", "", domain.ErrorIdempotencyRequired, "Idempotency-Key is required")
		return
	}
	var req retryTaskRequest
	if !decodeJSON(w, r, &req) {
		h.auditRejectedWrite(r, principal, actionTaskRetry, "task", taskID, "", key, domain.ErrorInvalidRequest, "invalid JSON request")
		return
	}
	result, err := h.controller.RetryTask(r.Context(), application.RetryTaskCommand{Owner: owner, ProjectID: req.ProjectID, TaskID: taskID, Input: domain.DeliveryInput{PromptDelta: req.PromptDelta, AttachmentRefs: req.AttachmentRefs, Options: req.Options}, IdempotencyKey: key, MaxAttempts: req.MaxAttempts})
	if err != nil {
		h.auditFailure(r.Context(), principal, actionTaskRetry, "task", taskID, req.ProjectID, key, err)
		h.writeError(w, r, maskForbidden(err, domain.ErrorTaskNotFound))
		return
	}
	h.auditSuccess(r.Context(), principal, actionTaskRetry, "task", taskID, result.Task.ProjectID, key, map[string]any{"created": result.Created, "provider_id": result.Task.ProviderID})
	status := http.StatusAccepted
	if !result.Created {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) {
	principal, owner, ok := h.authorize(w, r, scopeRunRead, actionRunList, "task", chi.URLParam(r, "task_id"))
	if !ok {
		return
	}
	taskID := chi.URLParam(r, "task_id")
	task, err := h.repository.GetTask(r.Context(), owner, taskID)
	if err != nil {
		masked := maskForbidden(err, domain.ErrorTaskNotFound)
		h.auditFailure(r.Context(), principal, actionRunList, "task", taskID, r.URL.Query().Get("project_id"), "", masked)
		h.writeError(w, r, masked)
		return
	}
	if !h.projectAllowed(w, r, principal, actionRunList, "task", task.ID, task.ProjectID) {
		return
	}
	page, err := h.repository.ListRuns(r.Context(), owner, task.ID, pageRequest(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) {
	principal, owner, ok := h.authorize(w, r, scopeRunRead, actionRunRead, "run", chi.URLParam(r, "run_id"))
	if !ok {
		return
	}
	run, task, err := h.authorizedRun(r.Context(), owner, chi.URLParam(r, "run_id"), r.URL.Query().Get("project_id"))
	if err != nil {
		h.auditFailure(r.Context(), principal, actionRunRead, "run", chi.URLParam(r, "run_id"), r.URL.Query().Get("project_id"), "", err)
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run, "task": task})
}

func (h *Handler) listRunEvents(w http.ResponseWriter, r *http.Request) {
	principal, owner, ok := h.authorize(w, r, scopeRunRead, actionRunEventsRead, "run", chi.URLParam(r, "run_id"))
	if !ok {
		return
	}
	run, _, err := h.authorizedRun(r.Context(), owner, chi.URLParam(r, "run_id"), r.URL.Query().Get("project_id"))
	if err != nil {
		h.auditFailure(r.Context(), principal, actionRunEventsRead, "run", chi.URLParam(r, "run_id"), r.URL.Query().Get("project_id"), "", err)
		h.writeError(w, r, err)
		return
	}
	after, err := decodeEventCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	limit, err := listLimit(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	fetchLimit := limit + 1
	if fetchLimit > 200 {
		fetchLimit = 200
	}
	events, err := h.repository.ListStandardEvents(r.Context(), owner, run.ID, after, fetchLimit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var next any
	if len(events) > limit {
		next = encodeEventCursor(events[limit-1].Sequence)
		events = events[:limit]
	} else if limit == 200 && len(events) == limit {
		// The store enforces the frozen maximum of 200. Returning the final
		// sequence as a cursor may produce one empty follow-up page, but never
		// skips an event or exposes a constructible numeric cursor.
		next = encodeEventCursor(events[len(events)-1].Sequence)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": events, "next_cursor": next})
}

func (h *Handler) listArtifacts(w http.ResponseWriter, r *http.Request) {
	principal, owner, ok := h.authorize(w, r, scopeArtifactRead, actionRunArtifactsRead, "run", chi.URLParam(r, "run_id"))
	if !ok {
		return
	}
	run, task, err := h.authorizedRun(r.Context(), owner, chi.URLParam(r, "run_id"), r.URL.Query().Get("project_id"))
	if err != nil {
		h.auditFailure(r.Context(), principal, actionRunArtifactsRead, "run", chi.URLParam(r, "run_id"), r.URL.Query().Get("project_id"), "", err)
		h.writeError(w, r, err)
		return
	}
	page, err := h.repository.ListArtifacts(r.Context(), owner, task.ID, run.ID, pageRequest(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *Handler) downloadArtifact(w http.ResponseWriter, r *http.Request) {
	principal, owner, ok := h.authorize(w, r, scopeArtifactRead, actionArtifactRead, "artifact", chi.URLParam(r, "artifact_id"))
	if !ok {
		return
	}
	artifactID := chi.URLParam(r, "artifact_id")
	artifact, err := h.repository.GetArtifact(r.Context(), owner, artifactID)
	if err != nil {
		masked := maskForbidden(err, domain.ErrorArtifactNotFound)
		h.auditFailure(r.Context(), principal, actionArtifactRead, "artifact", artifactID, r.URL.Query().Get("project_id"), "", masked)
		h.writeError(w, r, masked)
		return
	}
	task, err := h.repository.GetTask(r.Context(), owner, artifact.TaskID)
	if err != nil {
		masked := domain.NewDomainError(domain.ErrorArtifactNotFound, "Artifact not found", nil)
		h.auditFailure(r.Context(), principal, actionArtifactRead, "artifact", artifactID, r.URL.Query().Get("project_id"), "", masked)
		h.writeError(w, r, masked)
		return
	}
	if !h.projectAllowed(w, r, principal, actionArtifactRead, "artifact", artifactID, task.ProjectID) {
		return
	}
	if h.artifacts == nil {
		h.writeError(w, r, domain.NewDomainError(domain.ErrorArtifactUnavailable, "Artifact storage is unavailable", nil))
		return
	}
	reader, err := h.artifacts.Open(r.Context(), artifact.StorageKey)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", artifact.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.SizeBytes, 10))
	w.Header().Set("Digest", "sha-256="+digestBase64(artifact.SHA256))
	w.Header().Set("ETag", `"`+artifact.SHA256+`"`)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", sanitizeFilename(artifact.Name)))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, reader)
}

func (h *Handler) authorizedRun(ctx context.Context, owner domain.Owner, runID, projectID string) (domain.RunSnapshot, domain.TaskSnapshot, error) {
	run, err := h.repository.GetRun(ctx, owner, runID)
	if err != nil {
		return domain.RunSnapshot{}, domain.TaskSnapshot{}, maskForbidden(err, domain.ErrorRunNotFound)
	}
	task, err := h.repository.GetTask(ctx, owner, run.TaskID)
	if err != nil {
		return domain.RunSnapshot{}, domain.TaskSnapshot{}, domain.NewDomainError(domain.ErrorRunNotFound, "Run not found", nil)
	}
	if strings.TrimSpace(projectID) != "" && task.ProjectID != strings.TrimSpace(projectID) {
		return domain.RunSnapshot{}, domain.TaskSnapshot{}, domain.NewDomainError(domain.ErrorRunNotFound, "Run not found", nil)
	}
	return run, task, nil
}

// streamRun replays persisted events from cursor and then polls only durable rows.
func (h *Handler) streamRun(w http.ResponseWriter, r *http.Request) {
	principal, owner, ok := h.authorize(w, r, scopeRunSubscribe, actionRunStream, "run", chi.URLParam(r, "run_id"))
	if !ok {
		return
	}
	run, _, err := h.authorizedRun(r.Context(), owner, chi.URLParam(r, "run_id"), r.URL.Query().Get("project_id"))
	if err != nil {
		h.auditFailure(r.Context(), principal, actionRunStream, "run", chi.URLParam(r, "run_id"), r.URL.Query().Get("project_id"), "", err)
		h.writeError(w, r, err)
		return
	}
	cursor, err := decodeReplayCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	h.serveRunStream(r.Context(), conn, owner, run.ID, r.URL.Query().Get("project_id"), cursor)
}

func (h *Handler) serveRunStream(ctx context.Context, conn *websocket.Conn, owner domain.Owner, runID, projectID string, cursor store.ReplayCursor) {
	defer conn.Close()
	ticker := time.NewTicker(h.streamPoll)
	defer ticker.Stop()
	for {
		events, err := h.repository.ListRunReplayEvents(ctx, owner, runID, projectID, cursor, defaultStreamBuffer)
		if err != nil {
			_ = conn.WriteJSON(errorFrame(requestIDFromContext(ctx), err))
			return
		}
		for _, event := range events {
			frame := streamFrame{Topic: event.Topic, TS: event.OccurredAt, Cursor: encodeReplayCursor(event.Cursor), Data: event.Data}
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteJSON(frame); err != nil {
				return
			}
			cursor = event.Cursor
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) streamTasks(w http.ResponseWriter, r *http.Request) {
	principal, owner, ok := h.authorize(w, r, scopeTaskSubscribe, actionTaskStream, "task", "")
	if !ok {
		return
	}
	cursor, err := decodeReplayCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	ticker := time.NewTicker(h.streamPoll)
	defer ticker.Stop()
	for {
		events, listErr := h.repository.ListTaskReplayEvents(r.Context(), owner, r.URL.Query().Get("task_id"), r.URL.Query().Get("project_id"), cursor, defaultStreamBuffer)
		if listErr != nil {
			h.auditFailure(r.Context(), principal, actionTaskStream, "task", r.URL.Query().Get("task_id"), r.URL.Query().Get("project_id"), "", listErr)
			_ = conn.WriteJSON(errorFrame(requestID(r), listErr))
			return
		}
		for _, event := range events {
			cursor = event.Cursor
			frame := streamFrame{Topic: event.Topic, TS: event.OccurredAt, Cursor: encodeReplayCursor(cursor), Data: event.Data}
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteJSON(frame); err != nil {
				return
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

type streamFrame struct {
	Topic  string    `json:"topic"`
	TS     time.Time `json:"ts"`
	Cursor string    `json:"cursor"`
	Data   any       `json:"data"`
}

func encodeReplayCursor(cursor store.ReplayCursor) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodeReplayCursor(value string) (store.ReplayCursor, error) {
	if strings.TrimSpace(value) == "" {
		return store.ReplayCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return store.ReplayCursor{}, domain.InvalidRequestf("invalid cursor")
	}
	var out store.ReplayCursor
	if json.Unmarshal(raw, &out) != nil || out.OccurredAt.IsZero() || strings.TrimSpace(out.Kind) == "" || strings.TrimSpace(out.ID) == "" {
		return store.ReplayCursor{}, domain.InvalidRequestf("invalid cursor")
	}
	if out.Kind != "l" && out.Kind != "o" {
		return store.ReplayCursor{}, domain.InvalidRequestf("invalid cursor")
	}
	out.OccurredAt = out.OccurredAt.UTC()
	return out, nil
}

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request, scope, action, resourceKind, resourceID string) (integration.Principal, domain.Owner, bool) {
	principal, ok := integration.CurrentPrincipal(r.Context())
	if !ok {
		h.writeError(w, r, domain.NewDomainError(domain.ErrorUnauthorized, "authentication is required", nil))
		return integration.Principal{}, domain.Owner{}, false
	}
	kind := domain.PrincipalKind(principal.Kind)
	owner := domain.Owner{Kind: kind, ID: principal.ID}
	if err := owner.Validate(); err != nil {
		h.writeError(w, r, domain.NewDomainError(domain.ErrorUnauthorized, "invalid authenticated principal", err))
		return principal, owner, false
	}
	if principal.Kind != integration.KindAdmin && !integration.HasScope(principal.Scopes, scope) {
		err := domain.NewDomainError(domain.ErrorForbidden, "required scope is missing", nil)
		h.auditFailure(r.Context(), principal, action, resourceKind, resourceID, r.URL.Query().Get("project_id"), r.Header.Get("Idempotency-Key"), err)
		h.writeError(w, r, err)
		return principal, owner, false
	}
	return principal, owner, true
}

func (h *Handler) requireEnabled(w http.ResponseWriter, r *http.Request) bool {
	if h.enabled {
		return true
	}
	h.writeError(w, r, domain.NewDomainError(domain.ErrorDisabled, "One-shot execution is disabled", nil))
	return false
}

func (h *Handler) auditSuccess(ctx context.Context, principal integration.Principal, action, resourceKind, resourceID, projectID, key string, details map[string]any) {
	h.audit(ctx, principal, action, resourceKind, resourceID, projectID, key, "success", details)
}
func (h *Handler) auditFailure(ctx context.Context, principal integration.Principal, action, resourceKind, resourceID, projectID, key string, err error) {
	code, _ := domain.CodeOf(err)
	h.audit(ctx, principal, action, resourceKind, resourceID, projectID, key, "failure", map[string]any{"error_code": code})
}
func (h *Handler) auditRejectedWrite(r *http.Request, principal integration.Principal, action, resourceKind, resourceID, projectID, key string, code domain.ErrorCode, message string) {
	h.auditFailure(r.Context(), principal, action, resourceKind, resourceID, projectID, key, domain.NewDomainError(code, message, nil))
}
func (h *Handler) audit(ctx context.Context, principal integration.Principal, action, resourceKind, resourceID, projectID, key, result string, details map[string]any) {
	if h.auditor == nil {
		return
	}
	h.auditor.Record(ctx, AuditRecord{ActorKind: principal.Kind, ActorID: principal.ID, Action: action, ResourceKind: resourceKind, ResourceID: resourceID, ProjectID: projectID, Result: result, RequestID: requestIDFromContext(ctx), IdempotencyKey: auditKeyHash(key), AdminBypass: principal.Kind == integration.KindAdmin, OccurredAt: h.now(), Details: details})
}

func auditKeyHash(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	code, ok := domain.CodeOf(err)
	if !ok {
		code = domain.ErrorInternal
	}
	message := "internal error"
	retryable := domain.IsRetryableCode(code)
	var de *domain.DomainError
	if errors.As(err, &de) && de != nil && strings.TrimSpace(de.Message) != "" {
		message = de.Message
	}
	status := statusFor(code)
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "request_id": requestID(r), "retryable": retryable, "details": map[string]any{}}})
}

func statusFor(code domain.ErrorCode) int {
	switch code {
	case domain.ErrorUnauthorized:
		return http.StatusUnauthorized
	case domain.ErrorForbidden:
		return http.StatusForbidden
	case domain.ErrorTaskNotFound, domain.ErrorRunNotFound, domain.ErrorArtifactNotFound, domain.ErrorContextNotFound:
		return http.StatusNotFound
	case domain.ErrorIdempotencyConflict, domain.ErrorInvalidTransition, domain.ErrorRunConflict:
		return http.StatusConflict
	case domain.ErrorRateLimited:
		return http.StatusTooManyRequests
	case domain.ErrorDisabled, domain.ErrorProviderUnavailable, domain.ErrorQueueUnavailable, domain.ErrorArtifactUnavailable:
		return http.StatusServiceUnavailable
	case domain.ErrorInvalidRequest, domain.ErrorIdempotencyRequired, domain.ErrorContextOwnerMismatch, domain.ErrorUnsupportedProvider, domain.ErrorResumeUnsupported:
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func maskForbidden(err error, notFound domain.ErrorCode) error {
	if domain.HasCode(err, domain.ErrorForbidden) {
		return domain.NewDomainError(notFound, "resource not found", nil)
	}
	return err
}
func (h *Handler) projectAllowed(w http.ResponseWriter, r *http.Request, principal integration.Principal, action, resourceKind, resourceID, actual string) bool {
	requested := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if requested == "" || requested == actual {
		return true
	}
	err := domain.NewDomainError(domain.ErrorTaskNotFound, "resource not found", nil)
	h.auditFailure(r.Context(), principal, action, resourceKind, resourceID, requested, "", err)
	h.writeError(w, r, err)
	return false
}
func requireIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": domain.ErrorIdempotencyRequired, "message": "Idempotency-Key is required", "request_id": requestID(r), "retryable": false, "details": map[string]any{}}})
		return "", false
	}
	return key, true
}
func pageRequest(r *http.Request) store.PageRequest {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return store.PageRequest{Cursor: r.URL.Query().Get("cursor"), Limit: limit}
}

type eventCursor struct {
	Sequence int64 `json:"sequence"`
}

func encodeEventCursor(sequence int64) string {
	raw, _ := json.Marshal(eventCursor{Sequence: sequence})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeEventCursor(value string) (int64, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, domain.InvalidRequestf("invalid cursor")
	}
	var cursor eventCursor
	if json.Unmarshal(raw, &cursor) != nil || cursor.Sequence <= 0 {
		return 0, domain.InvalidRequestf("invalid cursor")
	}
	return cursor.Sequence, nil
}

func listLimit(r *http.Request) (int, error) {
	value := strings.TrimSpace(r.URL.Query().Get("limit"))
	if value == "" {
		return 50, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 200 {
		return 0, domain.InvalidRequestf("limit must be between 1 and 200")
	}
	return limit, nil
}
func sourceFromRESTRequest(input *domain.Source, fallbackRequestID string) (domain.Source, error) {
	source := domain.Source{Kind: domain.SourceAPI, ClientRequestID: strings.TrimSpace(fallbackRequestID)}
	if input == nil {
		return source, nil
	}
	switch input.Kind {
	case "", domain.SourceAPI:
		source.Kind = domain.SourceAPI
	case domain.SourceMobile, domain.SourceWeb:
		source.Kind = input.Kind
	case domain.SourceTelegram:
		return domain.Source{}, domain.InvalidRequestf("REST callers cannot create Telegram source identities")
	default:
		return domain.Source{}, domain.InvalidRequestf("invalid source.kind %q", input.Kind)
	}
	if strings.TrimSpace(input.ChannelID) != "" || strings.TrimSpace(input.SourceMessageID) != "" || input.ReplyAddress != nil {
		return domain.Source{}, domain.InvalidRequestf("REST source routing fields are server-controlled")
	}
	if value := strings.TrimSpace(input.ClientRequestID); value != "" {
		source.ClientRequestID = value
	}
	if len(input.Metadata) > 0 {
		source.Metadata = make(map[string]string, len(input.Metadata))
		for key, value := range input.Metadata {
			source.Metadata[key] = value
		}
	}
	return source, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		writeInvalidJSON(w, r)
		return false
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeInvalidJSON(w, r)
		return false
	}
	return true
}

func writeInvalidJSON(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": domain.ErrorInvalidRequest, "message": "invalid JSON request", "request_id": requestID(r), "retryable": false, "details": map[string]any{}}})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	raw, _ := json.Marshal(input)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}
func sanitizeFilename(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", ""), "\n", ""))
	value = strings.ReplaceAll(value, "\"", "'")
	if value == "" {
		return "artifact.bin"
	}
	return value
}

var requestIDFallback atomic.Uint64

func newRequestID() string {
	return newRequestIDFrom(rand.Reader, time.Now, &requestIDFallback)
}

func newRequestIDFrom(reader io.Reader, now func() time.Time, counter *atomic.Uint64) string {
	var raw [12]byte
	if _, err := io.ReadFull(reader, raw[:]); err == nil {
		return base64.RawURLEncoding.EncodeToString(raw[:])
	}
	fallback := fmt.Sprintf("%d:%d", now().UnixNano(), counter.Add(1))
	digest := sha256.Sum256([]byte(fallback))
	return base64.RawURLEncoding.EncodeToString(digest[:12])
}

func (h *Handler) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, id)))
	})
}

func digestBase64(value string) string {
	raw, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return base64.StdEncoding.EncodeToString([]byte(value))
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func requestID(r *http.Request) string {
	return requestIDFromContext(r.Context())
}
func requestIDFromContext(ctx context.Context) string {
	if value, ok := ctx.Value(requestIDContextKey{}).(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return newRequestID()
}

func errorFrame(requestID string, err error) any {
	code, ok := domain.CodeOf(err)
	if !ok {
		code = domain.ErrorInternal
	}
	return map[string]any{"topic": "oneshot.stream.error", "ts": time.Now().UTC(), "cursor": "", "data": map[string]any{"error": map[string]any{"code": code, "message": err.Error(), "request_id": requestID, "retryable": domain.IsRetryableCode(code), "details": map[string]any{}}}}
}
