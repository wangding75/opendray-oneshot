package domain

import (
	"strings"
	"testing"
)

func TestImmutableResourceConstructors(t *testing.T) {
	runID := NewRunID()
	taskID := NewTaskID()
	artifactID := NewArtifactID()
	text := "hello"

	t.Run("stream record", func(t *testing.T) {
		record, err := NewStreamRecord(StreamRecordArgs{
			RunID: runID, Sequence: 1, Stream: StreamStdout,
			ByteOffset: 0, ByteLength: 5, RawArtifactID: artifactID,
			Text: &text, DecodeStatus: DecodeValidUTF8,
			SHA256: strings.Repeat("a", 64), ReceivedAt: testNow,
		})
		if err != nil {
			t.Fatal(err)
		}
		first := record.Snapshot()
		*first.Text = "changed"
		second := record.Snapshot()
		if second.Text == nil || *second.Text != "hello" {
			t.Fatal("StreamRecord Snapshot exposed internal pointer")
		}
	})

	t.Run("standard event", func(t *testing.T) {
		content := map[string]any{"nested": map[string]any{"text": "original"}}
		event, err := NewStandardEvent(StandardEventArgs{
			RunID: runID, Sequence: 1, Type: "agent.output",
			AdapterID: "codex", AdapterVersion: "1.0.0",
			Content: content, OccurredAt: testNow,
		})
		if err != nil {
			t.Fatal(err)
		}
		content["nested"].(map[string]any)["text"] = "changed"
		first := event.Snapshot()
		if first.Content["nested"].(map[string]any)["text"] != "original" {
			t.Fatal("StandardEvent constructor retained mutable map")
		}
		first.Content["nested"].(map[string]any)["text"] = "changed-again"
		if event.Snapshot().Content["nested"].(map[string]any)["text"] != "original" {
			t.Fatal("StandardEvent Snapshot exposed internal map")
		}
	})

	t.Run("artifact", func(t *testing.T) {
		metadata := map[string]any{"source": map[string]any{"name": "test"}}
		artifact, err := NewArtifact(ArtifactArgs{
			TaskID: taskID, RunID: &runID, Kind: ArtifactFinalResult,
			Name: "result.json", ContentType: "application/json", SizeBytes: 10,
			SHA256: strings.Repeat("b", 64), StorageKey: "oneshot/results/result.json",
			Metadata: metadata, CreatedAt: testNow,
		})
		if err != nil {
			t.Fatal(err)
		}
		metadata["source"].(map[string]any)["name"] = "changed"
		first := artifact.Snapshot()
		if first.Metadata["source"].(map[string]any)["name"] != "test" {
			t.Fatal("Artifact constructor retained mutable map")
		}
		first.Metadata["source"].(map[string]any)["name"] = "changed-again"
		if artifact.Snapshot().Metadata["source"].(map[string]any)["name"] != "test" {
			t.Fatal("Artifact Snapshot exposed internal map")
		}
	})
}

func TestImmutableResourceValidation(t *testing.T) {
	t.Run("binary record rejects text", func(t *testing.T) {
		text := "not binary"
		_, err := NewStreamRecord(StreamRecordArgs{
			RunID: NewRunID(), Sequence: 1, Stream: StreamStdout,
			ByteLength: 1, RawArtifactID: NewArtifactID(), Text: &text,
			DecodeStatus: DecodeBinary, SHA256: strings.Repeat("a", 64), ReceivedAt: testNow,
		})
		if err == nil {
			t.Fatal("binary StreamRecord accepted text")
		}
	})

	t.Run("artifact rejects absolute storage key", func(t *testing.T) {
		_, err := NewArtifact(ArtifactArgs{
			TaskID: NewTaskID(), Kind: ArtifactFile, Name: "secret",
			ContentType: "application/octet-stream", SizeBytes: 1,
			SHA256: strings.Repeat("b", 64), StorageKey: "/etc/passwd",
			Metadata: map[string]any{}, CreatedAt: testNow,
		})
		if err == nil {
			t.Fatal("absolute storage key accepted")
		}
	})

	t.Run("artifact rejects traversal", func(t *testing.T) {
		_, err := NewArtifact(ArtifactArgs{
			TaskID: NewTaskID(), Kind: ArtifactFile, Name: "secret",
			ContentType: "application/octet-stream", SizeBytes: 1,
			SHA256: strings.Repeat("b", 64), StorageKey: "oneshot/../secret",
			Metadata: map[string]any{}, CreatedAt: testNow,
		})
		if err == nil {
			t.Fatal("traversal storage key accepted")
		}
	})
}

