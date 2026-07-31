package executor

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/adapter"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/queue"
	"github.com/opendray/opendray-v2/internal/oneshot/saga"
)

type sagaCredentialAllocator struct {
	mu       sync.Mutex
	acquired []adapter.CredentialRequest
	released []string
	lease    adapter.CredentialLease
	acquire  error
	release  error
}

func (a *sagaCredentialAllocator) Acquire(_ context.Context, request adapter.CredentialRequest) (adapter.CredentialLease, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.acquired = append(a.acquired, request)
	if a.acquire != nil {
		return adapter.CredentialLease{}, a.acquire
	}
	return a.lease, nil
}

func (a *sagaCredentialAllocator) Release(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.released = append(a.released, id)
	return a.release
}

type failingArtifactStorage struct{ err error }

func (s failingArtifactStorage) Put(context.Context, string, []byte) error { return s.err }
func (s failingArtifactStorage) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, s.err
}
func (s failingArtifactStorage) Delete(context.Context, string) error { return nil }

type failOnceAckQueue struct {
	queue.Repository
	mu       sync.Mutex
	failures int
}

func (q *failOnceAckQueue) Ack(ctx context.Context, deliveryID, workerID string) (domain.DeliverySnapshot, error) {
	q.mu.Lock()
	if q.failures > 0 {
		q.failures--
		q.mu.Unlock()
		return domain.DeliverySnapshot{}, errors.New("injected ACK failure")
	}
	q.mu.Unlock()
	return q.Repository.Ack(ctx, deliveryID, workerID)
}

func sagaClaim(t *testing.T, executionQueue *queue.MemoryQueue, command string) queue.Claim {
	t.Helper()
	enqueueShellTask(t, executionQueue, command)
	claims, err := executionQueue.ClaimDue(context.Background(), "saga-test-worker", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("claims = %d", len(claims))
	}
	return claims[0]
}

func sagaService(t *testing.T, repository *fakeRunRepository, commands map[string]adapter.CommandSpec, credentials *sagaCredentialAllocator, storage ArtifactStorage, options ...RunServiceOption) *RunService {
	t.Helper()
	if storage == nil {
		var err error
		storage, err = NewFileArtifactStorage(filepath.Join(t.TempDir(), "artifacts"))
		if err != nil {
			t.Fatal(err)
		}
	}
	shellAdapter := adapter.NewShellAdapter(adapter.ShellConfig{Enabled: true, Commands: commands})
	registry, err := adapter.NewConfiguredRegistry(true, nil, credentials, shellAdapter)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewRunService(repository, registry, NewProcessExecutor(), append([]RunServiceOption{WithArtifactStorage(storage)}, options...)...)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestRunSagaRunCreationFailureDoesNotStartProcess(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix fixture")
	}
	executionQueue := queue.NewMemoryQueue(nil)
	claim := sagaClaim(t, executionQueue, "success")
	repository := newFakeRunRepository(executionQueue)
	repository.failCreate = domain.NewDomainError(domain.ErrorQueueUnavailable, "injected create failure", nil)
	service := sagaService(t, repository, shellFixtureCommands(t), nil, nil)
	outcome := service.Process(context.Background(), claim)
	if outcome.Action != queue.ActionRetry || outcome.Code != domain.ErrorQueueUnavailable {
		t.Fatalf("outcome = %+v", outcome)
	}
	if len(repository.runs) != 0 || len(repository.sagas) != 0 {
		t.Fatalf("run or saga persisted after create failure: runs=%d sagas=%d", len(repository.runs), len(repository.sagas))
	}
}

func TestRunSagaStartFailureAndOutputFailuresAreAuditable(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix fixture")
	}
	cases := []struct {
		name          string
		command       string
		configureRepo func(*fakeRunRepository)
		storage       ArtifactStorage
		failureStage  saga.Stage
	}{
		{name: "process-start", command: "missing-command", failureStage: saga.StageProcessStarted},
		{name: "output-write", command: "success", configureRepo: func(repo *fakeRunRepository) { repo.failAppend = errors.New("injected output write failure") }, failureStage: saga.StageOutputCommitted},
		{name: "artifact-write", command: "success", storage: failingArtifactStorage{err: errors.New("injected artifact failure")}, failureStage: saga.StageOutputCommitted},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			executionQueue := queue.NewMemoryQueue(nil)
			claim := sagaClaim(t, executionQueue, test.command)
			repository := newFakeRunRepository(executionQueue)
			if test.configureRepo != nil {
				test.configureRepo(repository)
			}
			credentials := &sagaCredentialAllocator{lease: adapter.CredentialLease{ID: "lease-" + test.name}}
			service := sagaService(t, repository, shellFixtureCommands(t), credentials, test.storage)
			outcome := service.Process(context.Background(), claim)
			if outcome.Action != queue.ActionAck && outcome.Action != queue.ActionRecover {
				t.Fatalf("outcome = %+v", outcome)
			}
			_, run := repository.snapshots()
			if run.ID == "" {
				t.Fatal("Run was not persisted")
			}
			state, err := repository.GetSagaState(context.Background(), domain.Owner{Kind: claim.Task.PrincipalKind, ID: claim.Task.PrincipalID}, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state.FailureStage == nil || *state.FailureStage != string(test.failureStage) {
				t.Fatalf("failure stage = %+v, state=%+v", state.FailureStage, state)
			}
			if state.PrimaryErrorCode == nil || state.PrimaryErrorMessage == nil {
				t.Fatalf("primary error was not audited: %+v", state)
			}
			credentials.mu.Lock()
			released := append([]string(nil), credentials.released...)
			credentials.mu.Unlock()
			if outcome.Action == queue.ActionAck && len(released) != 1 {
				t.Fatalf("credential release = %+v", released)
			}
		})
	}
}

