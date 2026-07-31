//go:build postgres

package queue_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/application"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/queue"
	rootstore "github.com/opendray/opendray-v2/internal/store"
)

func liveQueue(t *testing.T) (*queue.PostgresQueue, *rootstore.Store, domain.Owner, string) {
	t.Helper()
	dsn := os.Getenv("OPENDRAY_DEV_DB_URL")
	if dsn == "" {
		t.Skip("OPENDRAY_DEV_DB_URL not set; use a disposable PostgreSQL database")
	}
	ctx := context.Background()
	root, err := rootstore.Open(ctx, dsn, 8)
	if err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}
	if err := root.Migrate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		root.Close()
		t.Fatalf("migrate: %v", err)
	}
	providerID := "od09-provider-" + time.Now().UTC().Format("150405000000")
	if _, err := root.Pool().Exec(ctx, `INSERT INTO providers (id,manifest_hash,config,enabled)
VALUES ($1,'od09-test','{}'::jsonb,true) ON CONFLICT (id) DO NOTHING`, providerID); err != nil {
		root.Close()
		t.Fatal(err)
	}
	owner := domain.Owner{Kind: domain.PrincipalAdmin, ID: "od09-owner-" + time.Now().UTC().Format("150405000000")}
	t.Cleanup(func() {
		_, _ = root.Pool().Exec(ctx, `DELETE FROM oneshot_idempotency_keys WHERE principal_kind=$1 AND principal_id=$2`, owner.Kind, owner.ID)
		_, _ = root.Pool().Exec(ctx, `DELETE FROM oneshot_tasks WHERE principal_kind=$1 AND principal_id=$2`, owner.Kind, owner.ID)
		_, _ = root.Pool().Exec(ctx, `DELETE FROM providers WHERE id=$1`, providerID)
		root.Close()
	})
	return queue.NewPostgresQueue(root.Pool(), nil), root, owner, providerID
}

func enqueueLive(t *testing.T, repository queue.Repository, owner domain.Owner, providerID, key string) application.CreateTaskResult {
	t.Helper()
	service := application.NewDispatchService(repository)
	result, err := service.CreateTask(context.Background(), application.CreateTaskCommand{
		Owner: owner, ProjectID: "od09-project", ProviderID: providerID,
		Source:         domain.Source{Kind: domain.SourceAPI, ClientRequestID: key},
		Prompt:         "queue integration " + key,
		Input:          domain.DeliveryInput{AttachmentRefs: []string{}, Options: map[string]any{}},
		IdempotencyKey: key, MaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPostgresQueueCompetitionLeaseRecoveryIdempotencyAndRestart(t *testing.T) {
	repository, root, owner, providerID := liveQueue(t)
	ctx := context.Background()
	created := enqueueLive(t, repository, owner, providerID, "competition")

	var winners atomic.Int32
	var winner queue.Claim
	var winnerMu sync.Mutex
	var wait sync.WaitGroup
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			claims, err := repository.ClaimDue(ctx, "worker-"+time.Duration(index).String(), 1, time.Minute)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if len(claims) == 1 {
				winners.Add(1)
				winnerMu.Lock()
				winner = claims[0]
				winnerMu.Unlock()
			}
		}(index)
	}
	wait.Wait()
	if winners.Load() != 1 {
		t.Fatalf("claim winners=%d; want 1", winners.Load())
	}
	if winner.Delivery.ID != created.Delivery.ID {
		t.Fatalf("winner=%s created=%s", winner.Delivery.ID, created.Delivery.ID)
	}

	// Persist a terminal Run and bind it to the claimed Delivery. A restarted
	// queue must reconcile/ack it rather than create another Run.
	runID := domain.NewRunID()
	tx, err := root.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO oneshot_runs (
 id,task_id,delivery_id,provider_id,status,started_at,finished_at,created_at
) VALUES ($1,$2,$3,$4,'completed',clock_timestamp(),clock_timestamp(),clock_timestamp())`,
		runID, created.Task.ID, created.Delivery.ID, providerID)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE oneshot_deliveries SET run_id=$1 WHERE id=$2`, runID, created.Delivery.ID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE oneshot_tasks SET status='completed',current_run_id=$1,
 version=version+1,updated_at=clock_timestamp() WHERE id=$2`, runID, created.Task.ID)
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Ack(ctx, created.Delivery.ID, *winner.Delivery.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	claims, err := repository.ClaimDue(ctx, "restart-worker", 1, time.Minute)
	if err != nil || len(claims) != 0 {
		t.Fatalf("restart claims=%+v err=%v", claims, err)
	}

	// A stable lease is claimed first, then expired via direct DB update so
	// recovery does not depend on wall-clock sleep.
	recovered := enqueueLive(t, repository, owner, providerID, "lease-recovery")
	firstClaims, err := repository.ClaimDue(ctx, "crashed-worker", 1, time.Minute)
	if err != nil || len(firstClaims) != 1 || firstClaims[0].Delivery.ID != recovered.Delivery.ID {
		t.Fatalf("short claim=%+v err=%v", firstClaims, err)
	}
	if firstClaims[0].Delivery.Attempt != 1 {
		t.Fatalf("first claim attempt=%d; want 1", firstClaims[0].Delivery.Attempt)
	}
	result, err := root.Pool().Exec(
		ctx,
		`UPDATE oneshot_deliveries
         SET lease_until = clock_timestamp() - interval '1 second'
         WHERE id = $1
           AND status = 'reserved'
           AND attempt = 1`,
		recovered.Delivery.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsAffected() != 1 {
		t.Fatalf("expire lease rows=%d; want 1", result.RowsAffected())
	}
	secondClaims, err := repository.ClaimDue(ctx, "recovery-worker", 1, time.Minute)
	if err != nil || len(secondClaims) != 1 || secondClaims[0].Delivery.ID != recovered.Delivery.ID || secondClaims[0].Delivery.Attempt != 2 {
		t.Fatalf("recovery=%+v err=%v", secondClaims, err)
	}

	// Same key/payload replays; different payload conflicts.
	service := application.NewDispatchService(repository)
	command := application.CreateTaskCommand{
		Owner: owner, ProjectID: "od09-project", ProviderID: providerID,
		Source: domain.Source{Kind: domain.SourceAPI, ClientRequestID: "idem"},
		Prompt: "idempotent", Input: domain.DeliveryInput{AttachmentRefs: []string{}, Options: map[string]any{}},
		IdempotencyKey: "idem", MaxAttempts: 3,
	}
	first, err := service.CreateTask(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateTask(ctx, command)
	if err != nil || first.Task.ID != second.Task.ID || second.Created {
		t.Fatalf("replay first=%+v second=%+v err=%v", first, second, err)
	}
	command.Prompt = "different"
	if _, err := service.CreateTask(ctx, command); !domain.HasCode(err, domain.ErrorIdempotencyConflict) {
		t.Fatalf("payload conflict err=%v", err)
	}
}
