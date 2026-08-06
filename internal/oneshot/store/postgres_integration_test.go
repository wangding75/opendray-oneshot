//go:build postgres

package store

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/saga"
	rootstore "github.com/opendray/opendray-v2/internal/store"
)

func postgresStore(t *testing.T) (*Store, *rootstore.Store) {
	t.Helper()
	dsn := os.Getenv("OPENDRAY_DEV_DB_URL")
	if dsn == "" {
		t.Skip("OPENDRAY_DEV_DB_URL not set; use a disposable PostgreSQL database")
	}
	ctx := context.Background()
	root, err := rootstore.Open(ctx, dsn, 4)
	if err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}
	isolated := isolatedDSN(t, ctx, root, dsn, "od08")
	storeRoot, err := rootstore.Open(ctx, isolated, 4)
	if err != nil {
		root.Close()
		t.Fatalf("open isolated db: %v", err)
	}
	t.Cleanup(func() {
		storeRoot.Close()
		root.Close()
	})
	if err := storeRoot.Migrate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		storeRoot.Close()
		root.Close()
		t.Fatalf("migrate isolated db: %v", err)
	}
	return New(storeRoot.Pool()), storeRoot
}

func isolatedDSN(t *testing.T, ctx context.Context, admin *rootstore.Store, dsn, prefix string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Skipf("cannot parse OPENDRAY_DEV_DB_URL for isolated db: %v", err)
	}
	name := fmt.Sprintf("%s_%d_%d", prefix, os.Getpid(), time.Now().UnixNano()%1_000_000)
	if _, err := admin.Pool().Exec(ctx, `CREATE DATABASE "`+name+`"`); err != nil {
		t.Fatalf("create isolated db: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Pool().Exec(context.Background(), `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`)
	})
	parsed.Path = "/" + name
	isolated := parsed.String()
	isolatedRoot, err := rootstore.Open(ctx, isolated, 1)
	if err != nil {
		t.Fatalf("open isolated db for extension: %v", err)
	}
	defer isolatedRoot.Close()
	if _, err := isolatedRoot.Pool().Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector;`); err != nil {
		t.Fatalf("enable vector extension in isolated db: %v", err)
	}
	return isolated
}

func seedProvider(t *testing.T, root *rootstore.Store, id string) {
	t.Helper()
	_, err := root.Pool().Exec(context.Background(), `
