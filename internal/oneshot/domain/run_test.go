package domain

import (
	"testing"
	"time"
)

func newCreatedRun(t *testing.T) *Run {
	t.Helper()
	task := mustTask(t)
	delivery := mustReservedDelivery(t, task.Snapshot(), DeliveryNew, testNow)
	return mustRun(t, task.Snapshot(), delivery, nil, testNow)
}

func newStartingRun(t *testing.T) *Run {
	t.Helper()
	run := newCreatedRun(t)
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	return run
}

func newRunningRun(t *testing.T) *Run {
	t.Helper()
	run := newStartingRun(t)
	if err := run.ProcessStarted(1234, testNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	return run
}

func newCollectingRun(t *testing.T, exitCode int) *Run {
	t.Helper()
	run := newRunningRun(t)
	if err := run.ProcessExited(exitCode); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestRunLegalTransitions(t *testing.T) {
	t.Run("created start", func(t *testing.T) {
		run := newCreatedRun(t)
		if err := run.Start(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("created cancel", func(t *testing.T) {
		run := newCreatedRun(t)
		if err := run.CancelCreated(testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("starting process started", func(t *testing.T) {
		run := newStartingRun(t)
		if err := run.ProcessStarted(1234, testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("starting failed", func(t *testing.T) {
		run := newStartingRun(t)
		if err := run.StartFailed(ErrorProviderUnavailable, "provider missing", testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("starting cancelled", func(t *testing.T) {
		run := newStartingRun(t)
		if err := run.CancelStarting(true, testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("starting timed out", func(t *testing.T) {
		run := newStartingRun(t)
		if err := run.TimeoutStarting(true, testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("running process exited", func(t *testing.T) {
		run := newRunningRun(t)
		if err := run.ProcessExited(0); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("running supervision failed", func(t *testing.T) {
		run := newRunningRun(t)
		if err := run.SupervisionFailed(ErrorExecutionFailed, "wait failed", true, testNow.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("running cancelled", func(t *testing.T) {
		run := newRunningRun(t)
		if err := run.CancelRunning(true, testNow.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("running timed out", func(t *testing.T) {
		run := newRunningRun(t)
		if err := run.TimeoutRunning(true, testNow.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("collecting waiting input", func(t *testing.T) {
		run := newCollectingRun(t, 0)
		if err := run.FinalizeWaitingInput(true, testNow.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("collecting success", func(t *testing.T) {
		run := newCollectingRun(t, 0)
		if err := run.FinalizeSuccess(true, testNow.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("collecting failure", func(t *testing.T) {
		run := newCollectingRun(t, 1)
		if err := run.FinalizeFailure(ErrorExecutionFailed, "exit 1", true, testNow.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("collecting cancelled", func(t *testing.T) {
		run := newCollectingRun(t, 130)
		if err := run.FinalizeCancel(true, testNow.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("collecting timed out", func(t *testing.T) {
		run := newCollectingRun(t, 124)
		if err := run.FinalizeTimeout(true, testNow.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRunGuardsPreventPrematureTerminalState(t *testing.T) {
	t.Run("cleanup unresolved", func(t *testing.T) {
		run := newRunningRun(t)
		err := run.CancelRunning(false, testNow.Add(2*time.Second))
		requireCode(t, err, ErrorCancelFailed)
		if run.Snapshot().Status != RunRunning {
			t.Fatal("Run mutated despite unresolved cleanup")
		}
	})

	t.Run("output not committed", func(t *testing.T) {
		run := newCollectingRun(t, 0)
		err := run.FinalizeSuccess(false, testNow.Add(2*time.Second))
		requireCode(t, err, ErrorOutputPersistFailed)
		if run.Snapshot().Status != RunCollectingOutput {
			t.Fatal("Run mutated despite uncommitted output")
		}
	})

	t.Run("success requires zero exit", func(t *testing.T) {
		run := newCollectingRun(t, 1)
		if err := run.FinalizeSuccess(true, testNow.Add(2*time.Second)); err == nil {
			t.Fatal("non-zero exit finalized as success")
		}
		if run.Snapshot().Status != RunCollectingOutput {
			t.Fatal("Run mutated after invalid success")
		}
	})
}

func TestRunTerminalStatesAreIrreversibleAndFinished(t *testing.T) {
	constructors := map[RunStatus]func(*testing.T) *Run{
		RunWaitingInput: func(t *testing.T) *Run {
			run := newCollectingRun(t, 0)
			if err := run.FinalizeWaitingInput(true, testNow.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			return run
		},
		RunCompleted: func(t *testing.T) *Run {
			run := newCollectingRun(t, 0)
			if err := run.FinalizeSuccess(true, testNow.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			return run
		},
		RunFailed: func(t *testing.T) *Run {
			run := newCollectingRun(t, 1)
			if err := run.FinalizeFailure(ErrorExecutionFailed, "failed", true, testNow.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			return run
		},
		RunCancelled: func(t *testing.T) *Run {
			run := newRunningRun(t)
			if err := run.CancelRunning(true, testNow.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			return run
		},
		RunTimedOut: func(t *testing.T) *Run {
			run := newRunningRun(t)
			if err := run.TimeoutRunning(true, testNow.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			return run
		},
	}
	for status, constructor := range constructors {
		t.Run(status.String(), func(t *testing.T) {
			run := constructor(t)
			snapshot := run.Snapshot()
			if snapshot.FinishedAt == nil {
				t.Fatal("terminal Run missing finished_at")
			}
			err := run.Start()
			requireCode(t, err, ErrorInvalidTransition)
			if run.Snapshot().Status != status {
				t.Fatal("terminal Run mutated")
			}
		})
	}
}

func TestContinueRunRequiresExactBusyContext(t *testing.T) {
	task := mustTask(t)
	context := mustContext(t, testOwner(), "prj_demo", "codex", testNow)
	version := context.Snapshot().Version
	if err := context.Acquire(testOwner(), "prj_demo", "codex", version, testNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	delivery := mustReservedDelivery(t, task.Snapshot(), DeliveryContinue, testNow.Add(2*time.Second))
	run := mustRun(t, task.Snapshot(), delivery, ptrContext(context.Snapshot()), testNow.Add(2*time.Second))
	if got := run.Snapshot().RuntimeContextID; got == nil || *got != context.Snapshot().ID {
		t.Fatalf("Run missing context: %+v", run.Snapshot())
	}
}

func ptrContext(value RuntimeContextSnapshot) *RuntimeContextSnapshot { return &value }

func TestInvalidFailureTransitionDoesNotMutateRun(t *testing.T) {
	run := newCreatedRun(t)
	err := run.StartFailed(ErrorProviderUnavailable, "missing", testNow.Add(time.Second))
	requireCode(t, err, ErrorInvalidTransition)
	snapshot := run.Snapshot()
	if snapshot.Status != RunCreated || snapshot.ErrorCode != nil || snapshot.ErrorMessage != nil || snapshot.FinishedAt != nil {
		t.Fatalf("invalid transition partially mutated Run: %+v", snapshot)
	}
}
