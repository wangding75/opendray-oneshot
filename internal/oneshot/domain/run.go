package domain

import "time"

// RunStatus is the persisted state of one ordinary child-process execution.
type RunStatus string

const (
	RunCreated          RunStatus = "created"
	RunStarting         RunStatus = "starting"
	RunRunning          RunStatus = "running"
	RunCollectingOutput RunStatus = "collecting_output"
	RunWaitingInput     RunStatus = "waiting_input"
	RunCompleted        RunStatus = "completed"
	RunFailed           RunStatus = "failed"
	RunCancelled        RunStatus = "cancelled"
	RunTimedOut         RunStatus = "timed_out"
)

var allRunStatuses = []RunStatus{
	RunCreated, RunStarting, RunRunning, RunCollectingOutput,
	RunWaitingInput, RunCompleted, RunFailed, RunCancelled, RunTimedOut,
}

func (s RunStatus) String() string { return string(s) }

func (s RunStatus) Valid() bool {
	switch s {
	case RunCreated, RunStarting, RunRunning, RunCollectingOutput,
		RunWaitingInput, RunCompleted, RunFailed, RunCancelled, RunTimedOut:
		return true
	default:
		return false
	}
}

func (s RunStatus) Terminal() bool {
	switch s {
	case RunWaitingInput, RunCompleted, RunFailed, RunCancelled, RunTimedOut:
		return true
	default:
		return false
	}
}

var runTransitions = map[RunStatus]map[RunStatus]struct{}{
	RunCreated: {
		RunStarting: {}, RunCancelled: {},
	},
	RunStarting: {
		RunRunning: {}, RunFailed: {}, RunCancelled: {}, RunTimedOut: {},
	},
	RunRunning: {
		RunCollectingOutput: {}, RunFailed: {}, RunCancelled: {}, RunTimedOut: {},
	},
	RunCollectingOutput: {
		RunWaitingInput: {}, RunCompleted: {}, RunFailed: {}, RunCancelled: {}, RunTimedOut: {},
	},
}

// CanRunTransition reports whether a state edge exists in the frozen contract.
func CanRunTransition(from, to RunStatus) bool {
	toSet, ok := runTransitions[from]
	if !ok {
		return false
	}
	_, ok = toSet[to]
	return ok
}

