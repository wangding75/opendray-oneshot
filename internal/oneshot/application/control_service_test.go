package application

import (
	"context"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/store"
)

type controlRepoFixture struct {
	task           domain.TaskSnapshot
	run            domain.RunSnapshot
	replayTask     domain.TaskSnapshot
	replayDelivery domain.DeliverySnapshot
	replayed       bool
	getTaskCalls   int
}

func (r *controlRepoFixture) GetTask(context.Context, domain.Owner, string) (domain.TaskSnapshot, error) {
	r.getTaskCalls++
	return r.task, nil
}
func (r *controlRepoFixture) GetRun(context.Context, domain.Owner, string) (domain.RunSnapshot, error) {
	return r.run, nil
}
func (r *controlRepoFixture) ListDeliveries(context.Context, domain.Owner, string, store.PageRequest) (store.Page[domain.DeliverySnapshot], error) {
	return store.Page[domain.DeliverySnapshot]{}, nil
}
func (r *controlRepoFixture) UpdateTask(_ context.Context, _ domain.Owner, task domain.TaskSnapshot, _ int64) (domain.TaskSnapshot, error) {
	r.task = task
	return task, nil
}
func (r *controlRepoFixture) FindRetryReplay(context.Context, domain.Owner, string, string, string) (domain.TaskSnapshot, domain.DeliverySnapshot, bool, error) {
	return r.replayTask, r.replayDelivery, r.replayed, nil
}
func (r *controlRepoFixture) CreateRetryDelivery(_ context.Context, _ domain.Owner, task domain.TaskSnapshot, _ int64, delivery domain.DeliverySnapshot, _ string, _ *time.Time) (domain.TaskSnapshot, domain.DeliverySnapshot, bool, error) {
	return task, delivery, true, nil
}

type cancelQueueFixture struct{ calls int }

func (q *cancelQueueFixture) Cancel(context.Context, string, domain.Owner, string) (domain.DeliverySnapshot, error) {
	q.calls++
	return domain.DeliverySnapshot{}, nil
}

type activeCancelFixture struct{ calls int }

func (a *activeCancelFixture) CancelActiveRun(context.Context, string) error { a.calls++; return nil }

type treeTerminatorFixture struct{ calls int }

func (t *treeTerminatorFixture) TerminateExistingTree(context.Context, int, time.Duration) error {
	t.calls++
	return nil
}

func validControlTask(t *testing.T, status domain.TaskStatus) domain.TaskSnapshot {
	t.Helper()
	now := time.Now().UTC()
	task, err := domain.NewTask(domain.TaskArgs{
		Owner: domain.Owner{Kind: domain.PrincipalAdmin, ID: "owner-1"}, ProjectID: "project-1",
		ProviderID: "codex", Model: "default-model", Source: domain.Source{Kind: domain.SourceAPI}, Prompt: "test",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := task.Snapshot()
	snapshot.Status = status
	snapshot.Version = 2
	if status == domain.TaskRunning {
		runID := domain.NewRunID()
		snapshot.CurrentRunID = &runID
	}
	return snapshot
}

func TestCancelRunningTaskTerminatesTrackedAndRecoveredTree(t *testing.T) {
	task := validControlTask(t, domain.TaskRunning)
	pid := 1234
	repo := &controlRepoFixture{task: task, run: domain.RunSnapshot{ID: *task.CurrentRunID, PID: &pid}}
	queue := &cancelQueueFixture{}
	active := &activeCancelFixture{}
	tree := &treeTerminatorFixture{}
	service, err := NewControlService(repo, queue, active, tree, "worker", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CancelTask(context.Background(), CancelTaskCommand{
		Owner: domain.Owner{Kind: domain.PrincipalAdmin, ID: "owner-1"}, ProjectID: "project-1", TaskID: task.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Run == nil || active.calls != 1 || tree.calls != 1 || queue.calls != 0 {
		t.Fatalf("cancel path did not terminate exact process tree: result=%+v active=%d tree=%d queue=%d", result, active.calls, tree.calls, queue.calls)
	}
}

func TestRetryReturnsAtomicReplayWithoutReadingAdvancedTask(t *testing.T) {
	repo := &controlRepoFixture{replayed: true, replayTask: domain.TaskSnapshot{ID: domain.NewTaskID()}, replayDelivery: domain.DeliverySnapshot{ID: domain.NewDeliveryID()}}
	service, err := NewControlService(repo, &cancelQueueFixture{}, nil, nil, "worker", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RetryTask(context.Background(), RetryTaskCommand{
		Owner: domain.Owner{Kind: domain.PrincipalAdmin, ID: "owner-1"}, ProjectID: "project-1", TaskID: "task-1", IdempotencyKey: "retry-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created || result.Delivery.ID != repo.replayDelivery.ID || repo.getTaskCalls != 0 {
		t.Fatalf("retry replay was not returned atomically: %+v getTaskCalls=%d", result, repo.getTaskCalls)
	}
}
