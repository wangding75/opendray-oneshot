package domain

import "testing"

func TestTaskTransitionMatrixMatchesFrozenContract(t *testing.T) {
	expected := map[TaskStatus]map[TaskStatus]bool{
		TaskPending:      {TaskQueued: true, TaskCancelled: true, TaskFailed: true},
		TaskQueued:       {TaskRunning: true, TaskCancelled: true, TaskFailed: true},
		TaskRunning:      {TaskWaitingInput: true, TaskCompleted: true, TaskFailed: true, TaskCancelled: true, TaskTimedOut: true},
		TaskWaitingInput: {TaskQueued: true, TaskCancelled: true},
		TaskCompleted:    {TaskQueued: true},
		TaskFailed:       {TaskQueued: true},
		TaskCancelled:    {},
		TaskTimedOut:     {TaskQueued: true},
	}
	assertTransitionMatrix(t, allTaskStatuses, expected, func(from, to TaskStatus) bool {
		return CanTaskTransition(from, to)
	})
}

func TestDeliveryTransitionMatrixMatchesFrozenContract(t *testing.T) {
	expected := map[DeliveryStatus]map[DeliveryStatus]bool{
		DeliveryPending:      {DeliveryReserved: true, DeliveryCancelled: true},
		DeliveryReserved:     {DeliveryAcknowledged: true, DeliveryRetryWait: true, DeliveryDeadLetter: true, DeliveryCancelled: true},
		DeliveryRetryWait:    {DeliveryReserved: true, DeliveryCancelled: true},
		DeliveryAcknowledged: {},
		DeliveryDeadLetter:   {},
		DeliveryCancelled:    {},
	}
	assertTransitionMatrix(t, allDeliveryStatuses, expected, func(from, to DeliveryStatus) bool {
		return CanDeliveryTransition(from, to)
	})
}

func TestRunTransitionMatrixMatchesFrozenContract(t *testing.T) {
	expected := map[RunStatus]map[RunStatus]bool{
		RunCreated:          {RunStarting: true, RunCancelled: true},
		RunStarting:         {RunRunning: true, RunFailed: true, RunCancelled: true, RunTimedOut: true},
		RunRunning:          {RunCollectingOutput: true, RunFailed: true, RunCancelled: true, RunTimedOut: true},
		RunCollectingOutput: {RunWaitingInput: true, RunCompleted: true, RunFailed: true, RunCancelled: true, RunTimedOut: true},
		RunWaitingInput:     {},
		RunCompleted:        {},
		RunFailed:           {},
		RunCancelled:        {},
		RunTimedOut:         {},
	}
	assertTransitionMatrix(t, allRunStatuses, expected, func(from, to RunStatus) bool {
		return CanRunTransition(from, to)
	})
}

func TestRuntimeContextTransitionMatrixMatchesFrozenContract(t *testing.T) {
	expected := map[RuntimeContextStatus]map[RuntimeContextStatus]bool{
		ContextActive:  {ContextBusy: true, ContextInvalid: true, ContextRevoked: true},
		ContextBusy:    {ContextActive: true, ContextInvalid: true, ContextRevoked: true},
		ContextInvalid: {},
		ContextRevoked: {},
	}
	assertTransitionMatrix(t, allRuntimeContextStatuses, expected, func(from, to RuntimeContextStatus) bool {
		return CanRuntimeContextTransition(from, to)
	})
}

func assertTransitionMatrix[S comparable](
	t *testing.T,
	states []S,
	expected map[S]map[S]bool,
	canTransition func(S, S) bool,
) {
	t.Helper()
	for _, from := range states {
		for _, to := range states {
			want := expected[from][to]
			if got := canTransition(from, to); got != want {
				t.Fatalf("transition %v -> %v: got %v, want %v", from, to, got, want)
			}
		}
	}
}
