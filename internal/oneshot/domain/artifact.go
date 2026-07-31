package domain

import (
	"path"
	"path/filepath"
	"strings"
	"time"
)

// ArtifactKind is the frozen set of immutable result/evidence kinds.
type ArtifactKind string

const (
	ArtifactRawStdout   ArtifactKind = "raw_stdout"
	ArtifactRawStderr   ArtifactKind = "raw_stderr"
	ArtifactStructured  ArtifactKind = "structured_output"
	ArtifactFinalResult ArtifactKind = "final_result"
	ArtifactFile        ArtifactKind = "file"
	ArtifactLog         ArtifactKind = "log"
	ArtifactAttachment  ArtifactKind = "attachment"
)

func (k ArtifactKind) String() string { return string(k) }
func (k ArtifactKind) Valid() bool {
	switch k {
	case ArtifactRawStdout, ArtifactRawStderr, ArtifactStructured,
		ArtifactFinalResult, ArtifactFile, ArtifactLog, ArtifactAttachment:
		return true
	default:
		return false
	}
}

// ArtifactArgs contains immutable content-addressed artifact metadata.
type ArtifactArgs struct {
	TaskID      string
	RunID       *string
	Kind        ArtifactKind
	Name        string
	ContentType string
	SizeBytes   int64
	SHA256      string
	StorageKey  string
	Metadata    map[string]any
	CreatedAt   time.Time
}

// ArtifactSnapshot is the persistence/API representation of an Artifact.
type ArtifactSnapshot struct {
	ID          string         `json:"id"`
	TaskID      string         `json:"task_id"`
	RunID       *string        `json:"run_id,omitempty"`
	Kind        ArtifactKind   `json:"kind"`
	Name        string         `json:"name"`
	ContentType string         `json:"content_type"`
	SizeBytes   int64          `json:"size_bytes"`
	SHA256      string         `json:"sha256"`
	StorageKey  string         `json:"storage_key"`
	Metadata    map[string]any `json:"metadata"`
	CreatedAt   time.Time      `json:"created_at"`
}

// Artifact is immutable after construction.
type Artifact struct{ snapshot ArtifactSnapshot }

func NewArtifact(args ArtifactArgs) (*Artifact, error) {
	metadata, err := cloneJSONMap(args.Metadata, "artifact.metadata")
	if err != nil {
		return nil, err
	}
	snapshot := ArtifactSnapshot{
		ID:          NewArtifactID(),
		TaskID:      args.TaskID,
		RunID:       cloneOptionalString(args.RunID),
		Kind:        args.Kind,
		Name:        args.Name,
		ContentType: args.ContentType,
		SizeBytes:   args.SizeBytes,
		SHA256:      args.SHA256,
		StorageKey:  args.StorageKey,
		Metadata:    metadata,
		CreatedAt:   args.CreatedAt,
	}
	return RestoreArtifact(snapshot)
}

func RestoreArtifact(snapshot ArtifactSnapshot) (*Artifact, error) {
	if err := validateArtifactSnapshot(snapshot); err != nil {
		return nil, err
	}
	metadata, err := cloneJSONMap(snapshot.Metadata, "artifact.metadata")
	if err != nil {
		return nil, err
	}
	copySnapshot := snapshot
	copySnapshot.RunID = cloneOptionalString(snapshot.RunID)
	copySnapshot.Metadata = metadata
	copySnapshot.CreatedAt = snapshot.CreatedAt.UTC()
	return &Artifact{snapshot: copySnapshot}, nil
}

func validateStorageKey(value string) error {
	if err := requireNonEmpty(value, "storage_key"); err != nil {
		return err
	}
	if filepath.IsAbs(value) || path.IsAbs(value) || filepath.VolumeName(value) != "" || isWindowsAbsolutePath(value) {
		return InvalidRequestf("storage_key must be server-controlled and relative")
	}
	normalized := strings.ReplaceAll(value, "\\", "/")
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return InvalidRequestf("storage_key cannot escape its storage root")
		}
	}
	if path.Clean(normalized) == "." {
		return InvalidRequestf("storage_key is required")
	}
	return nil
}

func validateArtifactSnapshot(snapshot ArtifactSnapshot) error {
	if err := validateID(snapshot.ID, artifactIDPrefix, "artifact.id"); err != nil {
		return err
	}
	if err := validateID(snapshot.TaskID, taskIDPrefix, "task_id"); err != nil {
		return err
	}
	if snapshot.RunID != nil {
		if err := validateID(*snapshot.RunID, runIDPrefix, "run_id"); err != nil {
			return err
		}
	}
	if !snapshot.Kind.Valid() {
		return InvalidRequestf("invalid artifact.kind %q", snapshot.Kind)
	}
	if err := requireNonEmpty(snapshot.Name, "artifact.name"); err != nil {
		return err
	}
	if err := requireNonEmpty(snapshot.ContentType, "content_type"); err != nil {
		return err
	}
	if err := requireNonNegative64(snapshot.SizeBytes, "size_bytes"); err != nil {
		return err
	}
	if err := requireSHA256(snapshot.SHA256, "sha256"); err != nil {
		return err
	}
	if err := validateStorageKey(snapshot.StorageKey); err != nil {
		return err
	}
	if _, err := cloneJSONMap(snapshot.Metadata, "artifact.metadata"); err != nil {
		return err
	}
	_, err := normalizeTime(snapshot.CreatedAt, "created_at")
	return err
}

func (a *Artifact) Snapshot() ArtifactSnapshot {
	out := a.snapshot
	out.RunID = cloneOptionalString(a.snapshot.RunID)
	out.Metadata, _ = cloneJSONMap(a.snapshot.Metadata, "artifact.metadata")
	return out
}