INSERT INTO providers (id,manifest_hash,config,enabled)
VALUES ($1,'od08-test','{}'::jsonb,true)
ON CONFLICT (id) DO NOTHING`, id)
	if err != nil {
		t.Fatal(err)
	}
}

func makeTaskDelivery(t *testing.T, owner domain.Owner, providerID, sourceMessage, key string, now time.Time) (domain.TaskSnapshot, domain.DeliverySnapshot) {
	t.Helper()
	task, err := domain.NewTask(domain.TaskArgs{
		Owner: owner, ProjectID: "project-od08", ProviderID: providerID,
		Source: domain.Source{Kind: domain.SourceTelegram, ChannelID: "channel-od08", SourceMessageID: sourceMessage},
		Prompt: "persist this task",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := domain.NewDelivery(domain.DeliveryArgs{
		TaskID: task.Snapshot().ID, Operation: domain.DeliveryNew, RequestedBy: owner,
		Input:          domain.DeliveryInput{AttachmentRefs: []string{}, Options: map[string]any{}},
		IdempotencyKey: key, PayloadSHA256: strings.Repeat("a", 64), MaxAttempts: 3,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := task.QueueInitialDelivery(delivery.Snapshot(), now.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	return task.Snapshot(), delivery.Snapshot()
}

func TestPostgresStoreCRUDOwnershipAndConstraints(t *testing.T) {
	store, root := postgresStore(t)
	defer root.Close()
	ctx := context.Background()
	providerID := "od08-provider-" + strings.ToLower(time.Now().UTC().Format("150405.000000"))
	seedProvider(t, root, providerID)
	owner := domain.Owner{Kind: domain.PrincipalAdmin, ID: "od08-owner"}
	other := domain.Owner{Kind: domain.PrincipalAdmin, ID: "other-owner"}
	_, _ = root.Pool().Exec(ctx, `DELETE FROM oneshot_idempotency_keys WHERE principal_kind=$1 AND principal_id=$2`, owner.Kind, owner.ID)
	_, _ = root.Pool().Exec(ctx, `DELETE FROM oneshot_runtime_contexts WHERE principal_kind=$1 AND principal_id=$2`, owner.Kind, owner.ID)
	_, _ = root.Pool().Exec(ctx, `DELETE FROM oneshot_tasks WHERE principal_kind=$1 AND principal_id=$2`, owner.Kind, owner.ID)
	now := time.Now().UTC().Add(time.Second)

	task, delivery := makeTaskDelivery(t, owner, providerID, "message-1", "key-1", now)
	persistedTask, persistedDelivery, err := store.CreateTaskWithDelivery(ctx, task, delivery)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = root.Pool().Exec(ctx, `DELETE FROM oneshot_idempotency_keys WHERE principal_kind=$1 AND principal_id=$2`, owner.Kind, owner.ID)
		_, _ = root.Pool().Exec(ctx, `DELETE FROM oneshot_runtime_contexts WHERE principal_kind=$1 AND principal_id=$2`, owner.Kind, owner.ID)
		_, _ = root.Pool().Exec(ctx, `DELETE FROM oneshot_tasks WHERE id=$1`, persistedTask.ID)
		_, _ = root.Pool().Exec(ctx, `DELETE FROM providers WHERE id=$1`, providerID)
	})

	got, err := store.GetTask(ctx, owner, persistedTask.ID)
	if err != nil || got.ID != persistedTask.ID {
		t.Fatalf("GetTask=%+v err=%v", got, err)
	}
	if _, err := store.GetTask(ctx, other, persistedTask.ID); !domain.HasCode(err, domain.ErrorTaskNotFound) {
		t.Fatalf("cross-owner GetTask err=%v; want task_not_found", err)
	}
	page, err := store.ListTasks(ctx, owner, PageRequest{Limit: 1})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("ListTasks=%+v err=%v", page, err)
	}

	duplicateTask, duplicateDelivery := makeTaskDelivery(t, owner, providerID, "message-1", "key-2", now.Add(time.Second))
	_, _, err = store.CreateTaskWithDelivery(ctx, duplicateTask, duplicateDelivery)
	if !domain.HasCode(err, domain.ErrorIdempotencyConflict) {
		t.Fatalf("duplicate Telegram source err=%v; want idempotency conflict", err)
	}

	deliveryAggregate, err := domain.RestoreDelivery(persistedDelivery)
	if err != nil {
		t.Fatal(err)
	}
	reserveAt := now.Add(2 * time.Second)
	if err := deliveryAggregate.Reserve("worker-1", reserveAt.Add(time.Minute), reserveAt); err != nil {
		t.Fatal(err)
	}
	runAggregate, err := domain.NewRun(persistedTask, deliveryAggregate.Snapshot(), nil, reserveAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := runAggregate.Start(); err != nil {
		t.Fatal(err)
	}
	if err := deliveryAggregate.AttachRun(runAggregate.Snapshot().ID, reserveAt.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	taskAggregate, err := domain.RestoreTask(persistedTask)
	if err != nil {
		t.Fatal(err)
	}
	if err := taskAggregate.StartRun(deliveryAggregate.Snapshot(), runAggregate.Snapshot(), reserveAt.Add(2*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	initialSaga := saga.State{
		RunID: runAggregate.Snapshot().ID, TaskID: taskAggregate.Snapshot().ID,
		DeliveryID: deliveryAggregate.Snapshot().ID, Stage: saga.StageRunCreated,
		UpdatedAt: reserveAt.Add(2 * time.Millisecond),
	}
	persistedTask, persistedDelivery, run, err := store.CreateRunWithSaga(
		ctx, owner, taskAggregate.Snapshot(), persistedTask.Version, deliveryAggregate.Snapshot(), runAggregate.Snapshot(), initialSaga)
	if err != nil {
		t.Fatal(err)
	}
	persistedSaga, err := store.GetSagaState(ctx, owner, run.ID)
	if err != nil || persistedSaga.Stage != saga.StageRunCreated {
		t.Fatalf("initial Saga was not atomic with Run creation: %+v err=%v", persistedSaga, err)
	}
	if run.ID == "" || persistedTask.CurrentRunID == nil || persistedDelivery.RunID == nil {
		t.Fatalf("run transaction did not bind aggregates: task=%+v delivery=%+v run=%+v", persistedTask, persistedDelivery, run)
	}

	rawArtifact, err := domain.NewArtifact(domain.ArtifactArgs{
		TaskID: persistedTask.ID, RunID: &run.ID, Kind: domain.ArtifactRawStdout,
		Name: "stdout.bin", ContentType: "application/octet-stream", SizeBytes: 5,
		SHA256: strings.Repeat("b", 64), StorageKey: "runs/" + run.ID + "/stdout.bin",
		Metadata: map[string]any{}, CreatedAt: reserveAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := "hello"
	record, err := domain.NewStreamRecord(domain.StreamRecordArgs{
		RunID: run.ID, Sequence: 1, Stream: domain.StreamStdout, ByteOffset: 0, ByteLength: 5,
		RawArtifactID: rawArtifact.Snapshot().ID, Text: &text, DecodeStatus: domain.DecodeValidUTF8,
		SHA256: strings.Repeat("c", 64), ReceivedAt: reserveAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	recordSnapshot := record.Snapshot()
	event, err := domain.NewStandardEvent(domain.StandardEventArgs{
		RunID: run.ID, Sequence: 1, Type: "oneshot.output.delta",
		SourceStreamRecordID: &recordSnapshot.ID, AdapterID: "shell", AdapterVersion: "1",
		Content: map[string]any{"text": "hello"}, OccurredAt: reserveAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PersistOutputBatch(ctx, owner, run.ID, OutputBatch{
		Artifacts:      []domain.ArtifactSnapshot{rawArtifact.Snapshot()},
		StreamRecords:  []domain.StreamRecordSnapshot{record.Snapshot()},
		StandardEvents: []domain.StandardEventSnapshot{event.Snapshot()},
	}); err != nil {
		t.Fatal(err)
	}
	if records, err := store.ListStreamRecords(ctx, owner, run.ID, 0, 10); err != nil || len(records) != 1 {
		t.Fatalf("stream records=%+v err=%v", records, err)
	}

	// A second active Run for the same Task is rejected by the partial unique index.
	_, err = root.Pool().Exec(ctx, `
