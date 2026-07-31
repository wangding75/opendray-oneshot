package domain

import "time"

// TaskStatus is the persisted Task state.
type TaskStatus string

const (
	TaskPending      TaskStatus = "pending"
	TaskQueued       TaskStatus = "queued"
	TaskRunning      TaskStatus = "running"
	TaskWaitingInput TaskStatus = "waiting_input"
	TaskCompleted    TaskStatus = "completed"
	TaskFailed       TaskStatus = "failed"
	TaskCancelled    TaskStatus = "cancelled"
	TaskTimedOut     TaskStatus = "timed_out"
)

var allTaskStatuses = []TaskStatus{
	TaskPending, TaskQueued, TaskRunning, TaskWaitingInput,
	TaskCompleted, TaskFailed, TaskCancelled, TaskTimedOut,
}

func (s TaskStatus) String() string { return string(s) }

func (s TaskStatus) Valid() bool {
	switch s {
	case TaskPending, TaskQueued, TaskRunning, TaskWaitingInput,
		TaskCompleted, TaskFailed, TaskCancelled, TaskTimedOut:
		return true
	default:
		return false
	}
}

// Terminal reports whether the Task can never transition again.
func (s TaskStatus) Terminal() bool { return s == TaskCancelled }

// Quiescent reports whether the Task has no active child execution.
func (s TaskStatus) Quiescent() bool {
	switch s {
	case TaskPending, TaskQueued, TaskWaitingInput, TaskCompleted, TaskFailed, TaskCancelled, TaskTimedOut:
		return true
	default:
		return false
	}
}

var taskTransitions = map[TaskStatus]map[TaskStatus]struct{}{
	TaskPending: {
		TaskQueued: {}, TaskCancelled: {}, TaskFailed: {},
	},
	TaskQueued: {
		TaskRunning: {}, TaskCancelled: {}, TaskFailed: {},
	},
	TaskRunning: {
		TaskWaitingInput: {}, TaskCompleted: {}, TaskFailed: {}, TaskCancelled: {}, TaskTimedOut: {},
	},
	TaskWaitingInput: {
		TaskQueued: {}, TaskCancelled: {},
	},
	TaskCompleted: {
		TaskQueued: {},
	},
	TaskFailed: {
		TaskQueued: {},
	},
	TaskTimedOut: {
		TaskQueued: {},
	},
}

// CanTaskTransition reports whether a state edge exists in the frozen contract.
func CanTaskTransition(from, to TaskStatus) bool {
	toSet, ok := taskTransitions[from]
	if !ok {
		return false
	}
	_, ok = toSet[to]
	return ok
}

// TaskArgs contains immutable Task creation data.
type TaskArgs struct {
	Owner      Owner
	ProjectID  string
	ProviderID string
	Source     Source
	Prompt     string
}

