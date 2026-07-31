package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

func TestTaskLifecycleTopicMatchesFrozenContract(t *testing.T) {
	tests := map[domain.TaskStatus]string{
		domain.TaskPending:      "oneshot.task.created",
		domain.TaskQueued:       "oneshot.task.queued",
		domain.TaskRunning:      "oneshot.task.running",
		domain.TaskWaitingInput: "oneshot.task.waiting_input",
		domain.TaskCompleted:    "oneshot.task.completed",
		domain.TaskFailed:       "oneshot.task.failed",
		domain.TaskCancelled:    "oneshot.task.cancelled",
		domain.TaskTimedOut:     "oneshot.task.timed_out",
	}
	for status, want := range tests {
		got, ok := taskLifecycleTopic(status)
		if !ok || got != want {
			t.Fatalf("status %q mapped to topic %q ok=%v; want %q", status, got, ok, want)
		}
	}
}

func TestRunLifecycleTopicMatchesFrozenContract(t *testing.T) {
	tests := map[domain.RunStatus]string{
		domain.RunRunning:      "oneshot.run.started",
		domain.RunWaitingInput: "oneshot.run.waiting_input",
		domain.RunCompleted:    "oneshot.run.completed",
		domain.RunFailed:       "oneshot.run.failed",
		domain.RunCancelled:    "oneshot.run.cancelled",
		domain.RunTimedOut:     "oneshot.run.timed_out",
	}
	for status, want := range tests {
		got, ok := runLifecycleTopic(status)
		if !ok || got != want {
			t.Fatalf("status %q mapped to topic %q ok=%v; want %q", status, got, ok, want)
		}
	}
	if topic, ok := runLifecycleTopic(domain.RunCollectingOutput); ok || topic != "" {
		t.Fatalf("non-public collecting_output status unexpectedly emitted %q", topic)
	}
}

type lifecycleCaptureRow struct{ err error }

func (r lifecycleCaptureRow) Scan(...any) error { return r.err }

type lifecycleCaptureQueryer struct{ query string }

func (q *lifecycleCaptureQueryer) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	q.query = query
	return lifecycleCaptureRow{err: pgx.ErrNoRows}
}

func TestRunLifecycleSerializesSequenceAndOnlyIgnoresDuplicateTopic(t *testing.T) {
	queryer := &lifecycleCaptureQueryer{}
	snapshot := domain.RunSnapshot{ID: "oru_test", TaskID: "otk_test", Status: domain.RunRunning}
	err := insertRunLifecycle(
		context.Background(), queryer,
		domain.Owner{Kind: domain.PrincipalAdmin, ID: "owner-1"},
		snapshot, "oneshot.run.started", time.Unix(1, 0).UTC(),
	)
	if err == nil || !domain.HasCode(err, domain.ErrorRunNotFound) {
		t.Fatalf("missing Run did not fail closed: %v", err)
	}
	if !strings.Contains(queryer.query, "FROM oneshot_runs") || !strings.Contains(queryer.query, "FOR UPDATE") {
		t.Fatalf("Run row is not locked before lifecycle sequence allocation:\n%s", queryer.query)
	}
	if !strings.Contains(queryer.query, "ON CONFLICT (aggregate_id,topic) WHERE aggregate_kind='run' DO UPDATE") {
		t.Fatalf("duplicate topic conflict is not explicitly scoped:\n%s", queryer.query)
	}
	if !strings.Contains(queryer.query, "SET data=oneshot_lifecycle_events.data") {
		t.Fatalf("duplicate Run lifecycle event does not return the existing row deterministically:\n%s", queryer.query)
	}
	if strings.Contains(queryer.query, "ON CONFLICT DO NOTHING") {
		t.Fatalf("generic conflict swallowing remains in Run lifecycle insert:\n%s", queryer.query)
	}
}
