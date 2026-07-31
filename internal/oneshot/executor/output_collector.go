package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/opendray/opendray-v2/internal/oneshot/adapter"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

const (
	defaultOutputChunkSize     = 32 * 1024
	defaultArtifactCleanupTime = 5 * time.Second
)

// OutputCursor is the durable append position for one Run.
type OutputCursor struct {
	StreamSequence int64
	EventSequence  int64
	StdoutOffset   int64
	StderrOffset   int64
}

// OutputRepository persists immutable output metadata. Store implements this
// interface without exposing PostgreSQL details to the executor.
type OutputRepository interface {
	LoadOutputCursor(context.Context, domain.Owner, string) (int64, int64, int64, int64, error)
	AppendOutput(context.Context, domain.Owner, string, []domain.ArtifactSnapshot, []domain.StreamRecordSnapshot, []domain.StandardEventSnapshot) error
}

// ArtifactStorage owns raw immutable bytes outside PostgreSQL metadata.
type ArtifactStorage interface {
	Put(context.Context, string, []byte) error
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
}

// FileArtifactStorage stores immutable blobs under one server-controlled root.
type FileArtifactStorage struct {
	root string
}

func NewFileArtifactStorage(root string) (*FileArtifactStorage, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || !filepath.IsAbs(root) {
		return nil, domain.InvalidRequestf("artifact storage root must be absolute")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, domain.NewDomainError(domain.ErrorArtifactUnavailable, "create artifact storage root", err)
	}
	return &FileArtifactStorage{root: root}, nil
}

func (s *FileArtifactStorage) resolve(storageKey string) (string, error) {
	if s == nil || s.root == "" {
		return "", domain.NewDomainError(domain.ErrorArtifactUnavailable, "artifact storage is unavailable", nil)
	}
	cleaned := filepath.Clean(strings.TrimSpace(storageKey))
	if cleaned == "." || filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) || cleaned == ".." {
		return "", domain.InvalidRequestf("artifact storage key must be server-controlled and relative")
	}
	resolved := filepath.Join(s.root, cleaned)
	relative, err := filepath.Rel(s.root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", domain.InvalidRequestf("artifact storage key escapes storage root")
	}
	return resolved, nil
}