func TestGeneratedIDsHaveStablePrefixesAndAreUnique(t *testing.T) {
	generators := []struct {
		name   string
		prefix string
		newID  func() string
	}{
		{"Task", taskIDPrefix, NewTaskID},
		{"Delivery", deliveryIDPrefix, NewDeliveryID},
		{"Run", runIDPrefix, NewRunID},
		{"RuntimeContext", runtimeContextIDPrefix, NewRuntimeContextID},
		{"StreamRecord", streamRecordIDPrefix, NewStreamRecordID},
		{"StandardEvent", standardEventIDPrefix, NewStandardEventID},
		{"Artifact", artifactIDPrefix, NewArtifactID},
	}
	for _, tc := range generators {
		t.Run(tc.name, func(t *testing.T) {
			seen := map[string]struct{}{}
			for i := 0; i < 100; i++ {
				id := tc.newID()
				if !strings.HasPrefix(id, tc.prefix) {
					t.Fatalf("ID %q missing prefix %q", id, tc.prefix)
				}
				if _, exists := seen[id]; exists {
					t.Fatalf("duplicate ID %q", id)
				}
				seen[id] = struct{}{}
			}
		})
	}
}

func TestAppendOnlySequenceValidation(t *testing.T) {
	text := "chunk"
	runID := NewRunID()
	firstRecord, err := NewStreamRecord(StreamRecordArgs{
		RunID: runID, Sequence: 1, Stream: StreamStdout, ByteOffset: 0, ByteLength: 5,
		RawArtifactID: NewArtifactID(), Text: &text, DecodeStatus: DecodeValidUTF8,
		SHA256: strings.Repeat("a", 64), ReceivedAt: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondRecord, err := NewStreamRecord(StreamRecordArgs{
		RunID: runID, Sequence: 2, Stream: StreamStderr, ByteOffset: 0, ByteLength: 5,
		RawArtifactID: NewArtifactID(), Text: &text, DecodeStatus: DecodeValidUTF8,
		SHA256: strings.Repeat("b", 64), ReceivedAt: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateNextStreamRecord(firstRecord.Snapshot(), secondRecord.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNextStreamRecord(secondRecord.Snapshot(), firstRecord.Snapshot()); err == nil {
		t.Fatal("non-increasing StreamRecord sequence accepted")
	}

	firstEvent, err := NewStandardEvent(StandardEventArgs{
		RunID: runID, Sequence: 10, Type: "agent.started", AdapterID: "codex", AdapterVersion: "1",
		Content: map[string]any{}, OccurredAt: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondEvent, err := NewStandardEvent(StandardEventArgs{
		RunID: runID, Sequence: 20, Type: "agent.output", AdapterID: "codex", AdapterVersion: "1",
		Content: map[string]any{}, OccurredAt: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateNextStandardEvent(firstEvent.Snapshot(), secondEvent.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNextStandardEvent(secondEvent.Snapshot(), firstEvent.Snapshot()); err == nil {
		t.Fatal("non-increasing StandardEvent sequence accepted")
	}
}

func TestCrossPlatformPathValidation(t *testing.T) {
	if _, err := NewRuntimeContext(RuntimeContextArgs{
		Owner: testOwner(), ProjectID: "prj_demo", ProviderID: "codex",
		ProviderContextID: "ctx", WorkspacePath: `C:\\workspaces\\demo`,
	}, testNow); err != nil {
		t.Fatalf("Windows workspace path rejected: %v", err)
	}
	_, err := NewArtifact(ArtifactArgs{
		TaskID: NewTaskID(), Kind: ArtifactFile, Name: "secret",
		ContentType: "application/octet-stream", SizeBytes: 1,
		SHA256: strings.Repeat("b", 64), StorageKey: `C:\\secrets\\token`,
		Metadata: map[string]any{}, CreatedAt: testNow,
	})
	if err == nil {
		t.Fatal("Windows absolute storage key accepted")
	}
}
