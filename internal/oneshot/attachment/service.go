// Package attachment owns the safe, owner-scoped staging of immutable
// One-shot input files. It is deliberately independent from PTY Sessions.
package attachment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

const (
	DefaultMaxBytes = int64(20 << 20)
	DefaultTTL      = 24 * time.Hour
)

// Repository persists staged attachment metadata with owner filtering.
type Repository interface {
	CreateStagedAttachment(context.Context, domain.StagedAttachmentSnapshot) (domain.StagedAttachmentSnapshot, error)
	GetStagedAttachment(context.Context, domain.Owner, string) (domain.StagedAttachmentSnapshot, error)
	DeleteStagedAttachment(context.Context, domain.Owner, string, time.Time) (domain.StagedAttachmentSnapshot, error)
}

// Storage stores immutable bytes under a server-generated relative key.
type Storage interface {
	Put(context.Context, string, []byte) error
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
}

// Policy constrains file intake before any provider can observe the bytes.
type Policy struct {
	MaxBytes    int64
	TTL         time.Duration
	AllowedMIME map[string]struct{}
}

func DefaultPolicy() Policy {
	allowed := []string{
		"text/plain", "text/markdown", "text/csv", "application/json",
		"application/pdf", "image/png", "image/jpeg", "image/gif", "image/webp",
	}
	out := Policy{MaxBytes: DefaultMaxBytes, TTL: DefaultTTL, AllowedMIME: make(map[string]struct{}, len(allowed))}
	for _, value := range allowed {
		out.AllowedMIME[value] = struct{}{}
	}
	return out
}

// StageRequest is a transport-neutral upload request. SourceRef may contain a
// Telegram file ID or mobile request ID, never a secret URL/token.
type StageRequest struct {
	Owner        domain.Owner
	ProjectID    string
	SourceKind   domain.SourceKind
	SourceRef    string
	Name         string
	DeclaredMIME string
	Reader       io.Reader
}

type Service struct {
	repository Repository
	storage    Storage
	policy     Policy
	now        func() time.Time
}

func NewService(repository Repository, storage Storage, policy Policy) (*Service, error) {
	if repository == nil || storage == nil {
		return nil, domain.InvalidRequestf("attachment repository and storage are required")
	}
	if policy.MaxBytes <= 0 {
		policy.MaxBytes = DefaultMaxBytes
	}
	if policy.TTL <= 0 {
		policy.TTL = DefaultTTL
	}
	if len(policy.AllowedMIME) == 0 {
		policy.AllowedMIME = DefaultPolicy().AllowedMIME
	}
	return &Service{repository: repository, storage: storage, policy: policy, now: func() time.Time { return time.Now().UTC() }}, nil
}

// Stage validates filename, byte count and MIME from the actual content before
// committing immutable bytes and metadata. A metadata failure removes bytes.
func (s *Service) Stage(ctx context.Context, request StageRequest) (domain.StagedAttachmentSnapshot, error) {
	if err := request.Owner.Validate(); err != nil {
		return domain.StagedAttachmentSnapshot{}, err
	}
	projectID := strings.TrimSpace(request.ProjectID)
	if projectID == "" {
		return domain.StagedAttachmentSnapshot{}, domain.InvalidRequestf("project_id is required")
	}
	if !request.SourceKind.Valid() {
		return domain.StagedAttachmentSnapshot{}, domain.InvalidRequestf("invalid attachment source kind %q", request.SourceKind)
	}
	name, err := safeFilename(request.Name)
	if err != nil {
		return domain.StagedAttachmentSnapshot{}, err
	}
	if request.Reader == nil {
		return domain.StagedAttachmentSnapshot{}, domain.InvalidRequestf("attachment content is required")
	}
	data, err := io.ReadAll(io.LimitReader(request.Reader, s.policy.MaxBytes+1))
	if err != nil {
		return domain.StagedAttachmentSnapshot{}, domain.NewDomainError(domain.ErrorArtifactUnavailable, "read attachment content", err)
	}
	if len(data) == 0 {
		return domain.StagedAttachmentSnapshot{}, domain.InvalidRequestf("attachment must not be empty")
	}
	if int64(len(data)) > s.policy.MaxBytes {
		return domain.StagedAttachmentSnapshot{}, domain.InvalidRequestf("attachment exceeds %d byte limit", s.policy.MaxBytes)
	}
	detected := normalizeMIME(http.DetectContentType(data[:min(len(data), 512)]))
	declared := normalizeMIME(request.DeclaredMIME)
	if declared != "" && declared != "application/octet-stream" && !mimeCompatible(declared, detected) {
		return domain.StagedAttachmentSnapshot{}, domain.InvalidRequestf("attachment MIME mismatch: declared %s, detected %s", declared, detected)
	}
	if _, ok := s.policy.AllowedMIME[detected]; !ok {
		return domain.StagedAttachmentSnapshot{}, domain.InvalidRequestf("attachment MIME %s is not allowed", detected)
	}
	now := s.now().UTC()
	id := domain.NewStagedAttachmentID()
	digest := sha256.Sum256(data)
	sha := hex.EncodeToString(digest[:])
	storageKey := filepath.ToSlash(filepath.Join("attachments", id[4:6], id, sha))
	if err := s.storage.Put(ctx, storageKey, data); err != nil {
		return domain.StagedAttachmentSnapshot{}, err
	}
	snapshot := domain.StagedAttachmentSnapshot{
		ID: id, PrincipalKind: request.Owner.Kind, PrincipalID: request.Owner.ID,
		ProjectID: projectID, SourceKind: request.SourceKind, SourceRef: sanitizeSourceRef(request.SourceRef),
		Name: name, DeclaredMIME: declared, DetectedMIME: detected, SizeBytes: int64(len(data)),
		SHA256: sha, StorageKey: storageKey, Status: domain.StagedAttachmentReady,
		CreatedAt: now, ExpiresAt: now.Add(s.policy.TTL),
	}
	if err := snapshot.Validate(); err != nil {
		_ = s.storage.Delete(ctx, storageKey)
		return domain.StagedAttachmentSnapshot{}, err
	}
	persisted, err := s.repository.CreateStagedAttachment(ctx, snapshot)
	if err != nil {
		_ = s.storage.Delete(ctx, storageKey)
		return domain.StagedAttachmentSnapshot{}, err
	}
	if persisted.ID != snapshot.ID {
		_ = s.storage.Delete(ctx, storageKey)
		if persisted.SHA256 != snapshot.SHA256 || persisted.SizeBytes != snapshot.SizeBytes || persisted.Name != snapshot.Name {
			return domain.StagedAttachmentSnapshot{}, domain.NewDomainError(domain.ErrorIdempotencyConflict, "attachment source was reused with different content", nil)
		}
	}
	return persisted, nil
}

