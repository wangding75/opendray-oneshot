// Package saga contains durable execution-orchestration checkpoints shared by
// the executor, PostgreSQL store, and crash reconciler.
package saga

import (
	"strings"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

// Stage is one explicit step in the Delivery -> Run execution saga.
type Stage string

const (
	StageRunCreated         Stage = "run_created"
	StageCredentialAcquired Stage = "credential_acquired"
	StageCommandBuilt       Stage = "command_built"
	StageProcessStarted     Stage = "process_started"
	StageRunningPersisted   Stage = "running_persisted"
	StageProcessExited      Stage = "process_exited"
	StageOutputCommitted    Stage = "output_committed"
	StageTerminalPersisted  Stage = "terminal_persisted"
	StageCredentialReleased Stage = "credential_released"
	StageAcknowledged       Stage = "acknowledged"
)

var stageOrder = map[Stage]int{
	StageRunCreated:         1,
	StageCredentialAcquired: 2,
	StageCommandBuilt:       3,
	StageProcessStarted:     4,
	StageRunningPersisted:   5,
	StageProcessExited:      6,
	StageOutputCommitted:    7,
	StageTerminalPersisted:  8,
	StageCredentialReleased: 9,
	StageAcknowledged:       10,
}

func (s Stage) Valid() bool              { _, ok := stageOrder[s]; return ok }
func (s Stage) AtLeast(other Stage) bool { return stageOrder[s] >= stageOrder[other] }

// State is the durable crash-recovery checkpoint for one Run.
type State struct {
	RunID               string
	TaskID              string
	DeliveryID          string
	Stage               Stage
	CredentialLeaseID   *string
	PID                 *int
	ExitCode            *int
	ResultErrorCode     *string
	ResultErrorMessage  *string
	ResultCancelled     bool
	ResultTimedOut      bool
	FailureStage        *string
	PrimaryErrorCode    *string
	PrimaryErrorMessage *string
	CompensationError   *string
	UpdatedAt           time.Time
}

func (s State) Validate() error {
	if strings.TrimSpace(s.RunID) == "" || strings.TrimSpace(s.TaskID) == "" || strings.TrimSpace(s.DeliveryID) == "" {
		return domain.InvalidRequestf("saga run_id, task_id, and delivery_id are required")
	}
	if !s.Stage.Valid() {
		return domain.InvalidRequestf("invalid saga stage %q", s.Stage)
	}
	if s.UpdatedAt.IsZero() {
		return domain.InvalidRequestf("saga updated_at is required")
	}
	if s.ResultErrorCode != nil && *s.ResultErrorCode != "" && !domain.IsKnownErrorCode(domain.ErrorCode(*s.ResultErrorCode)) {
		return domain.InvalidRequestf("unknown saga result_error_code %q", *s.ResultErrorCode)
	}
	if s.PrimaryErrorCode != nil && *s.PrimaryErrorCode != "" && !domain.IsKnownErrorCode(domain.ErrorCode(*s.PrimaryErrorCode)) {
		return domain.InvalidRequestf("unknown saga primary_error_code %q", *s.PrimaryErrorCode)
	}
	return nil
}

// RecoveryItem is the complete owner-scoped state needed to reconcile one Run.
type RecoveryItem struct {
	Owner    domain.Owner
	Task     domain.TaskSnapshot
	Delivery domain.DeliverySnapshot
	Run      domain.RunSnapshot
	Saga     State
}