func TestRunSagaCrashCheckpointsRemainRecoverable(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix fixture")
	}
	stages := []saga.Stage{
		saga.StageRunCreated,
		saga.StageCredentialAcquired,
		saga.StageCommandBuilt,
		saga.StageProcessStarted,
		saga.StageRunningPersisted,
		saga.StageProcessExited,
		saga.StageOutputCommitted,
		saga.StageTerminalPersisted,
	}
	for _, target := range stages {
		t.Run(string(target), func(t *testing.T) {
			executionQueue := queue.NewMemoryQueue(nil)
			commands := shellFixtureCommands(t)
			commands["crash-fast"] = adapter.CommandSpec{
				Executable: "/bin/sh", Args: []string{"-c", "printf crash-stage; sleep 0.05"}, Dir: t.TempDir(),
			}
			claim := sagaClaim(t, executionQueue, "crash-fast")
			repository := newFakeRunRepository(executionQueue)
			credentials := &sagaCredentialAllocator{lease: adapter.CredentialLease{ID: "lease-crash"}}
			service := sagaService(t, repository, commands, credentials, nil, WithRunFaultInjector(FaultInjectorFunc(func(stage saga.Stage) error {
				if stage == target {
					return ErrInjectedCrash
				}
				return nil
			})))
			outcome := service.Process(context.Background(), claim)
			if outcome.Action != queue.ActionRecover {
				t.Fatalf("outcome = %+v", outcome)
			}
			_, run := repository.snapshots()
			if run.ID == "" {
				t.Fatal("Run was not durably created")
			}
			state, err := repository.GetSagaState(context.Background(), domain.Owner{Kind: claim.Task.PrincipalKind, ID: claim.Task.PrincipalID}, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state.Stage != target {
				t.Fatalf("saga stage=%s want=%s", state.Stage, target)
			}
			if len(repository.runs) != 1 {
				t.Fatalf("run count=%d", len(repository.runs))
			}
			// A real gateway crash would terminate this process. In the in-process
			// fault fixture, allow any short-lived child to finish before cleanup.
			time.Sleep(80 * time.Millisecond)
		})
	}
}

func TestACKFailureDoesNotRerunCompletedProviderProcess(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix fixture")
	}
	current := time.Now().UTC()
	memory := queue.NewMemoryQueue(nil)
	memory.SetClock(func() time.Time { return current })
	counter := filepath.Join(t.TempDir(), "invocations")
	commands := map[string]adapter.CommandSpec{
		"count": {Executable: "/bin/sh", Args: []string{"-c", "printf x >> \"$1\"", "sh", counter}, Dir: t.TempDir()},
	}
	_, initialDelivery := enqueueShellTask(t, memory, "count")
	repository := newFakeRunRepository(memory)
	credentials := &sagaCredentialAllocator{lease: adapter.CredentialLease{ID: "ack-lease"}}
	service := sagaService(t, repository, commands, credentials, nil)
	wrapped := &failOnceAckQueue{Repository: memory, failures: 1}
	worker, err := queue.NewWorker(wrapped, service, "ack-worker", queue.WithWorkerClaimLimit(1), queue.WithWorkerTiming(time.Millisecond, 100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.DrainOnce(context.Background()); err == nil {
		t.Fatal("first ACK failure was not returned")
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "x" {
		t.Fatalf("provider invocation evidence = %q", data)
	}
	current = current.Add(time.Minute)
	if err := worker.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "x" {
		t.Fatalf("provider was rerun after ACK failure: %q", data)
	}
	delivery, ok := memory.GetDelivery(initialDelivery.ID)
	if !ok || delivery.Status != domain.DeliveryReserved || delivery.RunID == nil {
		t.Fatalf("delivery must remain attached for crash reconciliation: %+v exists=%v", delivery, ok)
	}
	_, run := repository.snapshots()
	state, err := repository.GetSagaState(context.Background(), domain.Owner{Kind: domain.PrincipalAdmin, ID: "executor-test-owner"}, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Stage != saga.StageCredentialReleased {
		t.Fatalf("saga stage=%s; recovery must perform durable ACK", state.Stage)
	}
}

func TestCredentialReleaseFailureIsRecordedForRecovery(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix fixture")
	}
	executionQueue := queue.NewMemoryQueue(nil)
	claim := sagaClaim(t, executionQueue, "success")
	repository := newFakeRunRepository(executionQueue)
	credentials := &sagaCredentialAllocator{
		lease:   adapter.CredentialLease{ID: "lease-release-failure"},
		release: errors.New("injected credential release failure"),
	}
	service := sagaService(t, repository, shellFixtureCommands(t), credentials, nil)
	outcome := service.Process(context.Background(), claim)
	if outcome.Action != queue.ActionAck {
		t.Fatalf("outcome = %+v", outcome)
	}
	_, run := repository.snapshots()
	state, err := repository.GetSagaState(context.Background(), domain.Owner{Kind: claim.Task.PrincipalKind, ID: claim.Task.PrincipalID}, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.CredentialLeaseID == nil || state.CompensationError == nil {
		t.Fatalf("credential release recovery state missing: %+v", state)
	}
}
