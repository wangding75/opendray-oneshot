package adapter

import (
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

func FuzzDecodeJSONLinesNeverPanicsOrCrossesRunState(f *testing.F) {
	f.Add("{\"type\":\"message\",\"text\":\"hello\"}\n")
	f.Add("not-json\n{\"timestamp\":\"2026-07-28T00:00:00Z\"}\n")
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 1<<20 {
			value = value[:1<<20]
		}
		state := &providerStateStore{}
		text := value
		events, err := decodeJSONLines(state, OutputChunk{RunID: "orun_fuzz", Stream: domain.StreamStdout, Sequence: 1, Text: &text, ReceivedAt: time.Unix(1, 0).UTC()}, "fuzz", func(payload map[string]any, _ *providerRunState) (string, map[string]any) {
			return "fuzz.event", payload
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Type == "" || event.OccurredAt.IsZero() {
				t.Fatalf("invalid event: %+v", event)
			}
		}
		if _, ok := state.runs["other-run"]; ok {
			t.Fatal("parser crossed run state")
		}
	})
}