// TaskSnapshot is the storage/API representation of a Task aggregate.
type TaskSnapshot struct {
	ID               string        `json:"id"`
	PrincipalKind    PrincipalKind `json:"principal_kind"`
	PrincipalID      string        `json:"principal_id"`
	ProjectID        string        `json:"project_id"`
	ProviderID       string        `json:"provider_id"`
	Source           Source        `json:"source"`
	Prompt           string        `json:"prompt"`
	Status           TaskStatus    `json:"status"`
	CurrentRunID     *string       `json:"current_run_id,omitempty"`
	RuntimeContextID *string       `json:"runtime_context_id,omitempty"`
	Version          int64         `json:"version"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

// Task is the user-visible One-shot aggregate. Mutable fields are intentionally private.
type Task struct {
	id               string
	owner            Owner
	projectID        string
	providerID       string
	source           Source
	prompt           string
	status           TaskStatus
	currentRunID     *string
	runtimeContextID *string
	version          int64
	createdAt        time.Time
	updatedAt        time.Time
}

// NewTask creates a pending Task with optimistic version 1.
func NewTask(args TaskArgs, now time.Time) (*Task, error) {
	normalizedNow, err := normalizeTime(now, "created_at")
	if err != nil {
		return nil, err
	}
	if err := args.Owner.Validate(); err != nil {
		return nil, err
	}
	if err := requireNonEmpty(args.ProjectID, "project_id"); err != nil {
		return nil, err
	}
	if err := requireNonEmpty(args.ProviderID, "provider_id"); err != nil {
		return nil, err
	}
	if err := args.Source.Validate(); err != nil {
		return nil, err
	}
	if err := requireNonEmpty(args.Prompt, "prompt"); err != nil {
		return nil, err
	}
	return &Task{
		id:         NewTaskID(),
		owner:      args.Owner,
		projectID:  args.ProjectID,
		providerID: args.ProviderID,
		source:     cloneSource(args.Source),
		prompt:     args.Prompt,
		status:     TaskPending,
		version:    1,
		createdAt:  normalizedNow,
		updatedAt:  normalizedNow,
	}, nil
}

// RestoreTask validates and restores a persisted Task snapshot.
func RestoreTask(snapshot TaskSnapshot) (*Task, error) {
	if err := validateTaskSnapshot(snapshot); err != nil {
		return nil, err
	}
	return &Task{
		id:               snapshot.ID,
		owner:            Owner{Kind: snapshot.PrincipalKind, ID: snapshot.PrincipalID},
		projectID:        snapshot.ProjectID,
		providerID:       snapshot.ProviderID,
		source:           cloneSource(snapshot.Source),
		prompt:           snapshot.Prompt,
		status:           snapshot.Status,
		currentRunID:     cloneOptionalString(snapshot.CurrentRunID),
		runtimeContextID: cloneOptionalString(snapshot.RuntimeContextID),
		version:          snapshot.Version,
		createdAt:        snapshot.CreatedAt.UTC(),
		updatedAt:        snapshot.UpdatedAt.UTC(),
	}, nil
}

func validateTaskSnapshot(snapshot TaskSnapshot) error {
	if err := validateID(snapshot.ID, taskIDPrefix, "task.id"); err != nil {
		return err
	}
	owner := Owner{Kind: snapshot.PrincipalKind, ID: snapshot.PrincipalID}
	if err := owner.Validate(); err != nil {
		return err
	}
	if err := requireNonEmpty(snapshot.ProjectID, "project_id"); err != nil {
		return err
	}
	if err := requireNonEmpty(snapshot.ProviderID, "provider_id"); err != nil {
		return err
	}
	if err := snapshot.Source.Validate(); err != nil {
		return err
	}
	if err := requireNonEmpty(snapshot.Prompt, "prompt"); err != nil {
		return err
	}
	if !snapshot.Status.Valid() {
		return InvalidRequestf("invalid task.status %q", snapshot.Status)
	}
	if snapshot.CurrentRunID != nil {
		if err := validateID(*snapshot.CurrentRunID, runIDPrefix, "current_run_id"); err != nil {
			return err
		}
	}
	if snapshot.RuntimeContextID != nil {
		if err := validateID(*snapshot.RuntimeContextID, runtimeContextIDPrefix, "runtime_context_id"); err != nil {
			return err
		}
	}
	if snapshot.Version < 1 {
		return InvalidRequestf("task.version must be at least 1")
	}
	createdAt, err := normalizeTime(snapshot.CreatedAt, "created_at")
	if err != nil {
		return err
	}
	updatedAt, err := normalizeTime(snapshot.UpdatedAt, "updated_at")
	if err != nil {
		return err
	}
	if updatedAt.Before(createdAt) {
		return InvalidRequestf("updated_at must not be before created_at")
	}
	if snapshot.Status == TaskRunning && snapshot.CurrentRunID == nil {
		return InvalidRequestf("running Task requires current_run_id")
	}
	return nil
}

// Snapshot returns a defensive copy suitable for persistence or API rendering.
func (t *Task) Snapshot() TaskSnapshot {
	return TaskSnapshot{
		ID:               t.id,
		PrincipalKind:    t.owner.Kind,
		PrincipalID:      t.owner.ID,
		ProjectID:        t.projectID,
		ProviderID:       t.providerID,
		Source:           cloneSource(t.source),
		Prompt:           t.prompt,
		Status:           t.status,
		CurrentRunID:     cloneOptionalString(t.currentRunID),
		RuntimeContextID: cloneOptionalString(t.runtimeContextID),
		Version:          t.version,
		CreatedAt:        t.createdAt,
		UpdatedAt:        t.updatedAt,
	}
}

func (t *Task) transition(to TaskStatus, command string, now time.Time) error {
	if !CanTaskTransition(t.status, to) {
		return invalidTransition("Task", t.status.String(), to.String(), command)
	}
	normalizedNow, err := normalizeTime(now, "updated_at")
	if err != nil {
		return err
	}
	if normalizedNow.Before(t.updatedAt) {
		return InvalidRequestf("updated_at must not move backwards")
	}
	t.status = to
	t.version++
	t.updatedAt = normalizedNow
	return nil
}

func (t *Task) validateDelivery(delivery DeliverySnapshot) error {
	if err := validateDeliverySnapshot(delivery); err != nil {
		return err
	}
	if delivery.TaskID != t.id {
		return InvalidRequestf("delivery belongs to another Task")
	}
	if delivery.RequestedByKind != t.owner.Kind || delivery.RequestedByID != t.owner.ID {
		return &DomainError{Code: ErrorForbidden, Message: "delivery requester does not own Task"}
	}
	return nil
}

// QueueInitialDelivery performs pending -> queued for a validated initial Delivery.
func (t *Task) QueueInitialDelivery(delivery DeliverySnapshot, now time.Time) error {
	if err := t.validateDelivery(delivery); err != nil {
		return err
	}
	if delivery.Operation != DeliveryNew {
		return InvalidRequestf("initial Task dispatch requires new Delivery operation")
	}
	if delivery.Status != DeliveryPending {
		return InvalidRequestf("initial Delivery must be pending")
	}
	return t.transition(TaskQueued, "dispatch", now)
}

// Reject performs pending -> failed after durable preparation failure.
func (t *Task) Reject(now time.Time) error {
	return t.transition(TaskFailed, "reject", now)
}

// DispatchFailed performs queued -> failed after non-retryable or exhausted dispatch.
func (t *Task) DispatchFailed(now time.Time) error {
	return t.transition(TaskFailed, "dispatch_failed", now)
}

// CancelQuiescent cancels a Task before or between Runs.
func (t *Task) CancelQuiescent(now time.Time) error {
	switch t.status {
	case TaskPending, TaskQueued, TaskWaitingInput:
		return t.transition(TaskCancelled, "cancel", now)
	default:
		return invalidTransition("Task", t.status.String(), TaskCancelled.String(), "cancel")
	}
}

// StartRun performs queued -> running after Delivery reservation and Run start preparation.
func (t *Task) StartRun(delivery DeliverySnapshot, run RunSnapshot, now time.Time) error {
	if t.status == TaskRunning {
		return runConflict("Task already has an active Run")
	}
	if err := validateRunSnapshot(run); err != nil {
		return err
	}
	if err := t.validateDelivery(delivery); err != nil {
		return err
	}
	if delivery.Status != DeliveryReserved {
		return InvalidRequestf("Delivery must be reserved before Task starts Run")
	}
	if delivery.RunID == nil || *delivery.RunID != run.ID {
		return InvalidRequestf("Delivery run_id must reference the Run")
	}
	if run.TaskID != t.id || run.DeliveryID != delivery.ID {
		return InvalidRequestf("Run does not belong to Task and Delivery")
	}
	if run.ProviderID != t.providerID {
		return InvalidRequestf("Run provider does not match Task provider")
	}
	if run.Status != RunStarting && run.Status != RunRunning {
		return InvalidRequestf("Run must be starting or running before Task starts it")
	}
	if err := t.transition(TaskRunning, "start_run", now); err != nil {
		return err
	}
	t.currentRunID = cloneOptionalString(&run.ID)
	return nil
}

func (t *Task) applyRunOutcome(run RunSnapshot, context *RuntimeContextSnapshot, target TaskStatus, command string, now time.Time) error {
	if err := validateRunSnapshot(run); err != nil {
		return err
	}
	if t.status != TaskRunning {
		return invalidTransition("Task", t.status.String(), target.String(), command)
	}
	if t.currentRunID == nil || *t.currentRunID != run.ID || run.TaskID != t.id {
		return InvalidRequestf("Run is not the Task current Run")
	}
	if run.Status.String() != target.String() {
		return InvalidRequestf("Run outcome %q does not match Task outcome %q", run.Status, target)
	}
	if !run.Status.Terminal() {
		return InvalidRequestf("Run outcome must be terminal")
	}
	if context != nil {
		if err := validateContextCompatibility(t.owner, t.projectID, t.providerID, *context); err != nil {
			return err
		}
		if context.Status != ContextActive {
			return InvalidRequestf("Run outcome RuntimeContext must be active")
		}
		if run.RuntimeContextID != nil && *run.RuntimeContextID != context.ID {
			return InvalidRequestf("Run RuntimeContext does not match outcome RuntimeContext")
		}
	}
	if target == TaskWaitingInput && context == nil {
		return InvalidRequestf("waiting_input Task requires an active RuntimeContext")
	}
	if err := t.transition(target, command, now); err != nil {
		return err
	}
	if context != nil {
		t.runtimeContextID = cloneOptionalString(&context.ID)
	}
	return nil
}

func (t *Task) MarkRunWaitingInput(run RunSnapshot, context RuntimeContextSnapshot, now time.Time) error {
	return t.applyRunOutcome(run, &context, TaskWaitingInput, "run_waiting_input", now)
}

func (t *Task) MarkRunCompleted(run RunSnapshot, context *RuntimeContextSnapshot, now time.Time) error {
	return t.applyRunOutcome(run, context, TaskCompleted, "run_completed", now)
}

func (t *Task) MarkRunFailed(run RunSnapshot, context *RuntimeContextSnapshot, now time.Time) error {
	return t.applyRunOutcome(run, context, TaskFailed, "run_failed", now)
}

func (t *Task) MarkRunCancelled(run RunSnapshot, context *RuntimeContextSnapshot, now time.Time) error {
	return t.applyRunOutcome(run, context, TaskCancelled, "run_cancelled", now)
}

func (t *Task) MarkRunTimedOut(run RunSnapshot, context *RuntimeContextSnapshot, now time.Time) error {
	return t.applyRunOutcome(run, context, TaskTimedOut, "run_timed_out", now)
}

// QueueContinue opens a new cycle from a resumable quiescent outcome.
func (t *Task) QueueContinue(delivery DeliverySnapshot, context RuntimeContextSnapshot, now time.Time) error {
	if err := t.validateDelivery(delivery); err != nil {
		return err
	}
	if delivery.Operation != DeliveryContinue || delivery.Status != DeliveryPending {
		return InvalidRequestf("continue requires a pending continue Delivery")
	}
	if err := validateContextCompatibility(t.owner, t.projectID, t.providerID, context); err != nil {
		return err
	}
	if context.Status != ContextActive {
		return runConflict("RuntimeContext is not active")
	}
	if t.runtimeContextID == nil || *t.runtimeContextID != context.ID {
		return contextOwnerMismatch()
	}
	switch t.status {
	case TaskWaitingInput, TaskCompleted:
		return t.transition(TaskQueued, "continue", now)
	case TaskFailed, TaskTimedOut:
		return t.transition(TaskQueued, "retry_or_continue", now)
	default:
		return invalidTransition("Task", t.status.String(), TaskQueued.String(), "continue")
	}
}

// QueueRetry opens a new execution cycle from failed or timed_out.
func (t *Task) QueueRetry(delivery DeliverySnapshot, now time.Time) error {
	if err := t.validateDelivery(delivery); err != nil {
		return err
	}
	if delivery.Operation != DeliveryRetry || delivery.Status != DeliveryPending {
		return InvalidRequestf("retry requires a pending retry Delivery")
	}
	switch t.status {
	case TaskFailed, TaskTimedOut:
		return t.transition(TaskQueued, "retry_or_continue", now)
	default:
		return invalidTransition("Task", t.status.String(), TaskQueued.String(), "retry")
	}
}

// Authorize verifies exact Task owner and project access. Resource existence
// masking is handled by the API layer; the domain only returns a stable code.
func (t *Task) Authorize(owner Owner, projectID string) error {
	if err := owner.Validate(); err != nil {
		return err
	}
	if !t.owner.Equal(owner) || t.projectID != projectID {
		return &DomainError{Code: ErrorForbidden, Message: "principal does not own Task in project"}
	}
	return nil
}

// ValidateArtifactOwnership ensures Artifact authorization derives through Task ownership.
func (t *Task) ValidateArtifactOwnership(artifact ArtifactSnapshot, run *RunSnapshot) error {
	if err := validateArtifactSnapshot(artifact); err != nil {
		return err
	}
	if artifact.TaskID != t.id {
		return &DomainError{Code: ErrorForbidden, Message: "Artifact belongs to another Task"}
	}
	if artifact.RunID == nil {
		if run != nil {
			return InvalidRequestf("Task-level Artifact must not be paired with a Run")
		}
		return nil
	}
	if run == nil {
		return InvalidRequestf("Run Artifact requires Run ownership evidence")
	}
	if err := validateRunSnapshot(*run); err != nil {
		return err
	}
	if run.ID != *artifact.RunID || run.TaskID != t.id {
		return &DomainError{Code: ErrorForbidden, Message: "Artifact Run does not belong to Task"}
	}
	return nil
}