func (s *FileArtifactStorage) Put(ctx context.Context, storageKey string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.resolve(storageKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return domain.NewDomainError(domain.ErrorArtifactUnavailable, "create artifact directory", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".artifact-*")
	if err != nil {
		return domain.NewDomainError(domain.ErrorArtifactUnavailable, "create artifact temporary file", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return domain.NewDomainError(domain.ErrorArtifactUnavailable, "secure artifact temporary file", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return domain.NewDomainError(domain.ErrorArtifactUnavailable, "write artifact bytes", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return domain.NewDomainError(domain.ErrorArtifactUnavailable, "sync artifact bytes", err)
	}
	if err := temporary.Close(); err != nil {
		return domain.NewDomainError(domain.ErrorArtifactUnavailable, "close artifact file", err)
	}
	if _, err := os.Stat(path); err == nil {
		return domain.NewDomainError(domain.ErrorArtifactUnavailable, "artifact storage key already exists", nil)
	} else if !os.IsNotExist(err) {
		return domain.NewDomainError(domain.ErrorArtifactUnavailable, "inspect artifact destination", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return domain.NewDomainError(domain.ErrorArtifactUnavailable, "commit artifact bytes", err)
	}
	return nil
}

func (s *FileArtifactStorage) Open(ctx context.Context, storageKey string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := s.resolve(storageKey)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, domain.NewDomainError(domain.ErrorArtifactUnavailable, "open artifact bytes", err)
	}
	return file, nil
}

func (s *FileArtifactStorage) Delete(ctx context.Context, storageKey string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.resolve(storageKey)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return domain.NewDomainError(domain.ErrorArtifactUnavailable, "delete uncommitted artifact bytes", err)
	}
	return nil
}

// OutputCollectorConfig is immutable per-Run output collection state.
type OutputCollectorConfig struct {
	Owner          domain.Owner
	Task           domain.TaskSnapshot
	Run            domain.RunSnapshot
	Repository     OutputRepository
	Storage        ArtifactStorage
	Adapter        adapter.OneShotAdapter
	ChunkSize      int
	Now            func() time.Time
	CleanupTimeout time.Duration
}

// FinalOutput records the terminal process result in the final manifest.
type FinalOutput struct {
	ExitCode   int
	Succeeded  bool
	FinishedAt time.Time
}

type utf8DecoderState struct {
	carry []byte
}

// OutputCollector serializes stdout/stderr writes through one lock, assigning
// Run-global monotonic sequences in the order writes reach the collector.
type OutputCollector struct {
	mu             sync.Mutex
	owner          domain.Owner
	task           domain.TaskSnapshot
	run            domain.RunSnapshot
	repository     OutputRepository
	storage        ArtifactStorage
	adapter        adapter.OneShotAdapter
	chunkSize      int
	now            func() time.Time
	cleanupTimeout time.Duration
	cursor         OutputCursor
	initial        OutputCursor
	decoders       map[domain.StreamKind]*utf8DecoderState
	closed         bool
	firstErr       error
}

func NewOutputCollector(ctx context.Context, config OutputCollectorConfig) (*OutputCollector, error) {
	if config.Repository == nil || config.Storage == nil || config.Adapter == nil {
		return nil, domain.InvalidRequestf("output repository, artifact storage, and adapter are required")
	}
	if config.Task.ID == "" || config.Run.ID == "" || config.Run.TaskID != config.Task.ID {
		return nil, domain.InvalidRequestf("output collector Task/Run identity is invalid")
	}
	if config.Adapter.ProviderID() != config.Run.ProviderID {
		return nil, domain.InvalidRequestf("output adapter provider does not match Run")
	}
	chunkSize := config.ChunkSize
	if chunkSize <= 0 {
		chunkSize = defaultOutputChunkSize
	}
	if chunkSize > 1024*1024 {
		return nil, domain.InvalidRequestf("output chunk size cannot exceed 1 MiB")
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	cleanupTimeout := config.CleanupTimeout
	if cleanupTimeout <= 0 {
		cleanupTimeout = defaultArtifactCleanupTime
	}
	if cleanupTimeout > time.Minute {
		return nil, domain.InvalidRequestf("artifact cleanup timeout cannot exceed 1 minute")
	}
	streamSequence, eventSequence, stdoutOffset, stderrOffset, err := config.Repository.LoadOutputCursor(ctx, config.Owner, config.Run.ID)
	if err != nil {
		return nil, err
	}
	cursor := OutputCursor{StreamSequence: streamSequence, EventSequence: eventSequence, StdoutOffset: stdoutOffset, StderrOffset: stderrOffset}
	return &OutputCollector{
		owner: config.Owner, task: config.Task, run: config.Run,
		repository: config.Repository, storage: config.Storage, adapter: config.Adapter,
		chunkSize: chunkSize, now: now, cleanupTimeout: cleanupTimeout, cursor: cursor, initial: cursor,
		decoders: map[domain.StreamKind]*utf8DecoderState{
			domain.StreamStdout: {}, domain.StreamStderr: {},
		},
	}, nil
}

func (c *OutputCollector) Writer(stream domain.StreamKind) io.Writer {
	return &streamWriter{collector: c, stream: stream}
}

func (c *OutputCollector) deleteUncommittedArtifact(parent context.Context, storageKey string) {
	base := context.Background()
	if parent != nil {
		base = context.WithoutCancel(parent)
	}
	cleanupCtx, cancel := context.WithTimeout(base, c.cleanupTimeout)
	defer cancel()
	_ = c.storage.Delete(cleanupCtx, storageKey)
}

func (c *OutputCollector) appendChunk(ctx context.Context, stream domain.StreamKind, raw []byte, receivedAt time.Time) error {
	if len(raw) == 0 {
		return nil
	}
	if !stream.Valid() {
		return domain.InvalidRequestf("invalid output stream %q", stream)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return domain.NewDomainError(domain.ErrorOutputPersistFailed, "output collector is closed", nil)
	}
	if c.firstErr != nil {
		return c.firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	sequence := c.cursor.StreamSequence + 1
	offset := c.cursor.StdoutOffset
	if stream == domain.StreamStderr {
		offset = c.cursor.StderrOffset
	}
	if receivedAt.IsZero() {
		receivedAt = c.now().UTC()
	} else {
		receivedAt = receivedAt.UTC()
	}
	payload := append([]byte(nil), raw...)
	digest := sha256.Sum256(payload)
	digestText := hex.EncodeToString(digest[:])
	storageKey := fmt.Sprintf("oneshot/%s/%s/%s/%020d-%s.bin", c.task.ID, c.run.ID, stream, sequence, digestText)
	if err := c.storage.Put(ctx, storageKey, payload); err != nil {
		c.firstErr = err
		return err
	}

	runID := c.run.ID
	artifact, err := domain.NewArtifact(domain.ArtifactArgs{
		TaskID: c.task.ID, RunID: &runID,
		Kind: rawArtifactKind(stream), Name: fmt.Sprintf("%s-%020d.bin", stream, sequence),
		ContentType: "application/octet-stream", SizeBytes: int64(len(payload)), SHA256: digestText,
		StorageKey: storageKey,
		Metadata: map[string]any{
			"stream": stream.String(), "sequence": sequence, "byte_offset": offset,
		},
		CreatedAt: receivedAt,
	})
	if err != nil {
		c.deleteUncommittedArtifact(ctx, storageKey)
		c.firstErr = err
		return err
	}
	recordText, decodeStatus := classifyRawChunk(payload)
	record, err := domain.NewStreamRecord(domain.StreamRecordArgs{
		RunID: c.run.ID, Sequence: sequence, Stream: stream,
		ByteOffset: offset, ByteLength: int64(len(payload)), RawArtifactID: artifact.Snapshot().ID,
		Text: recordText, DecodeStatus: decodeStatus, SHA256: digestText, ReceivedAt: receivedAt,
	})
	if err != nil {
		c.deleteUncommittedArtifact(ctx, storageKey)
		c.firstErr = err
		return err
	}
	eventText := decodeIncremental(c.decoders[stream], payload)
	if decodeStatus == domain.DecodeBinary {
		c.decoders[stream].carry = c.decoders[stream].carry[:0]
		eventText = nil
	}
	chunk := adapter.OutputChunk{
		RunID: c.run.ID, Sequence: sequence, Stream: stream,
		ByteOffset: offset, ByteLength: int64(len(payload)),
		StreamRecordID: record.Snapshot().ID, RawArtifactID: artifact.Snapshot().ID,
		DecodeStatus: decodeStatus, Text: eventText, SHA256: digestText, ReceivedAt: receivedAt,
	}
	normalized, err := c.adapter.NormalizeOutput(ctx, chunk)
	if err != nil {
		c.deleteUncommittedArtifact(ctx, storageKey)
		c.firstErr = err
		return err
	}
	events := make([]domain.StandardEventSnapshot, 0, len(normalized))
	nextEventSequence := c.cursor.EventSequence
	for _, item := range normalized {
		nextEventSequence++
		occurredAt := item.OccurredAt
		if occurredAt.IsZero() {
			occurredAt = receivedAt
		}
		recordID := record.Snapshot().ID
		event, eventErr := domain.NewStandardEvent(domain.StandardEventArgs{
			RunID: c.run.ID, Sequence: nextEventSequence, Type: item.Type,
			SourceStreamRecordID: &recordID, AdapterID: c.adapter.ProviderID(),
			AdapterVersion: c.adapter.AdapterVersion(), Content: item.Content, OccurredAt: occurredAt,
		})
		if eventErr != nil {
			c.deleteUncommittedArtifact(ctx, storageKey)
			c.firstErr = eventErr
			return eventErr
		}
		events = append(events, event.Snapshot())
	}
	if err := c.repository.AppendOutput(ctx, c.owner, c.run.ID,
		[]domain.ArtifactSnapshot{artifact.Snapshot()},
		[]domain.StreamRecordSnapshot{record.Snapshot()}, events); err != nil {
		c.deleteUncommittedArtifact(ctx, storageKey)
		c.firstErr = domain.NewDomainError(domain.ErrorOutputPersistFailed, "persist One-shot output chunk", err)
		return c.firstErr
	}
	c.cursor.StreamSequence = sequence
	c.cursor.EventSequence = nextEventSequence
	if stream == domain.StreamStdout {
		c.cursor.StdoutOffset += int64(len(payload))
	} else {
		c.cursor.StderrOffset += int64(len(payload))
	}
	return nil
}

func rawArtifactKind(stream domain.StreamKind) domain.ArtifactKind {
	if stream == domain.StreamStderr {
		return domain.ArtifactRawStderr
	}
	return domain.ArtifactRawStdout
}

func classifyRawChunk(raw []byte) (*string, domain.DecodeStatus) {
	if utf8.Valid(raw) {
		text := string(raw)
		return &text, domain.DecodeValidUTF8
	}
	if looksBinary(raw) {
		return nil, domain.DecodeBinary
	}
	if hasIncompleteUTF8Suffix(raw) {
		return nil, domain.DecodeLossyUTF8
	}
	text := strings.ToValidUTF8(string(raw), "\uFFFD")
	return &text, domain.DecodeLossyUTF8
}

func looksBinary(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	controls := 0
	for _, value := range raw {
		if value == 0 {
			return true
		}
		if value < 0x20 && value != '\n' && value != '\r' && value != '\t' {
			controls++
		}
	}
	return controls*5 > len(raw)
}

func hasIncompleteUTF8Suffix(raw []byte) bool {
	for index := 0; index < len(raw); {
		_, size := utf8.DecodeRune(raw[index:])
		if size == 1 && !utf8.FullRune(raw[index:]) {
			return true
		}
		index += size
	}
	return false
}

func decodeIncremental(state *utf8DecoderState, raw []byte) *string {
	combined := make([]byte, 0, len(state.carry)+len(raw))
	combined = append(combined, state.carry...)
	combined = append(combined, raw...)
	state.carry = state.carry[:0]
	var builder strings.Builder
	for len(combined) > 0 {
		if !utf8.FullRune(combined) {
			state.carry = append(state.carry, combined...)
			break
		}
		runeValue, size := utf8.DecodeRune(combined)
		if runeValue == utf8.RuneError && size == 1 {
			builder.WriteRune(utf8.RuneError)
			combined = combined[1:]
			continue
		}
		builder.WriteRune(runeValue)
		combined = combined[size:]
	}
	if builder.Len() == 0 {
		return nil
	}
	text := builder.String()
	return &text
}

// Finalize writes a small JSON manifest that can replay every raw StreamRecord
// by Run ID and sequence range without materializing complete output in memory.
func (c *OutputCollector) Finalize(ctx context.Context, output FinalOutput) (domain.ArtifactSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return domain.ArtifactSnapshot{}, domain.NewDomainError(domain.ErrorInvalidTransition, "output collector is already finalized", nil)
	}
	if c.firstErr != nil {
		return domain.ArtifactSnapshot{}, c.firstErr
	}
	finishedAt := output.FinishedAt.UTC()
	if finishedAt.IsZero() {
		finishedAt = c.now().UTC()
	}
	firstStreamSequence := int64(0)
	if c.cursor.StreamSequence > c.initial.StreamSequence {
		firstStreamSequence = c.initial.StreamSequence + 1
	}
	firstEventSequence := int64(0)
	if c.cursor.EventSequence > c.initial.EventSequence {
		firstEventSequence = c.initial.EventSequence + 1
	}
	manifest := map[string]any{
		"run_id":                c.run.ID,
		"task_id":               c.task.ID,
		"stream_record_run_id":  c.run.ID,
		"first_stream_sequence": firstStreamSequence,
		"last_stream_sequence":  c.cursor.StreamSequence,
		"first_event_sequence":  firstEventSequence,
		"last_event_sequence":   c.cursor.EventSequence,
		"stdout_bytes":          c.cursor.StdoutOffset,
		"stderr_bytes":          c.cursor.StderrOffset,
		"exit_code":             output.ExitCode,
		"succeeded":             output.Succeeded,
		"finished_at":           finishedAt.Format(time.RFC3339Nano),
		"replay_events_url":     fmt.Sprintf("/api/v1/oneshot/runs/%s/events", c.run.ID),
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return domain.ArtifactSnapshot{}, domain.NewDomainError(domain.ErrorOutputPersistFailed, "encode final output manifest", err)
	}
	digest := sha256.Sum256(data)
	digestText := hex.EncodeToString(digest[:])
	storageKey := fmt.Sprintf("oneshot/%s/%s/final/%s.json", c.task.ID, c.run.ID, digestText)
	if err := c.storage.Put(ctx, storageKey, data); err != nil {
		return domain.ArtifactSnapshot{}, err
	}
	runID := c.run.ID
	artifact, err := domain.NewArtifact(domain.ArtifactArgs{
		TaskID: c.task.ID, RunID: &runID, Kind: domain.ArtifactFinalResult,
		Name: "final-result.json", ContentType: "application/json", SizeBytes: int64(len(data)),
		SHA256: digestText, StorageKey: storageKey,
		Metadata: map[string]any{
			"stream_record_run_id": c.run.ID,
			"last_stream_sequence": c.cursor.StreamSequence,
			"last_event_sequence":  c.cursor.EventSequence,
		},
		CreatedAt: finishedAt,
	})
	if err != nil {
		c.deleteUncommittedArtifact(ctx, storageKey)
		return domain.ArtifactSnapshot{}, err
	}
	if err := c.repository.AppendOutput(ctx, c.owner, c.run.ID, []domain.ArtifactSnapshot{artifact.Snapshot()}, nil, nil); err != nil {
		c.deleteUncommittedArtifact(ctx, storageKey)
		return domain.ArtifactSnapshot{}, domain.NewDomainError(domain.ErrorOutputPersistFailed, "persist final output artifact", err)
	}
	c.closed = true
	return artifact.Snapshot(), nil
}
