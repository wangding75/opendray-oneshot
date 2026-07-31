package domain

import (
	"strings"
	"time"
)

// StagedAttachmentStatus tracks immutable input bytes before and after they
// are referenced by a Delivery. Bytes remain content-addressed and never move
// into the interactive Session domain.
type StagedAttachmentStatus string

const (
	StagedAttachmentReady   StagedAttachmentStatus = "ready"
	StagedAttachmentDeleted StagedAttachmentStatus = "deleted"
	StagedAttachmentExpired StagedAttachmentStatus = "expired"
)

func (s StagedAttachmentStatus) Valid() bool {
	switch s {
	case StagedAttachmentReady, StagedAttachmentDeleted, StagedAttachmentExpired:
		return true
	default:
		return false
	}
}

// StagedAttachmentSnapshot is the owner-scoped, immutable metadata returned by
// upload APIs and consumed by Task/Delivery attachment binding.
type StagedAttachmentSnapshot struct {
	ID            string                 `json:"id"`
	PrincipalKind PrincipalKind          `json:"principal_kind"`
	PrincipalID   string                 `json:"principal_id"`
	ProjectID     string                 `json:"project_id"`
	SourceKind    SourceKind             `json:"source_kind"`
	SourceRef     string                 `json:"source_ref,omitempty"`
	Name          string                 `json:"name"`
	DeclaredMIME  string                 `json:"declared_mime,omitempty"`
	DetectedMIME  string                 `json:"detected_mime"`
	SizeBytes     int64                  `json:"size_bytes"`
	SHA256        string                 `json:"sha256"`
	StorageKey    string                 `json:"-"`
	Status        StagedAttachmentStatus `json:"status"`
	CreatedAt     time.Time              `json:"created_at"`
	ExpiresAt     time.Time              `json:"expires_at"`
	DeletedAt     *time.Time             `json:"deleted_at,omitempty"`
}

func (s StagedAttachmentSnapshot) Validate() error {
	if err := ValidateStagedAttachmentID(s.ID); err != nil {
		return err
	}
	if err := (Owner{Kind: s.PrincipalKind, ID: s.PrincipalID}).Validate(); err != nil {
		return err
	}
	if err := requireNonEmpty(s.ProjectID, "staged_attachment.project_id"); err != nil {
		return err
	}
	if !s.SourceKind.Valid() {
		return InvalidRequestf("invalid staged_attachment.source_kind %q", s.SourceKind)
	}
	if err := requireNonEmpty(s.Name, "staged_attachment.name"); err != nil {
		return err
	}
	if strings.ContainsAny(s.Name, "/\\\x00") || s.Name == "." || s.Name == ".." {
		return InvalidRequestf("staged_attachment.name must be a base filename")
	}
	if err := requireNonEmpty(s.DetectedMIME, "staged_attachment.detected_mime"); err != nil {
		return err
	}
	if s.SizeBytes <= 0 {
		return InvalidRequestf("staged_attachment.size_bytes must be positive")
	}
	if err := requireSHA256(s.SHA256, "staged_attachment.sha256"); err != nil {
		return err
	}
	if err := validateStorageKey(s.StorageKey); err != nil {
		return err
	}
	if !s.Status.Valid() {
		return InvalidRequestf("invalid staged_attachment.status %q", s.Status)
	}
	created, err := normalizeTime(s.CreatedAt, "staged_attachment.created_at")
	if err != nil {
		return err
	}
	expires, err := normalizeTime(s.ExpiresAt, "staged_attachment.expires_at")
	if err != nil {
		return err
	}
	if !expires.After(created) {
		return InvalidRequestf("staged_attachment.expires_at must be after created_at")
	}
	return nil
}
