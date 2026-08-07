package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/adapter"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

type outputRepoFixture struct {
	mu        sync.Mutex
	cursor    OutputCursor
	artifacts []domain.ArtifactSnapshot
	records   []domain.StreamRecordSnapshot
	events    []domain.StandardEventSnapshot
	maxBatch  int64
	appendErr error
}

func (r *outputRepoFixture) LoadOutputCursor(_ context.Context, _ domain.Owner, _ string) (int64, int64, int64, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cursor.StreamSequence, r.cursor.EventSequence, r.cursor.StdoutOffset, r.cursor.StderrOffset, nil
}

func (r *outputRepoFixture) AppendOutput(_ context.Context, _ domain.Owner, _ string, artifacts []domain.ArtifactSnapshot, records []domain.StreamRecordSnapshot, events []domain.StandardEventSnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.appendErr != nil {
		return r.appendErr
	}
	r.artifacts = append(r.artifacts, artifacts...)
	r.records = append(r.records, records...)
	r.events = append(r.events, events...)
	for _, record := range records {
		if record.ByteLength > r.maxBatch {
			r.maxBatch = record.ByteLength
		}
		if record.Sequence > r.cursor.StreamSequence {
			r.cursor.StreamSequence = record.Sequence
		}
		next := record.ByteOffset + record.ByteLength
		if record.Stream == domain.StreamStdout && next > r.cursor.StdoutOffset {
			r.cursor.StdoutOffset = next
		}
		if record.Stream == domain.StreamStderr && next > r.cursor.StderrOffset {
			r.cursor.StderrOffset = next
		}
	}
	for _, event := range events {
		if event.Sequence > r.cursor.EventSequence {
			r.cursor.EventSequence = event.Sequence
		}
	}
	return nil
}