func (s *Service) Get(ctx context.Context, owner domain.Owner, id string) (domain.StagedAttachmentSnapshot, error) {
	return s.repository.GetStagedAttachment(ctx, owner, strings.TrimSpace(id))
}

func (s *Service) Delete(ctx context.Context, owner domain.Owner, id string) (domain.StagedAttachmentSnapshot, error) {
	item, err := s.repository.DeleteStagedAttachment(ctx, owner, strings.TrimSpace(id), s.now())
	if err != nil {
		return domain.StagedAttachmentSnapshot{}, err
	}
	if err := s.storage.Delete(ctx, item.StorageKey); err != nil {
		return domain.StagedAttachmentSnapshot{}, err
	}
	return item, nil
}

func safeFilename(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." {
		return "", domain.InvalidRequestf("attachment filename is required")
	}
	if strings.ContainsAny(value, "/\\\x00") || filepath.Base(value) != value {
		return "", domain.InvalidRequestf("attachment filename must not contain a path")
	}
	if len(value) > 255 {
		return "", domain.InvalidRequestf("attachment filename is too long")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", domain.InvalidRequestf("attachment filename contains control characters")
		}
	}
	return value, nil
}

func normalizeMIME(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	parsed, _, err := mime.ParseMediaType(value)
	if err != nil {
		return value
	}
	return parsed
}

func mimeCompatible(declared, detected string) bool {
	if declared == detected {
		return true
	}
	// DetectContentType reports plain UTF-8 JSON/CSV/Markdown as text/plain.
	if detected == "text/plain" {
		return declared == "application/json" || declared == "text/csv" || declared == "text/markdown"
	}
	return false
}

func sanitizeSourceRef(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		value = value[:256]
	}
	// Never persist transport URLs containing bot tokens or signed query data.
	if strings.Contains(value, "://") || strings.ContainsAny(value, "?#") {
		digest := sha256.Sum256([]byte(value))
		return "sha256:" + hex.EncodeToString(digest[:])
	}
	return value
}

// VerifyStored rechecks immutable bytes and is used by hardening/fault tests.
func (s *Service) VerifyStored(ctx context.Context, owner domain.Owner, id string) error {
	item, err := s.Get(ctx, owner, id)
	if err != nil {
		return err
	}
	reader, err := s.storage.Open(ctx, item.StorageKey)
	if err != nil {
		return err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, item.SizeBytes+1))
	if err != nil {
		return domain.NewDomainError(domain.ErrorArtifactUnavailable, "verify attachment content", err)
	}
	if int64(len(data)) != item.SizeBytes {
		return domain.NewDomainError(domain.ErrorArtifactUnavailable, "attachment size verification failed", nil)
	}
	digest := sha256.Sum256(data)
	if !bytes.Equal([]byte(hex.EncodeToString(digest[:])), []byte(item.SHA256)) {
		return domain.NewDomainError(domain.ErrorArtifactUnavailable, "attachment digest verification failed", nil)
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func Summary(item domain.StagedAttachmentSnapshot) string {
	return fmt.Sprintf("%s (%s, %d bytes, sha256:%s)", item.Name, item.DetectedMIME, item.SizeBytes, item.SHA256)
}

// ExpiryRepository is optional so alternative repositories can implement the
// staging path without a background sweeper. PostgreSQL implements it.
type ExpiryRepository interface {
	ListExpiredStagedAttachments(context.Context, time.Time, int) ([]domain.StagedAttachmentSnapshot, error)
	ExpireStagedAttachment(context.Context, domain.Owner, string, time.Time) error
}

// CleanupExpired removes expired bytes before marking metadata expired. File
// deletion is idempotent, so concurrent sweepers are safe.
func (s *Service) CleanupExpired(ctx context.Context, limit int) (int, error) {
	repository, ok := s.repository.(ExpiryRepository)
	if !ok {
		return 0, nil
	}
	if limit <= 0 {
		limit = 100
	}
	items, err := repository.ListExpiredStagedAttachments(ctx, s.now(), limit)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	for _, item := range items {
		if err := s.storage.Delete(ctx, item.StorageKey); err != nil {
			return cleaned, err
		}
		owner := domain.Owner{Kind: item.PrincipalKind, ID: item.PrincipalID}
		if err := repository.ExpireStagedAttachment(ctx, owner, item.ID, s.now()); err != nil {
			return cleaned, err
		}
		cleaned++
	}
	return cleaned, nil
}
