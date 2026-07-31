package api

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	attachmentservice "github.com/opendray/opendray-v2/internal/oneshot/attachment"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/store"
)

func (h *Handler) stageAttachment(w http.ResponseWriter, r *http.Request) {
	principal, owner, ok := h.authorize(w, r, scopeTaskCreate, actionAttachmentStage, "attachment", "")
	if !ok {
		return
	}
	if !h.requireEnabled(w, r) {
		return
	}
	if h.attachments == nil {
		h.writeError(w, r, domain.NewDomainError(domain.ErrorDisabled, "One-shot attachment staging is unavailable", nil))
		return
	}
	maxRequestBytes := h.attachmentMaxBytes + (1 << 20)
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	if err := r.ParseMultipartForm(maxRequestBytes); err != nil {
		h.writeError(w, r, domain.InvalidRequestf("invalid or oversized multipart attachment"))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		h.writeError(w, r, domain.InvalidRequestf("multipart file field is required"))
		return
	}
	defer file.Close()
	projectID := strings.TrimSpace(r.FormValue("project_id"))
	sourceKind := domain.SourceKind(strings.TrimSpace(r.FormValue("source_kind")))
	if sourceKind == "" {
		sourceKind = domain.SourceAPI
	}
	snapshot, err := h.attachments.Stage(r.Context(), attachmentservice.StageRequest{
		Owner: owner, ProjectID: projectID, SourceKind: sourceKind,
		SourceRef: strings.TrimSpace(r.FormValue("source_ref")), Name: header.Filename,
		DeclaredMIME: header.Header.Get("Content-Type"), Reader: io.LimitReader(file, h.attachmentMaxBytes+1),
	})
	if err != nil {
		h.auditFailure(r.Context(), principal, actionAttachmentStage, "attachment", "", projectID, "", err)
		h.writeError(w, r, err)
		return
	}
	h.auditSuccess(r.Context(), principal, actionAttachmentStage, "attachment", snapshot.ID, snapshot.ProjectID, "", map[string]any{
		"source": snapshot.SourceKind, "detected_mime": snapshot.DetectedMIME, "size_bytes": snapshot.SizeBytes,
	})
	writeJSON(w, http.StatusCreated, map[string]any{"attachment": snapshot})
}

func (h *Handler) getStagedAttachment(w http.ResponseWriter, r *http.Request) {
	principal, owner, ok := h.authorize(w, r, scopeArtifactRead, actionAttachmentRead, "attachment", chi.URLParam(r, "attachment_id"))
	if !ok {
		return
	}
	if h.attachments == nil {
		h.writeError(w, r, domain.NewDomainError(domain.ErrorDisabled, "One-shot attachment staging is unavailable", nil))
		return
	}
	item, err := h.attachments.Get(r.Context(), owner, chi.URLParam(r, "attachment_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if !h.projectAllowed(w, r, principal, actionAttachmentRead, "attachment", item.ID, item.ProjectID) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attachment": item})
}

func (h *Handler) deleteStagedAttachment(w http.ResponseWriter, r *http.Request) {
	principal, owner, ok := h.authorize(w, r, scopeTaskCreate, actionAttachmentDelete, "attachment", chi.URLParam(r, "attachment_id"))
	if !ok {
		return
	}
	if h.attachments == nil {
		h.writeError(w, r, domain.NewDomainError(domain.ErrorDisabled, "One-shot attachment staging is unavailable", nil))
		return
	}
	item, err := h.attachments.Get(r.Context(), owner, chi.URLParam(r, "attachment_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if !h.projectAllowed(w, r, principal, actionAttachmentDelete, "attachment", item.ID, item.ProjectID) {
		return
	}
	item, err = h.attachments.Delete(r.Context(), owner, item.ID)
	if err != nil {
		h.auditFailure(r.Context(), principal, actionAttachmentDelete, "attachment", item.ID, item.ProjectID, "", err)
		h.writeError(w, r, err)
		return
	}
	h.auditSuccess(r.Context(), principal, actionAttachmentDelete, "attachment", item.ID, item.ProjectID, "", nil)
	w.WriteHeader(http.StatusNoContent)
}

func boolOption(options map[string]any, key string) bool {
	value, _ := options[key].(bool)
	return value
}

type notificationPreferenceReader interface {
	GetNotificationPreference(context.Context, domain.Owner, string) (store.NotificationPreference, error)
}