// RunSnapshot is the persistence/API representation of a Run.
type RunSnapshot struct {
	ID               string     `json:"id"`
	TaskID           string     `json:"task_id"`
	DeliveryID       string     `json:"delivery_id"`
	ProviderID       string     `json:"provider_id"`
	Model            string     `json:"model"`
	RuntimeContextID *string    `json:"runtime_context_id,omitempty"`
	Status           RunStatus  `json:"status"`
	PID              *int       `json:"pid,omitempty"`
	ExitCode         *int       `json:"exit_code,omitempty"`
	ErrorCode        *string    `json:"error_code,omitempty"`
	ErrorMessage     *string    `json:"error_message,omitempty"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

// Run is one immutable-identity One-shot execution attempt.
type Run struct {
	id               string
	taskID           string
	deliveryID       string
	providerID       string
	model            string
	runtimeContextID *string
	status           RunStatus
	pid              *int
	exitCode         *int
	errorCode        *string
	errorMessage     *string
	startedAt        *time.Time
	finishedAt       *time.Time
	createdAt        time.Time
}

// NewRun creates a Run for a reserved execution Delivery. Continue requires an acquired context.
func NewRun(task TaskSnapshot, delivery DeliverySnapshot, context *RuntimeContextSnapshot, now time.Time) (*Run, error) {
	normalizedNow, err := normalizeTime(now, "created_at")
	if err != nil {
		return nil, err
	}
	if err := validateTaskSnapshot(task); err != nil {
		return nil, err
	}
	if err := validateDeliverySnapshot(delivery); err != nil {
		return nil, err
	}
	if delivery.TaskID != task.ID {
		return nil, InvalidRequestf("Delivery belongs to another Task")
	}
	if delivery.Status != DeliveryReserved {
		return nil, InvalidRequestf("Run requires a reserved Delivery")
	}
	if delivery.RunID != nil {
		return nil, runConflict("Delivery already owns a Run")
	}
	if task.Model == "" {
		return nil, NewDomainError(ErrorInvalidRequest, "Task configuration is unexecutable: missing model snapshot", nil)
	}
	var contextID *string
	if delivery.Operation == DeliveryContinue {
		if context == nil {
			return nil, InvalidRequestf("continue Run requires RuntimeContext")
		}
		if err := validateContextCompatibility(Owner{Kind: task.PrincipalKind, ID: task.PrincipalID}, task.ProjectID, task.ProviderID, *context); err != nil {
			return nil, err
		}
		if context.Status != ContextBusy {
			return nil, runConflict("continue RuntimeContext must be acquired before Run creation")
		}
		contextID = cloneOptionalString(&context.ID)
	} else if context != nil {
		return nil, InvalidRequestf("only continue Delivery may bind RuntimeContext")
	}
	return &Run{
		id:               NewRunID(),
		taskID:           task.ID,
		deliveryID:       delivery.ID,
		providerID:       task.ProviderID,
		model:            task.Model,
		runtimeContextID: contextID,
		status:           RunCreated,
		createdAt:        normalizedNow,
	}, nil
}

// RestoreRun validates and restores a persisted Run snapshot.
func RestoreRun(snapshot RunSnapshot) (*Run, error) {
	if err := validateRunSnapshot(snapshot); err != nil {
		return nil, err
	}
	return &Run{
		id:               snapshot.ID,
		taskID:           snapshot.TaskID,
		deliveryID:       snapshot.DeliveryID,
		providerID:       snapshot.ProviderID,
		model:            snapshot.Model,
		runtimeContextID: cloneOptionalString(snapshot.RuntimeContextID),
		status:           snapshot.Status,
		pid:              cloneOptionalInt(snapshot.PID),
		exitCode:         cloneOptionalInt(snapshot.ExitCode),
		errorCode:        cloneOptionalString(snapshot.ErrorCode),
		errorMessage:     cloneOptionalString(snapshot.ErrorMessage),
		startedAt:        normalizeOptionalTime(snapshot.StartedAt),
		finishedAt:       normalizeOptionalTime(snapshot.FinishedAt),
		createdAt:        snapshot.CreatedAt.UTC(),
	}, nil
}

func validateRunSnapshot(snapshot RunSnapshot) error {
	if err := validateID(snapshot.ID, runIDPrefix, "run.id"); err != nil {
		return err
	}
	if err := validateID(snapshot.TaskID, taskIDPrefix, "task_id"); err != nil {
		return err
	}
	if err := validateID(snapshot.DeliveryID, deliveryIDPrefix, "delivery_id"); err != nil {
		return err
	}
	if err := requireNonEmpty(snapshot.ProviderID, "provider_id"); err != nil {
		return err
	}
	if snapshot.RuntimeContextID != nil {
		if err := validateID(*snapshot.RuntimeContextID, runtimeContextIDPrefix, "runtime_context_id"); err != nil {
			return err
		}
	}
	if !snapshot.Status.Valid() {
		return InvalidRequestf("invalid run.status %q", snapshot.Status)
	}
	if snapshot.PID != nil && *snapshot.PID <= 0 {
		return InvalidRequestf("pid must be positive")
	}
	if snapshot.ErrorCode != nil && *snapshot.ErrorCode != "" && !IsKnownErrorCode(ErrorCode(*snapshot.ErrorCode)) {
		return InvalidRequestf("unknown run.error_code %q", *snapshot.ErrorCode)
	}
	createdAt, err := normalizeTime(snapshot.CreatedAt, "created_at")
	if err != nil {
		return err
	}
	if snapshot.StartedAt != nil && snapshot.StartedAt.UTC().Before(createdAt) {
		return InvalidRequestf("started_at must not be before created_at")
	}
	if snapshot.Status.Terminal() {
		if snapshot.FinishedAt == nil {
			return InvalidRequestf("terminal Run requires finished_at")
		}
	} else if snapshot.FinishedAt != nil {
		return InvalidRequestf("non-terminal Run cannot have finished_at")
	}
	if snapshot.FinishedAt != nil && snapshot.FinishedAt.UTC().Before(createdAt) {
		return InvalidRequestf("finished_at must not be before created_at")
	}
	if snapshot.Status == RunRunning || snapshot.Status == RunCollectingOutput {
		if snapshot.StartedAt == nil {
			return InvalidRequestf("started Run requires started_at")
		}
	}
	if snapshot.Status == RunFailed && (snapshot.ErrorCode == nil || *snapshot.ErrorCode == "") {
		return InvalidRequestf("failed Run requires error_code")
	}
	return nil
}

// Snapshot returns a defensive persistence/API copy.
func (r *Run) Snapshot() RunSnapshot {
	return RunSnapshot{
		ID:               r.id,
		TaskID:           r.taskID,
		DeliveryID:       r.deliveryID,
		ProviderID:       r.providerID,
		Model:            r.model,
		RuntimeContextID: cloneOptionalString(r.runtimeContextID),
		Status:           r.status,
		PID:              cloneOptionalInt(r.pid),
		ExitCode:         cloneOptionalInt(r.exitCode),
		ErrorCode:        cloneOptionalString(r.errorCode),
		ErrorMessage:     cloneOptionalString(r.errorMessage),
		StartedAt:        normalizeOptionalTime(r.startedAt),
		FinishedAt:       normalizeOptionalTime(r.finishedAt),
		CreatedAt:        r.createdAt,
	}
}

func (r *Run) transition(to RunStatus, command string) error {
	if !CanRunTransition(r.status, to) {
		return invalidTransition("Run", r.status.String(), to.String(), command)
	}
	r.status = to
	return nil
}

func (r *Run) setTerminal(to RunStatus, command string, finishedAt time.Time) error {
	normalizedFinished, err := normalizeTime(finishedAt, "finished_at")
	if err != nil {
		return err
	}
	if normalizedFinished.Before(r.createdAt) {
		return InvalidRequestf("finished_at must not be before created_at")
	}
	if r.startedAt != nil && normalizedFinished.Before(*r.startedAt) {
		return InvalidRequestf("finished_at must not be before started_at")
	}
	if err := r.transition(to, command); err != nil {
		return err
	}
	r.finishedAt = &normalizedFinished
	r.pid = nil
	return nil
}

func (r *Run) setFailure(code ErrorCode, message string) error {
	if !IsKnownErrorCode(code) {
		return InvalidRequestf("unknown error code %q", code)
	}
	if code == ErrorTimeout {
		return InvalidRequestf("timeout must use a timed_out transition")
	}
	if err := requireNonEmpty(message, "error_message"); err != nil {
		return err
	}
	codeString := string(code)
	r.errorCode = &codeString
	r.errorMessage = cloneOptionalString(&message)
	return nil
}

func requireCleanupResolved(cleanupResolved bool) error {
	if !cleanupResolved {
		return &DomainError{Code: ErrorCancelFailed, Message: "process cleanup is not resolved"}
	}
	return nil
}

func requireOutputCommitted(outputCommitted bool) error {
	if !outputCommitted {
		return &DomainError{Code: ErrorOutputPersistFailed, Message: "Run output is not durably committed"}
	}
	return nil
}

// Start performs created -> starting.
func (r *Run) Start() error { return r.transition(RunStarting, "start") }

// CancelCreated performs created -> cancelled before a child starts.
func (r *Run) CancelCreated(finishedAt time.Time) error {
	return r.setTerminal(RunCancelled, "cancel", finishedAt)
}

// ProcessStarted performs starting -> running and records the ordinary child PID.
func (r *Run) ProcessStarted(pid int, startedAt time.Time) error {
	if pid <= 0 {
		return InvalidRequestf("pid must be positive")
	}
	normalizedStarted, err := normalizeTime(startedAt, "started_at")
	if err != nil {
		return err
	}
	if normalizedStarted.Before(r.createdAt) {
		return InvalidRequestf("started_at must not be before created_at")
	}
	if err := r.transition(RunRunning, "process_started"); err != nil {
		return err
	}
	r.pid = &pid
	r.startedAt = &normalizedStarted
	return nil
}

// StartFailed performs starting -> failed after the failure is persisted.
func (r *Run) StartFailed(code ErrorCode, message string, finishedAt time.Time) error {
	if r.status != RunStarting {
		return invalidTransition("Run", r.status.String(), RunFailed.String(), "start_failed")
	}
	if err := r.setFailure(code, message); err != nil {
		return err
	}
	return r.setTerminal(RunFailed, "start_failed", finishedAt)
}

// CancelStarting performs starting -> cancelled after confirming process absence/termination.
func (r *Run) CancelStarting(cleanupResolved bool, finishedAt time.Time) error {
	if err := requireCleanupResolved(cleanupResolved); err != nil {
		return err
	}
	return r.setTerminal(RunCancelled, "cancel", finishedAt)
}

// TimeoutStarting performs starting -> timed_out after cleanup.
func (r *Run) TimeoutStarting(cleanupResolved bool, finishedAt time.Time) error {
	if r.status != RunStarting {
		return invalidTransition("Run", r.status.String(), RunTimedOut.String(), "timeout")
	}
	if err := requireCleanupResolved(cleanupResolved); err != nil {
		return err
	}
	code := string(ErrorTimeout)
	r.errorCode = &code
	return r.setTerminal(RunTimedOut, "timeout", finishedAt)
}

// ProcessExited performs running -> collecting_output while pipes drain.
func (r *Run) ProcessExited(exitCode int) error {
	if err := r.transition(RunCollectingOutput, "process_exited"); err != nil {
		return err
	}
	r.exitCode = &exitCode
	r.pid = nil
	return nil
}

// SupervisionFailed performs running -> failed after cleanup and failure persistence.
func (r *Run) SupervisionFailed(code ErrorCode, message string, cleanupResolved bool, finishedAt time.Time) error {
	if r.status != RunRunning {
		return invalidTransition("Run", r.status.String(), RunFailed.String(), "supervision_failed")
	}
	if err := requireCleanupResolved(cleanupResolved); err != nil {
		return err
	}
	if err := r.setFailure(code, message); err != nil {
		return err
	}
	return r.setTerminal(RunFailed, "supervision_failed", finishedAt)
}

// CancelRunning performs running -> cancelled after process-tree termination.
func (r *Run) CancelRunning(cleanupResolved bool, finishedAt time.Time) error {
	if err := requireCleanupResolved(cleanupResolved); err != nil {
		return err
	}
	return r.setTerminal(RunCancelled, "cancel", finishedAt)
}

// TimeoutRunning performs running -> timed_out after process-tree termination.
func (r *Run) TimeoutRunning(cleanupResolved bool, finishedAt time.Time) error {
	if r.status != RunRunning {
		return invalidTransition("Run", r.status.String(), RunTimedOut.String(), "timeout")
	}
	if err := requireCleanupResolved(cleanupResolved); err != nil {
		return err
	}
	code := string(ErrorTimeout)
	r.errorCode = &code
	return r.setTerminal(RunTimedOut, "timeout", finishedAt)
}

// FinalizeWaitingInput performs collecting_output -> waiting_input.
func (r *Run) FinalizeWaitingInput(outputCommitted bool, finishedAt time.Time) error {
	if err := requireOutputCommitted(outputCommitted); err != nil {
		return err
	}
	return r.setTerminal(RunWaitingInput, "finalize_waiting_input", finishedAt)
}

// FinalizeSuccess performs collecting_output -> completed.
func (r *Run) FinalizeSuccess(outputCommitted bool, finishedAt time.Time) error {
	if err := requireOutputCommitted(outputCommitted); err != nil {
		return err
	}
	if r.exitCode == nil {
		return InvalidRequestf("finalize_success requires process exit_code")
	}
	if *r.exitCode != 0 {
		return InvalidRequestf("finalize_success requires exit_code 0")
	}
	return r.setTerminal(RunCompleted, "finalize_success", finishedAt)
}

// FinalizeFailure performs collecting_output -> failed.
func (r *Run) FinalizeFailure(code ErrorCode, message string, outputCommitted bool, finishedAt time.Time) error {
	if r.status != RunCollectingOutput {
		return invalidTransition("Run", r.status.String(), RunFailed.String(), "finalize_failure")
	}
	if err := requireOutputCommitted(outputCommitted); err != nil {
		return err
	}
	if err := r.setFailure(code, message); err != nil {
		return err
	}
	return r.setTerminal(RunFailed, "finalize_failure", finishedAt)
}

// FinalizeCancel performs collecting_output -> cancelled after cancellation output drains.
func (r *Run) FinalizeCancel(outputCommitted bool, finishedAt time.Time) error {
	if err := requireOutputCommitted(outputCommitted); err != nil {
		return err
	}
	return r.setTerminal(RunCancelled, "finalize_cancel", finishedAt)
}

// FinalizeTimeout performs collecting_output -> timed_out after timeout output drains.
func (r *Run) FinalizeTimeout(outputCommitted bool, finishedAt time.Time) error {
	if r.status != RunCollectingOutput {
		return invalidTransition("Run", r.status.String(), RunTimedOut.String(), "finalize_timeout")
	}
	if err := requireOutputCommitted(outputCommitted); err != nil {
		return err
	}
	code := string(ErrorTimeout)
	r.errorCode = &code
	return r.setTerminal(RunTimedOut, "finalize_timeout", finishedAt)
}
