package domain

import "time"

// DeliveryOperation describes why an execution Delivery was created.
type DeliveryOperation string

const (
	DeliveryNew      DeliveryOperation = "new"
	DeliveryContinue DeliveryOperation = "continue"
	DeliveryRetry    DeliveryOperation = "retry"
)

func (o DeliveryOperation) String() string { return string(o) }

func (o DeliveryOperation) Valid() bool {
	switch o {
	case DeliveryNew, DeliveryContinue, DeliveryRetry:
		return true
	default:
		return false
	}
}

// DeliveryStatus is the persisted execution queue state.
type DeliveryStatus string

const (
	DeliveryPending      DeliveryStatus = "pending"
	DeliveryReserved     DeliveryStatus = "reserved"
	DeliveryRetryWait    DeliveryStatus = "retry_wait"
	DeliveryAcknowledged DeliveryStatus = "acknowledged"
	DeliveryDeadLetter   DeliveryStatus = "dead_letter"
	DeliveryCancelled    DeliveryStatus = "cancelled"
)

var allDeliveryStatuses = []DeliveryStatus{
	DeliveryPending, DeliveryReserved, DeliveryRetryWait,
	DeliveryAcknowledged, DeliveryDeadLetter, DeliveryCancelled,
}

func (s DeliveryStatus) String() string { return string(s) }

func (s DeliveryStatus) Valid() bool {
	switch s {
	case DeliveryPending, DeliveryReserved, DeliveryRetryWait,
		DeliveryAcknowledged, DeliveryDeadLetter, DeliveryCancelled:
		return true
	default:
		return false
	}
}

func (s DeliveryStatus) Terminal() bool {
	switch s {
	case DeliveryAcknowledged, DeliveryDeadLetter, DeliveryCancelled:
		return true
	default:
		return false
	}
}

var deliveryTransitions = map[DeliveryStatus]map[DeliveryStatus]struct{}{
	DeliveryPending: {
		DeliveryReserved: {}, DeliveryCancelled: {},
	},
	DeliveryRetryWait: {
		DeliveryReserved: {}, DeliveryCancelled: {},
	},
	DeliveryReserved: {
		DeliveryAcknowledged: {}, DeliveryRetryWait: {}, DeliveryDeadLetter: {}, DeliveryCancelled: {},
	},
}

// CanDeliveryTransition reports whether a state edge exists in the frozen contract.
func CanDeliveryTransition(from, to DeliveryStatus) bool {
	toSet, ok := deliveryTransitions[from]
	if !ok {
		return false
	}
	_, ok = toSet[to]
	return ok
}

// DeliveryInput is an immutable snapshot of follow-up input and options.
type DeliveryInput struct {
	PromptDelta    string         `json:"prompt_delta,omitempty"`
	AttachmentRefs []string       `json:"attachment_refs"`
	Options        map[string]any `json:"options"`
}

func cloneDeliveryInput(input DeliveryInput) (DeliveryInput, error) {
	options, err := cloneJSONMap(input.Options, "delivery.input.options")
	if err != nil {
		return DeliveryInput{}, err
	}
	return DeliveryInput{
		PromptDelta:    input.PromptDelta,
		AttachmentRefs: cloneStrings(input.AttachmentRefs),
		Options:        options,
	}, nil
}

func (i DeliveryInput) Validate(operation DeliveryOperation) error {
	for index, ref := range i.AttachmentRefs {
		if err := requireNonEmpty(ref, "delivery.input.attachment_refs"); err != nil {
			return InvalidRequestf("delivery.input.attachment_refs[%d] is required", index)
		}
	}
	if operation == DeliveryContinue && i.PromptDelta == "" && len(i.AttachmentRefs) == 0 {
		return InvalidRequestf("continue Delivery requires prompt_delta or attachment_refs")
	}
	_, err := cloneJSONMap(i.Options, "delivery.input.options")
	return err
}

// DeliveryArgs contains immutable execution Delivery creation data.
type DeliveryArgs struct {
	TaskID         string
	Operation      DeliveryOperation
	RequestedBy    Owner
	Input          DeliveryInput
	IdempotencyKey string
	PayloadSHA256  string
	MaxAttempts    int
	AvailableAt    time.Time
}