INSERT INTO oneshot_deliveries (
 id,task_id,operation,requested_by_kind,requested_by_id,input,idempotency_key,payload_sha256,
 status,attempt,max_attempts,available_at,lease_owner,lease_until,created_at,updated_at
) VALUES ('odl_conflict',$1,'retry','admin',$2,'{"attachment_refs":[],"options":{}}',
 'conflict-key',$3,'reserved',1,3,NOW(),'worker-2',NOW()+interval '1 minute',NOW(),NOW())`,
		persistedTask.ID, owner.ID, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	_, err = root.Pool().Exec(ctx, `
INSERT INTO oneshot_runs (id,task_id,delivery_id,provider_id,status,created_at)
VALUES ('orn_conflict',$1,'odl_conflict',$2,'created',NOW())`, persistedTask.ID, providerID)
	var pgConflict bool
	if err != nil {
		pgConflict = true
	}
	if !pgConflict {
		t.Fatal("database allowed two active Runs for one Task")
	}

	if _, err := store.GetRun(ctx, other, run.ID); !domain.HasCode(err, domain.ErrorRunNotFound) {
		t.Fatalf("cross-owner GetRun err=%v; want run_not_found", err)
	}

	// Context optimistic version and owner filtering.
	contextAggregate, err := domain.NewRuntimeContext(domain.RuntimeContextArgs{
		Owner: owner, ProjectID: persistedTask.ProjectID, ProviderID: providerID,
		ProviderContextID: "provider-context-1", WorkspacePath: "/tmp/opendray-od08",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	persistedContext, err := store.CreateRuntimeContext(ctx, contextAggregate.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetRuntimeContext(ctx, other, persistedContext.ID); !domain.HasCode(err, domain.ErrorContextNotFound) {
		t.Fatalf("cross-owner context err=%v", err)
	}
	if err := contextAggregate.Acquire(owner, persistedTask.ProjectID, providerID, persistedContext.Version, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateRuntimeContext(ctx, owner, contextAggregate.Snapshot(), persistedContext.Version); err != nil {
		t.Fatal(err)
	}

	// Idempotency payload mismatch remains a stable conflict.
	idem := IdempotencyRecord{Owner: owner, Method: "POST", CanonicalPath: "/api/v1/oneshot/tasks", Key: "idem-1", PayloadSHA256: strings.Repeat("e", 64)}
	if _, err := store.CreateIdempotencyRecord(ctx, idem); err != nil {
		t.Fatal(err)
	}
	idem.PayloadSHA256 = strings.Repeat("f", 64)
	if _, err := store.CreateIdempotencyRecord(ctx, idem); !domain.HasCode(err, domain.ErrorIdempotencyConflict) {
		t.Fatalf("duplicate idempotency err=%v", err)
	}

}

func TestPostgresStoreFinalizeRunWithTaskAtomic(t *testing.T) {
	store, root := postgresStore(t)
	defer root.Close()
	ctx := context.Background()
	ts := strings.ToLower(time.Now().UTC().Format("150405.000000"))
	providerID := "od10-provider-" + ts
	seedProvider(t, root, providerID)
	owner := domain.Owner{Kind: domain.PrincipalAdmin, ID: "od10-owner-" + ts}
	now := time.Now().UTC().Add(time.Second)
	taskSnapshot, deliverySnapshot := makeTaskDelivery(t, owner, providerID, "od10-message-"+ts, "od10-key-"+ts, now)
	persistedTask, persistedDelivery, err := store.CreateTaskWithDelivery(ctx, taskSnapshot, deliverySnapshot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = root.Pool().Exec(ctx, `DELETE FROM oneshot_tasks WHERE id=$1`, persistedTask.ID)
		_, _ = root.Pool().Exec(ctx, `DELETE FROM providers WHERE id=$1`, providerID)
	})

	delivery, err := domain.RestoreDelivery(persistedDelivery)
	if err != nil {
		t.Fatal(err)
	}
	reserveAt := now.Add(time.Second)
	if err := delivery.Reserve("od10-worker", reserveAt.Add(time.Minute), reserveAt); err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewRun(persistedTask, delivery.Snapshot(), nil, reserveAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	if err := delivery.AttachRun(run.Snapshot().ID, reserveAt.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	task, err := domain.RestoreTask(persistedTask)
	if err != nil {
		t.Fatal(err)
	}
	if err := task.StartRun(delivery.Snapshot(), run.Snapshot(), reserveAt.Add(2*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	persistedTask, _, persistedRun, err := store.CreateRunWithState(ctx, owner, task.Snapshot(), persistedTask.Version, delivery.Snapshot(), run.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	run, err = domain.RestoreRun(persistedRun)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := reserveAt.Add(3 * time.Millisecond)
	if err := run.ProcessStarted(12345, startedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateRun(ctx, owner, run.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := run.ProcessExited(0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateRun(ctx, owner, run.Snapshot()); err != nil {
		t.Fatal(err)
	}
	finishedAt := startedAt.Add(time.Second)
	if err := run.FinalizeSuccess(true, finishedAt); err != nil {
		t.Fatal(err)
	}
	task, err = domain.RestoreTask(persistedTask)
	if err != nil {
		t.Fatal(err)
	}
	if err := task.MarkRunCompleted(run.Snapshot(), nil, finishedAt); err != nil {
		t.Fatal(err)
	}
	finalTask, finalRun, err := store.FinalizeRunWithTask(ctx, owner, task.Snapshot(), persistedTask.Version, run.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if finalTask.Status != domain.TaskCompleted || finalRun.Status != domain.RunCompleted || finalRun.ExitCode == nil || *finalRun.ExitCode != 0 {
		t.Fatalf("final task/run = %+v / %+v", finalTask, finalRun)
	}
	loadedTask, err := store.GetTask(ctx, owner, finalTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	loadedRun, err := store.GetRun(ctx, owner, finalRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedTask.Status != domain.TaskCompleted || loadedRun.Status != domain.RunCompleted {
		t.Fatalf("persisted final task/run = %+v / %+v", loadedTask, loadedRun)
	}
}

func TestPostgresRunLifecycleConcurrentWritersKeepBothEvents(t *testing.T) {
	store, root := postgresStore(t)
	defer root.Close()
	ctx := context.Background()
	providerID := "lifecycle-provider-" + strings.ToLower(time.Now().UTC().Format("150405.000000"))
	seedProvider(t, root, providerID)
	owner := domain.Owner{Kind: domain.PrincipalAdmin, ID: "lifecycle-owner-" + strings.ToLower(time.Now().UTC().Format("150405.000000"))}
	now := time.Now().UTC().Add(time.Second)
	taskSnapshot, deliverySnapshot := makeTaskDelivery(t, owner, providerID, "lifecycle-message-"+strings.ToLower(time.Now().UTC().Format("150405.000000")), "lifecycle-key-"+strings.ToLower(time.Now().UTC().Format("150405.000000")), now)
	persistedTask, persistedDelivery, err := store.CreateTaskWithDelivery(ctx, taskSnapshot, deliverySnapshot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = root.Pool().Exec(ctx, `DELETE FROM oneshot_tasks WHERE id=$1`, persistedTask.ID)
		_, _ = root.Pool().Exec(ctx, `DELETE FROM providers WHERE id=$1`, providerID)
	})

	delivery, err := domain.RestoreDelivery(persistedDelivery)
	if err != nil {
		t.Fatal(err)
	}
	reserveAt := now.Add(time.Second)
	if err := delivery.Reserve("lifecycle-worker", reserveAt.Add(time.Minute), reserveAt); err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewRun(persistedTask, delivery.Snapshot(), nil, reserveAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	if err := delivery.AttachRun(run.Snapshot().ID, reserveAt.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	task, err := domain.RestoreTask(persistedTask)
	if err != nil {
		t.Fatal(err)
	}
	if err := task.StartRun(delivery.Snapshot(), run.Snapshot(), reserveAt.Add(2*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	persistedTask, _, persistedRun, err := store.CreateRunWithState(ctx, owner, task.Snapshot(), persistedTask.Version, delivery.Snapshot(), run.Snapshot())
	if err != nil {
		t.Fatal(err)
	}

	topics := []string{"oneshot.run.started", "oneshot.run.failed"}
	start := make(chan struct{})
	errs := make(chan error, len(topics))
	var wg sync.WaitGroup
	for i, topic := range topics {
		wg.Add(1)
		go func(topic string, offset int) {
			defer wg.Done()
			<-start
			errs <- insertRunLifecycle(ctx, root.Pool(), owner, persistedRun, topic, reserveAt.Add(time.Duration(offset+3)*time.Millisecond))
		}(topic, i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	rows, err := root.Pool().Query(ctx, `
SELECT sequence,topic
FROM oneshot_lifecycle_events
WHERE aggregate_kind='run' AND aggregate_id=$1
ORDER BY sequence`, persistedRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	var sequences []int64
	for rows.Next() {
		var sequence int64
		var topic string
		if err := rows.Scan(&sequence, &topic); err != nil {
			t.Fatal(err)
		}
		sequences = append(sequences, sequence)
		seen[topic] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, topic := range topics {
		if !seen[topic] {
			t.Fatalf("concurrent lifecycle topic was lost: %s; sequences=%v seen=%v", topic, sequences, seen)
		}
	}
	for i, sequence := range sequences {
		if sequence != int64(i+1) {
			t.Fatalf("lifecycle sequence gap/duplicate: %v", sequences)
		}
	}
}
