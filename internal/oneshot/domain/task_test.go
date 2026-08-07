package domain

import (
	"strings"
	"testing"
	"time"
)

func restoreTaskAtStatus(t *testing.T, status TaskStatus, currentRunID, contextID *string) *Task {
	t.Helper()
	snapshot := mustTask(t).Snapshot()
	snapshot.Status = status
	snapshot.CurrentRunID = cloneOptionalString(currentRunID)
	snapshot.RuntimeContextID = cloneOptionalString(contextID)
	snapshot.Version = 10
	snapshot.UpdatedAt = testNow.Add(time.Minute)
	task, err := RestoreTask(snapshot)
	if err != nil {
		t.Fatalf("RestoreTask(%s): %v", status, err)
	}
	return task
}

func TestTaskLegalTransitions(t *testing.T) {
	t.Run("pending dispatch", func(t *testing.T) {
		task := mustTask(t)
		delivery := mustDelivery(t, task.Snapshot(), DeliveryNew, testNow)
		if err := task.QueueInitialDelivery(delivery.Snapshot(), testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if got := task.Snapshot().Status; got != TaskQueued {
			t.Fatalf("status = %s", got)
		}
	})

	t.Run("pending cancel", func(t *testing.T) {
		task := mustTask(t)
		if err := task.CancelQuiescent(testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("pending reject", func(t *testing.T) {
		task := mustTask(t)
		if err := task.Reject(testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("queued start run", func(t *testing.T) {
		task := restoreTaskAtStatus(t, TaskQueued, nil, nil)
		delivery := mustReservedDelivery(t, task.Snapshot(), DeliveryNew, testNow.Add(2*time.Minute))
		run := mustRun(t, task.Snapshot(), delivery, nil, testNow.Add(2*time.Minute))
		if err := run.Start(); err != nil {
			t.Fatal(err)
		}
		if err := task.StartRun(delivery.Snapshot(), run.Snapshot(), testNow.Add(3*time.Minute)); err != nil {
			t.Fatal(err)
		}
		snapshot := task.Snapshot()
		if snapshot.Status != TaskRunning || snapshot.CurrentRunID == nil || *snapshot.CurrentRunID != run.Snapshot().ID {
			t.Fatalf("unexpected Task snapshot: %+v", snapshot)
		}
	})

	t.Run("queued cancel", func(t *testing.T) {
		task := restoreTaskAtStatus(t, TaskQueued, nil, nil)
		if err := task.CancelQuiescent(testNow.Add(2 * time.Minute)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("queued dispatch failed", func(t *testing.T) {
		task := restoreTaskAtStatus(t, TaskQueued, nil, nil)
		if err := task.DispatchFailed(testNow.Add(2 * time.Minute)); err != nil {
			t.Fatal(err)
		}
	})

	outcomes := []struct {
		name       string
		runStatus  RunStatus
		taskStatus TaskStatus
		apply      func(*Task, RunSnapshot, *RuntimeContextSnapshot, time.Time) error
		needsCtx   bool
	}{
		{"waiting input", RunWaitingInput, TaskWaitingInput, func(task *Task, run RunSnapshot, ctx *RuntimeContextSnapshot, at time.Time) error {
			return task.MarkRunWaitingInput(run, *ctx, at)
		}, true},
		{"completed", RunCompleted, TaskCompleted, (*Task).MarkRunCompleted, false},
		{"failed", RunFailed, TaskFailed, (*Task).MarkRunFailed, false},
		{"cancelled", RunCancelled, TaskCancelled, (*Task).MarkRunCancelled, false},
		{"timed out", RunTimedOut, TaskTimedOut, (*Task).MarkRunTimedOut, false},
	}
	for _, tc := range outcomes {
		t.Run("running to "+tc.name, func(t *testing.T) {
			runID := NewRunID()
			task := restoreTaskAtStatus(t, TaskRunning, &runID, nil)
			finished := testNow.Add(3 * time.Minute)
			errorCode := string(ErrorExecutionFailed)
			run := RunSnapshot{
				ID:         runID,
				TaskID:     task.Snapshot().ID,
				DeliveryID: NewDeliveryID(),
				ProviderID: task.Snapshot().ProviderID,
				Status:     tc.runStatus,
				CreatedAt:  testNow,
				FinishedAt: &finished,
			}
			if tc.runStatus == RunFailed {
				run.ErrorCode = &errorCode
			}
			var context *RuntimeContextSnapshot
			if tc.needsCtx {
				ctx := mustContext(t, testOwner(), "prj_demo", "codex", testNow)
				snapshot := ctx.Snapshot()
				context = &snapshot
				run.RuntimeContextID = &snapshot.ID
			}
			if err := tc.apply(task, run, context, testNow.Add(4*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if got := task.Snapshot().Status; got != tc.taskStatus {
				t.Fatalf("status = %s, want %s", got, tc.taskStatus)
			}
		})
	}

	for _, status := range []TaskStatus{TaskWaitingInput, TaskCompleted, TaskFailed, TaskTimedOut} {
		t.Run(status.String()+" continue", func(t *testing.T) {
			ctx := mustContext(t, testOwner(), "prj_demo", "codex", testNow)
			contextID := ctx.Snapshot().ID
			task := restoreTaskAtStatus(t, status, nil, &contextID)
			delivery := mustDelivery(t, task.Snapshot(), DeliveryContinue, testNow.Add(2*time.Minute))
			if err := task.QueueContinue(delivery.Snapshot(), ctx.Snapshot(), testNow.Add(3*time.Minute)); err != nil {
				t.Fatal(err)
			}
		})
	}

	for _, status := range []TaskStatus{TaskFailed, TaskTimedOut} {
		t.Run(status.String()+" retry", func(t *testing.T) {
			task := restoreTaskAtStatus(t, status, nil, nil)
			delivery := mustDelivery(t, task.Snapshot(), DeliveryRetry, testNow.Add(2*time.Minute))
			if err := task.QueueRetry(delivery.Snapshot(), testNow.Add(3*time.Minute)); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("waiting input cancel", func(t *testing.T) {
		task := restoreTaskAtStatus(t, TaskWaitingInput, nil, nil)
		if err := task.CancelQuiescent(testNow.Add(2 * time.Minute)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTaskRejectsSecondActiveRun(t *testing.T) {
	activeRunID := NewRunID()
	task := restoreTaskAtStatus(t, TaskRunning, &activeRunID, nil)
	delivery := mustReservedDelivery(t, task.Snapshot(), DeliveryNew, testNow.Add(2*time.Minute))
	runSnapshot := RunSnapshot{
		ID:         NewRunID(),
		TaskID:     task.Snapshot().ID,
		DeliveryID: delivery.Snapshot().ID,
		ProviderID: "codex",
		Status:     RunStarting,
		CreatedAt:  testNow.Add(2 * time.Minute),
	}
	if err := delivery.AttachRun(runSnapshot.ID, testNow.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	err := task.StartRun(delivery.Snapshot(), runSnapshot, testNow.Add(3*time.Minute))
	requireCode(t, err, ErrorRunConflict)
}

func TestTaskTerminalCancelledIsIrreversible(t *testing.T) {
	task := restoreTaskAtStatus(t, TaskCancelled, nil, nil)
	delivery := mustDelivery(t, task.Snapshot(), DeliveryRetry, testNow.Add(2*time.Minute))
	err := task.QueueRetry(delivery.Snapshot(), testNow.Add(3*time.Minute))
	requireCode(t, err, ErrorInvalidTransition)
	if task.Snapshot().Status != TaskCancelled {
		t.Fatal("cancelled Task mutated")
	}
}

func TestTaskSourceAndReplyAddressAreImmutableSnapshots(t *testing.T) {
	source := testSource()
	task, err := NewTask(TaskArgs{
		Owner: testOwner(), ProjectID: "prj_demo", ProviderID: "codex",
		Model: "default-model", Source: source, Prompt: "immutable",
	}, testNow)
	if err != nil {
		t.Fatal(err)
	}

	source.Metadata["client"] = "mutated"
	source.ReplyAddress.ConversationID = "other-chat"
	source.ReplyAddress.Metadata["locale"] = "en-US"

	first := task.Snapshot()
	if first.Source.Metadata["client"] != "test" || first.Source.ReplyAddress.ConversationID != "chat-1" || first.Source.ReplyAddress.Metadata["locale"] != "zh-CN" {
		t.Fatalf("constructor retained mutable aliases: %+v", first.Source)
	}
	first.Source.Metadata["client"] = "again"
	first.Source.ReplyAddress.ConversationID = "again"
	second := task.Snapshot()
	if second.Source.Metadata["client"] != "test" || second.Source.ReplyAddress.ConversationID != "chat-1" {
		t.Fatalf("Snapshot exposed aggregate internals: %+v", second.Source)
	}
}

func TestTaskContextOwnerProjectProviderMismatch(t *testing.T) {
	cases := []struct {
		name      string
		owner     Owner
		projectID string
		provider  string
	}{
		{"principal", Owner{Kind: PrincipalAdmin, ID: "other"}, "prj_demo", "codex"},
		{"project", testOwner(), "prj_other", "codex"},
		{"provider", testOwner(), "prj_demo", "claude"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := mustContext(t, tc.owner, tc.projectID, tc.provider, testNow)
			contextID := ctx.Snapshot().ID
			task := restoreTaskAtStatus(t, TaskCompleted, nil, &contextID)
			delivery := mustDelivery(t, task.Snapshot(), DeliveryContinue, testNow.Add(time.Minute))
			err := task.QueueContinue(delivery.Snapshot(), ctx.Snapshot(), testNow.Add(2*time.Minute))
			requireCode(t, err, ErrorContextOwnerMismatch)
		})
	}
}

func TestTaskVersionAndTimeAdvanceOnMutation(t *testing.T) {
	task := mustTask(t)
	before := task.Snapshot()
	delivery := mustDelivery(t, before, DeliveryNew, testNow)
	if err := task.QueueInitialDelivery(delivery.Snapshot(), testNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	after := task.Snapshot()
	if after.Version != before.Version+1 || !after.UpdatedAt.Equal(testNow.Add(time.Second)) {
		t.Fatalf("mutation metadata not advanced: before=%+v after=%+v", before, after)
	}
	if !strings.HasPrefix(after.ID, taskIDPrefix) {
		t.Fatalf("unexpected Task ID %q", after.ID)
	}
}

func TestTaskAuthorizationAndArtifactOwnership(t *testing.T) {
	task := mustTask(t)
	if err := task.Authorize(testOwner(), "prj_demo"); err != nil {
		t.Fatal(err)
	}
	if err := task.Authorize(Owner{Kind: PrincipalAdmin, ID: "other"}, "prj_demo"); !HasCode(err, ErrorForbidden) {
		t.Fatalf("expected forbidden owner mismatch, got %v", err)
	}

	runID := NewRunID()
	finished := testNow.Add(time.Minute)
	run := RunSnapshot{
		ID: runID, TaskID: task.Snapshot().ID, DeliveryID: NewDeliveryID(), ProviderID: "codex",
		Status: RunCompleted, ExitCode: intPtr(0), FinishedAt: &finished, CreatedAt: testNow,
	}
	artifact, err := NewArtifact(ArtifactArgs{
		TaskID: task.Snapshot().ID, RunID: &runID, Kind: ArtifactFinalResult,
		Name: "result.txt", ContentType: "text/plain", SizeBytes: 2,
		SHA256: strings.Repeat("d", 64), StorageKey: "oneshot/result.txt",
		Metadata: map[string]any{}, CreatedAt: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := task.ValidateArtifactOwnership(artifact.Snapshot(), &run); err != nil {
		t.Fatal(err)
	}
	otherTask := mustTask(t)
	if err := otherTask.ValidateArtifactOwnership(artifact.Snapshot(), &run); !HasCode(err, ErrorForbidden) {
		t.Fatalf("expected forbidden cross-Task Artifact, got %v", err)
	}
}

func intPtr(value int) *int { return &value }
