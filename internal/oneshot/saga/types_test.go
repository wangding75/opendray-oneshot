package saga

import (
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

func TestStagesAreOrderedAndValid(t *testing.T) {
	stages := []Stage{
		StageRunCreated, StageCredentialAcquired, StageCommandBuilt,
		StageProcessStarted, StageRunningPersisted, StageProcessExited,
		StageOutputCommitted, StageTerminalPersisted, StageCredentialReleased,
		StageAcknowledged,
	}
	for index, stage := range stages {
		if !stage.Valid() {
			t.Fatalf("stage %s is invalid", stage)
		}
		for previous := 0; previous <= index; previous++ {
			if !stage.AtLeast(stages[previous]) {
				t.Fatalf("stage %s is not at least %s", stage, stages[previous])
			}
		}
	}
}

func TestStateValidationRequiresIdentityStageAndKnownErrors(t *testing.T) {
	valid := State{
		RunID: "orn_run", TaskID: "otk_task", DeliveryID: "odl_delivery",
		Stage: StageRunCreated, UpdatedAt: time.Now().UTC(),
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	unknown := "oneshot.unknown"
	valid.PrimaryErrorCode = &unknown
	if err := valid.Validate(); err == nil {
		t.Fatal("unknown primary error code was accepted")
	}
	known := string(domain.ErrorExecutionFailed)
	valid.PrimaryErrorCode = &known
	valid.Stage = Stage("invalid")
	if err := valid.Validate(); err == nil {
		t.Fatal("unknown stage was accepted")
	}
}