// DeliverySnapshot is the persistence representation of a Delivery.
type DeliverySnapshot struct {
	ID              string            `json:"id"`
	TaskID          string            `json:"task_id"`
	Operation       DeliveryOperation `json:"operation"`
	RequestedByKind PrincipalKind     `json:"requested_by_kind"`
	RequestedByID   string            `json:"requested_by_id"`
	Input           DeliveryInput     `json:"input"`
	IdempotencyKey  string            `json:"idempotency_key"`
	PayloadSHA256   string            `json:"payload_sha256"`
	Status          DeliveryStatus    `json:"status"`
	Attempt         int               `json:"attempt"`
	MaxAttempts     int               `json:"max_attempts"`
	AvailableAt     time.Time         `json:"available_at"`
	LeaseOwner      *string           `json:"lease_owner,omitempty"`
	LeaseUntil      *time.Time        `json:"lease_until,omitempty"`
	RunID           *string           `json:"run_id,omitempty"`
	LastErrorCode   *string           `json:"last_error_code,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// Delivery is an execution queue aggregate, separate from channel delivery.
type Delivery struct {
	id             string
	taskID         string
	operation      DeliveryOperation
	requestedBy    Owner
	input          DeliveryInput
	idempotencyKey string
	payloadSHA256  string
	status         DeliveryStatus
	attempt        int
	maxAttempts    int
	availableAt    time.Time
	leaseOwner     *string
	leaseUntil     *time.Time
	runID          *string
	lastErrorCode  *string
	createdAt      time.Time
	updatedAt      time.Time
}

// NewDelivery creates a pending execution Delivery.
func NewDelivery(args DeliveryArgs, now time.Time) (*Delivery, error) {
	normalizedNow, err := normalizeTime(now, "created_at")
	if err != nil {
		return nil, err
	}
	if err := validateID(args.TaskID, taskIDPrefix, "task_id"); err != nil {
		return nil, err
	}
	if !args.Operation.Valid() {
		return nil, InvalidRequestf("invalid delivery.operation %q", args.Operation)
	}
	if err := args.RequestedBy.Validate(); err != nil {
		return nil, err
	}
	if err := args.Input.Validate(args.Operation); err != nil {
		return nil, err
	}
	input, err := cloneDeliveryInput(args.Input)
	if err != nil {
		return nil, err
	}
	if err := requireNonEmpty(args.IdempotencyKey, "idempotency_key"); err != nil {
		return nil, err
	}
	if err := requireSHA256(args.PayloadSHA256, "payload_sha256"); err != nil {
		return nil, err
	}
	if err := requirePositive(args.MaxAttempts, "max_attempts"); err != nil {
		return nil, err
	}
	availableAt := args.AvailableAt
	if availableAt.IsZero() {
		availableAt = normalizedNow
	} else {
		availableAt = availableAt.UTC()
	}
	return &Delivery{
		id:             NewDeliveryID(),
		taskID:         args.TaskID,
		operation:      args.Operation,
		requestedBy:    args.RequestedBy,
		input:          input,
		idempotencyKey: args.IdempotencyKey,
		payloadSHA256:  args.PayloadSHA256,
		status:         DeliveryPending,
		maxAttempts:    args.MaxAttempts,
		availableAt:    availableAt,
		createdAt:      normalizedNow,
		updatedAt:      normalizedNow,
	}, nil
}

// RestoreDelivery validates and restores a persisted Delivery snapshot.
func RestoreDelivery(snapshot DeliverySnapshot) (*Delivery, error) {
	if err := validateDeliverySnapshot(snapshot); err != nil {
		return nil, err
	}
	input, err := cloneDeliveryInput(snapshot.Input)
	if err != nil {
		return nil, err
	}
	return &Delivery{
		id:             snapshot.ID,
		taskID:         snapshot.TaskID,
		operation:      snapshot.Operation,
		requestedBy:    Owner{Kind: snapshot.RequestedByKind, ID: snapshot.RequestedByID},
		input:          input,
		idempotencyKey: snapshot.IdempotencyKey,
		payloadSHA256:  snapshot.PayloadSHA256,
		status:         snapshot.Status,
		attempt:        snapshot.Attempt,
		maxAttempts:    snapshot.MaxAttempts,
		availableAt:    snapshot.AvailableAt.UTC(),
		leaseOwner:     cloneOptionalString(snapshot.LeaseOwner),
		leaseUntil:     normalizeOptionalTime(snapshot.LeaseUntil),
		runID:          cloneOptionalString(snapshot.RunID),
		lastErrorCode:  cloneOptionalString(snapshot.LastErrorCode),
		createdAt:      snapshot.CreatedAt.UTC(),
		updatedAt:      snapshot.UpdatedAt.UTC(),
	}, nil
}

func validateDeliverySnapshot(snapshot DeliverySnapshot) error {
	if err := validateID(snapshot.ID, deliveryIDPrefix, "delivery.id"); err != nil {
		return err
	}
	if err := validateID(snapshot.TaskID, taskIDPrefix, "task_id"); err != nil {
		return err
	}
	if !snapshot.Operation.Valid() {
		return InvalidRequestf("invalid delivery.operation %q", snapshot.Operation)
	}
	if err := (Owner{Kind: snapshot.RequestedByKind, ID: snapshot.RequestedByID}).Validate(); err != nil {
		return err
	}
	if err := snapshot.Input.Validate(snapshot.Operation); err != nil {
		return err
	}
	if err := requireNonEmpty(snapshot.IdempotencyKey, "idempotency_key"); err != nil {
		return err
	}
	if err := requireSHA256(snapshot.PayloadSHA256, "payload_sha256"); err != nil {
		return err
	}
	if !snapshot.Status.Valid() {
		return InvalidRequestf("invalid delivery.status %q", snapshot.Status)
	}
	if snapshot.Attempt < 0 {
		return InvalidRequestf("delivery.attempt must not be negative")
	}
	if err := requirePositive(snapshot.MaxAttempts, "max_attempts"); err != nil {
		return err
	}
	if snapshot.Attempt > snapshot.MaxAttempts {
		return InvalidRequestf("delivery.attempt exceeds max_attempts")
	}
	if _, err := normalizeTime(snapshot.AvailableAt, "available_at"); err != nil {
		return err
	}
	if snapshot.Status == DeliveryReserved {
		if snapshot.LeaseOwner == nil || *snapshot.LeaseOwner == "" || snapshot.LeaseUntil == nil {
			return InvalidRequestf("reserved Delivery requires lease_owner and lease_until")
		}
	}
	if snapshot.Status != DeliveryReserved && (snapshot.LeaseOwner != nil || snapshot.LeaseUntil != nil) {
		return InvalidRequestf("only reserved Delivery may hold a lease")
	}
	if snapshot.RunID != nil {
		if err := validateID(*snapshot.RunID, runIDPrefix, "run_id"); err != nil {
			return err
		}
	}
	if snapshot.LastErrorCode != nil && *snapshot.LastErrorCode != "" && !IsKnownErrorCode(ErrorCode(*snapshot.LastErrorCode)) {
		return InvalidRequestf("unknown last_error_code %q", *snapshot.LastErrorCode)
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
	return nil
}

// Snapshot returns a defensive persistence/API copy.
func (d *Delivery) Snapshot() DeliverySnapshot {
	input, _ := cloneDeliveryInput(d.input)
	return DeliverySnapshot{
		ID:              d.id,
		TaskID:          d.taskID,
		Operation:       d.operation,
		RequestedByKind: d.requestedBy.Kind,
		RequestedByID:   d.requestedBy.ID,
		Input:           input,
		IdempotencyKey:  d.idempotencyKey,
		PayloadSHA256:   d.payloadSHA256,
		Status:          d.status,
		Attempt:         d.attempt,
		MaxAttempts:     d.maxAttempts,
		AvailableAt:     d.availableAt,
		LeaseOwner:      cloneOptionalString(d.leaseOwner),
		LeaseUntil:      normalizeOptionalTime(d.leaseUntil),
		RunID:           cloneOptionalString(d.runID),
		LastErrorCode:   cloneOptionalString(d.lastErrorCode),
		CreatedAt:       d.createdAt,
		UpdatedAt:       d.updatedAt,
	}
}

func (d *Delivery) transition(to DeliveryStatus, command string, now time.Time) error {
	if !CanDeliveryTransition(d.status, to) {
		return invalidTransition("Delivery", d.status.String(), to.String(), command)
	}
	normalizedNow, err := normalizeTime(now, "updated_at")
	if err != nil {
		return err
	}
	if normalizedNow.Before(d.updatedAt) {
		return InvalidRequestf("updated_at must not move backwards")
	}
	d.status = to
	d.updatedAt = normalizedNow
	return nil
}

// Reserve acquires a worker lease and increments the pre-execution attempt.
func (d *Delivery) Reserve(worker string, leaseUntil, now time.Time) error {
	if err := requireNonEmpty(worker, "lease_owner"); err != nil {
		return err
	}
	normalizedNow, err := normalizeTime(now, "updated_at")
	if err != nil {
		return err
	}
	normalizedLease, err := normalizeTime(leaseUntil, "lease_until")
	if err != nil {
		return err
	}
	if normalizedNow.Before(d.availableAt) {
		return &DomainError{Code: ErrorQueueUnavailable, Message: "Delivery is not available for reservation"}
	}
	if !normalizedLease.After(normalizedNow) {
		return InvalidRequestf("lease_until must be in the future")
	}
	if d.attempt >= d.maxAttempts {
		return &DomainError{Code: ErrorDeliveryExhausted, Message: "Delivery attempts exhausted"}
	}
	if err := d.transition(DeliveryReserved, "reserve", normalizedNow); err != nil {
		return err
	}
	d.attempt++
	d.leaseOwner = cloneOptionalString(&worker)
	d.leaseUntil = normalizeOptionalTime(&normalizedLease)
	return nil
}

// AttachRun binds the sole Run created by this Delivery.
func (d *Delivery) AttachRun(runID string, now time.Time) error {
	if d.status != DeliveryReserved {
		return InvalidRequestf("Run can only be attached to a reserved Delivery")
	}
	if err := validateID(runID, runIDPrefix, "run_id"); err != nil {
		return err
	}
	if d.runID != nil {
		if *d.runID == runID {
			return nil
		}
		return runConflict("Delivery already owns another Run")
	}
	normalizedNow, err := normalizeTime(now, "updated_at")
	if err != nil {
		return err
	}
	if normalizedNow.Before(d.updatedAt) {
		return InvalidRequestf("updated_at must not move backwards")
	}
	d.runID = cloneOptionalString(&runID)
	d.updatedAt = normalizedNow
	return nil
}

func (d *Delivery) clearLease() {
	d.leaseOwner = nil
	d.leaseUntil = nil
}

// Acknowledge records that a durable Run outcome is known.
func (d *Delivery) Acknowledge(now time.Time) error {
	if d.runID == nil {
		return InvalidRequestf("acknowledged Delivery requires run_id")
	}
	if err := d.transition(DeliveryAcknowledged, "ack", now); err != nil {
		return err
	}
	d.clearLease()
	return nil
}

// Nack schedules a retryable failure that happened before child process start.
func (d *Delivery) Nack(code ErrorCode, availableAt, now time.Time) error {
	if !IsKnownErrorCode(code) {
		return InvalidRequestf("unknown error code %q", code)
	}
	if !IsRetryableCode(code) {
		return InvalidRequestf("nack requires a retryable error code")
	}
	if d.runID != nil {
		return InvalidRequestf("Delivery with run_id cannot be automatically retried")
	}
	if d.attempt >= d.maxAttempts {
		return &DomainError{Code: ErrorDeliveryExhausted, Message: "Delivery attempts exhausted"}
	}
	normalizedAvailable, err := normalizeTime(availableAt, "available_at")
	if err != nil {
		return err
	}
	if err := d.transition(DeliveryRetryWait, "nack", now); err != nil {
		return err
	}
	d.availableAt = normalizedAvailable
	codeString := string(code)
	d.lastErrorCode = &codeString
	d.clearLease()
	return nil
}

// DeadLetter records a non-retryable or exhausted dispatch failure.
func (d *Delivery) DeadLetter(code ErrorCode, now time.Time) error {
	if !IsKnownErrorCode(code) {
		return InvalidRequestf("unknown error code %q", code)
	}
	if err := d.transition(DeliveryDeadLetter, "dead_letter", now); err != nil {
		return err
	}
	codeString := string(code)
	d.lastErrorCode = &codeString
	d.clearLease()
	return nil
}

// Cancel cancels a pending/retry-wait Delivery or a reserved Delivery after cleanup.
func (d *Delivery) Cancel(cleanupResolved bool, now time.Time) error {
	if d.status == DeliveryReserved && !cleanupResolved {
		return &DomainError{Code: ErrorCancelFailed, Message: "reserved Delivery cleanup is not resolved"}
	}
	if err := d.transition(DeliveryCancelled, "cancel", now); err != nil {
		return err
	}
	d.clearLease()
	return nil
}