func outputIdentity(t *testing.T) (domain.Owner, domain.TaskSnapshot, domain.RunSnapshot) {
	t.Helper()
	now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
	owner := domain.Owner{Kind: domain.PrincipalAdmin, ID: "output-owner"}
	task, err := domain.NewTask(domain.TaskArgs{
		Owner: owner, ProjectID: "output-project", ProviderID: adapter.ShellProviderID,
		Model:  "shell",
		Source: domain.Source{Kind: domain.SourceAPI, ClientRequestID: "output-request"},
		Prompt: "capture output",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := domain.NewDelivery(domain.DeliveryArgs{
		TaskID: task.Snapshot().ID, Operation: domain.DeliveryNew, RequestedBy: owner,
		Input:          domain.DeliveryInput{AttachmentRefs: []string{}, Options: map[string]any{}},
		IdempotencyKey: "output-idempotency", PayloadSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		MaxAttempts: 1, AvailableAt: now,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := delivery.Reserve("output-worker", now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewRun(task.Snapshot(), delivery.Snapshot(), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	return owner, task.Snapshot(), run.Snapshot()
}

func newCollectorFixture(t *testing.T, repo *outputRepoFixture, chunkSize int) (*OutputCollector, *FileArtifactStorage, domain.RunSnapshot) {
	t.Helper()
	owner, task, run := outputIdentity(t)
	storage, err := NewFileArtifactStorage(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	shell := adapter.NewShellAdapter(adapter.ShellConfig{Enabled: true})
	collector, err := NewOutputCollector(context.Background(), OutputCollectorConfig{
		Owner: owner, Task: task, Run: run, Repository: repo, Storage: storage,
		Adapter: shell, ChunkSize: chunkSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	return collector, storage, run
}

func readRawRecords(t *testing.T, storage *FileArtifactStorage, repo *outputRepoFixture) []byte {
	t.Helper()
	var out []byte
	for _, record := range repo.records {
		var artifact domain.ArtifactSnapshot
		for _, candidate := range repo.artifacts {
			if candidate.ID == record.RawArtifactID {
				artifact = candidate
				break
			}
		}
		if artifact.ID == "" {
			t.Fatalf("raw artifact %s not found", record.RawArtifactID)
		}
		reader, err := storage.Open(context.Background(), artifact.StorageKey)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, data...)
	}
	return out
}

func TestOutputCollectorOrdersInterleavedStreamsAndPersistsRawBytes(t *testing.T) {
	repo := &outputRepoFixture{}
	collector, storage, _ := newCollectorFixture(t, repo, 32)
	stdout := collector.Writer(domain.StreamStdout)
	stderr := collector.Writer(domain.StreamStderr)
	for _, step := range []struct {
		writer io.Writer
		data   string
	}{
		{stdout, "out-1"}, {stderr, "err-1"}, {stdout, "out-2"}, {stderr, "err-2"},
	} {
		if _, err := step.writer.Write([]byte(step.data)); err != nil {
			t.Fatal(err)
		}
	}
	if len(repo.records) != 4 || len(repo.events) != 4 {
		t.Fatalf("records/events = %d/%d", len(repo.records), len(repo.events))
	}
	wantStreams := []domain.StreamKind{domain.StreamStdout, domain.StreamStderr, domain.StreamStdout, domain.StreamStderr}
	for i, record := range repo.records {
		if record.Sequence != int64(i+1) || record.Stream != wantStreams[i] {
			t.Fatalf("record[%d] = %+v", i, record)
		}
		if repo.events[i].Sequence != int64(i+1) || repo.events[i].SourceStreamRecordID == nil || *repo.events[i].SourceStreamRecordID != record.ID {
			t.Fatalf("event[%d] = %+v", i, repo.events[i])
		}
	}
	if got := string(readRawRecords(t, storage, repo)); got != "out-1err-1out-2err-2" {
		t.Fatalf("raw bytes = %q", got)
	}
}

func TestOutputCollectorHandlesNoNewlineLargeOutputWithinChunkBound(t *testing.T) {
	repo := &outputRepoFixture{}
	collector, storage, _ := newCollectorFixture(t, repo, 4096)
	payload := bytes.Repeat([]byte("x"), 5*1024*1024+17)
	written, err := collector.Writer(domain.StreamStdout).Write(payload)
	if err != nil || written != len(payload) {
		t.Fatalf("write = %d/%d err=%v", written, len(payload), err)
	}
	if repo.maxBatch > 4096 || len(repo.records) < 2 {
		t.Fatalf("max chunk=%d records=%d", repo.maxBatch, len(repo.records))
	}
	if got := readRawRecords(t, storage, repo); !bytes.Equal(got, payload) {
		t.Fatalf("large raw output mismatch: got=%d want=%d", len(got), len(payload))
	}
}

func TestOutputCollectorDecodesUTF8AcrossChunksWithoutLosingRawBytes(t *testing.T) {
	repo := &outputRepoFixture{}
	collector, storage, _ := newCollectorFixture(t, repo, 32)
	encoded := []byte("你")
	if _, err := collector.Writer(domain.StreamStdout).Write(encoded[:2]); err != nil {
		t.Fatal(err)
	}
	if _, err := collector.Writer(domain.StreamStdout).Write(encoded[2:]); err != nil {
		t.Fatal(err)
	}
	var text string
	for _, event := range repo.events {
		if value, ok := event.Content["text"].(string); ok {
			text += value
		}
	}
	if text != "你" {
		t.Fatalf("decoded text = %q", text)
	}
	if got := readRawRecords(t, storage, repo); !bytes.Equal(got, encoded) {
		t.Fatalf("raw bytes = %v", got)
	}
}

func TestOutputCollectorPreservesInvalidBinaryBytes(t *testing.T) {
	repo := &outputRepoFixture{}
	collector, storage, _ := newCollectorFixture(t, repo, 32)
	payload := []byte{0xff, 0xfe, 0x00, 'A'}
	if _, err := collector.Writer(domain.StreamStderr).Write(payload); err != nil {
		t.Fatal(err)
	}
	if len(repo.records) != 1 || repo.records[0].DecodeStatus != domain.DecodeBinary || repo.records[0].Text != nil {
		t.Fatalf("record = %+v", repo.records)
	}
	if got := readRawRecords(t, storage, repo); !bytes.Equal(got, payload) {
		t.Fatalf("raw bytes = %v", got)
	}
}

func TestOutputCollectorResumesSequencesAndFinalArtifactReferencesRawRecords(t *testing.T) {
	repo := &outputRepoFixture{cursor: OutputCursor{StreamSequence: 5, EventSequence: 7, StdoutOffset: 12, StderrOffset: 3}}
	collector, storage, run := newCollectorFixture(t, repo, 32)
	if _, err := collector.Writer(domain.StreamStdout).Write([]byte("next")); err != nil {
		t.Fatal(err)
	}
	if repo.records[0].Sequence != 6 || repo.records[0].ByteOffset != 12 || repo.events[0].Sequence != 8 {
		t.Fatalf("resumed output = record=%+v event=%+v", repo.records[0], repo.events[0])
	}
	artifact, err := collector.Finalize(context.Background(), FinalOutput{ExitCode: 0, Succeeded: true, FinishedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Kind != domain.ArtifactFinalResult || artifact.RunID == nil || *artifact.RunID != run.ID {
		t.Fatalf("final artifact = %+v", artifact)
	}
	reader, err := storage.Open(context.Background(), artifact.StorageKey)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var manifest map[string]any
	if err := json.NewDecoder(reader).Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["run_id"] != run.ID || manifest["stream_record_run_id"] != run.ID {
		t.Fatalf("manifest = %+v", manifest)
	}
	if int64(manifest["last_stream_sequence"].(float64)) != 6 {
		t.Fatalf("manifest last sequence = %+v", manifest)
	}
}

func TestFileArtifactStorageRejectsTraversalAndOverwrite(t *testing.T) {
	storage, err := NewFileArtifactStorage(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Put(context.Background(), "safe/blob.bin", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := storage.Put(context.Background(), "safe/blob.bin", []byte("second")); !domain.HasCode(err, domain.ErrorArtifactUnavailable) {
		t.Fatalf("overwrite error = %v", err)
	}
	for _, key := range []string{"../escape", filepath.Join(string(filepath.Separator), "absolute")} {
		if err := storage.Put(context.Background(), key, []byte("x")); !domain.HasCode(err, domain.ErrorInvalidRequest) {
			t.Fatalf("key %q error = %v", key, err)
		}
	}
}

func TestOutputCollectorPersistenceFailureStopsFurtherWrites(t *testing.T) {
	repo := &outputRepoFixture{appendErr: errors.New("database unavailable")}
	collector, _, _ := newCollectorFixture(t, repo, 32)
	writer := collector.Writer(domain.StreamStdout)
	if _, err := writer.Write([]byte("first")); !domain.HasCode(err, domain.ErrorOutputPersistFailed) {
		t.Fatalf("first write error = %v", err)
	}
	if _, err := writer.Write([]byte("second")); !domain.HasCode(err, domain.ErrorOutputPersistFailed) {
		t.Fatalf("second write error = %v", err)
	}
	if len(repo.records) != 0 || len(repo.artifacts) != 0 || len(repo.events) != 0 {
		t.Fatalf("failed persistence mutated repository: records=%d artifacts=%d events=%d", len(repo.records), len(repo.artifacts), len(repo.events))
	}
}

type blockingDeleteStorage struct {
	deleteStarted chan struct{}
	deleteDone    chan error
}

func (s *blockingDeleteStorage) Put(context.Context, string, []byte) error { return nil }
func (s *blockingDeleteStorage) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}
func (s *blockingDeleteStorage) Delete(ctx context.Context, _ string) error {
	close(s.deleteStarted)
	<-ctx.Done()
	s.deleteDone <- ctx.Err()
	return ctx.Err()
}

func TestOutputCollectorCleanupHasBoundedTimeout(t *testing.T) {
	owner, task, run := outputIdentity(t)
	storage := &blockingDeleteStorage{deleteStarted: make(chan struct{}), deleteDone: make(chan error, 1)}
	collector, err := NewOutputCollector(context.Background(), OutputCollectorConfig{
		Owner: owner, Task: task, Run: run, Repository: &outputRepoFixture{}, Storage: storage,
		Adapter: adapter.NewShellAdapter(adapter.ShellConfig{Enabled: true}), CleanupTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	collector.deleteUncommittedArtifact(context.Background(), "stale")
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("cleanup exceeded bounded timeout: %v", elapsed)
	}
	select {
	case err := <-storage.deleteDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("delete context error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("delete did not observe cleanup deadline")
	}
}
