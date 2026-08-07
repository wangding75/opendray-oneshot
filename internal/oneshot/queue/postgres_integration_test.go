//go:build postgres

package queue_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/application"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/queue"
	"github.com/opendray/opendray-v2/internal/oneshot/workspacepolicy"
	rootstore "github.com/opendray/opendray-v2/internal/store"
)

var dbCounter atomic.Uint64

// modelResolverFn returns the requested model resolver for live Queue tests.
func modelResolverFn(model string) application.ModelResolver {
	return modelResolverFunc(func(_ context.Context, _, requested string) (string, error) {
		if requested != "" {
			return requested, nil
		}
		return model, nil
	})
}

type modelResolverFunc func(ctx context.Context, providerID, requestedModel string) (string, error)

func (f modelResolverFunc) ResolveModel(ctx context.Context, providerID, requestedModel string) (string, error) {
	return f(ctx, providerID, requestedModel)
}

func liveQueue(t *testing.T) (*queue.PostgresQueue, *rootstore.Store, domain.Owner, string) {
	t.Helper()
	dsn := os.Getenv("OPENDRAY_DEV_DB_URL")
	if dsn == "" {
		t.Skip("OPENDRAY_DEV_DB_URL not set; use a disposable PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := rootstore.Open(ctx, dsn, 8)
	if err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}

	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		admin.Close()
		t.Skipf("cannot parse OPENDRAY_DEV_DB_URL for isolated db: %v", err)
	}

	dbName := fmt.Sprintf("od09_%d_%d_%d", os.Getpid(), time.Now().UnixNano(), dbCounter.Add(1))
	if _, err := admin.Pool().Exec(ctx, `CREATE DATABASE "`+dbName+`"`); err != nil {
		admin.Close()
		t.Fatalf("create isolated database %s: %v", dbName, err)
	}

	parsed.Path = "/" + dbName
	isolated := parsed.String()

	isolatedRoot, err := rootstore.Open(ctx, isolated, 1)
	if err != nil {
		_, dropErr := admin.Pool().Exec(ctx, `DROP DATABASE IF EXISTS "`+dbName+`" WITH (FORCE)`)
		admin.Close()
		if dropErr != nil {
			t.Fatalf("open isolated db for extension: %v (and failed to drop database %s: %v)", err, dbName, dropErr)
		}
		t.Fatalf("open isolated db for extension: %v", err)
	}
	if _, err := isolatedRoot.Pool().Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector;`); err != nil {
		isolatedRoot.Close()
		_, dropErr := admin.Pool().Exec(ctx, `DROP DATABASE IF EXISTS "`+dbName+`" WITH (FORCE)`)
		admin.Close()
		if dropErr != nil {
			t.Fatalf("enable vector extension: %v (and failed to drop database %s: %v)", err, dbName, dropErr)
		}
		t.Fatalf("enable vector extension in isolated db: %v", err)
	}
	isolatedRoot.Close()

	queueRoot, err := rootstore.Open(ctx, isolated, 8)
	if err != nil {
		_, dropErr := admin.Pool().Exec(ctx, `DROP DATABASE IF EXISTS "`+dbName+`" WITH (FORCE)`)
		admin.Close()
		if dropErr != nil {
			t.Fatalf("open isolated db: %v (and failed to drop database %s: %v)", err, dbName, dropErr)
		}
		t.Fatalf("open isolated db: %v", err)
	}

	if err := queueRoot.Migrate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		queueRoot.Close()
		_, dropErr := admin.Pool().Exec(ctx, `DROP DATABASE IF EXISTS "`+dbName+`" WITH (FORCE)`)
		admin.Close()
		if dropErr != nil {
			t.Fatalf("migrate isolated db: %v (and failed to drop database %s: %v)", err, dbName, dropErr)
		}
		t.Fatalf("migrate isolated db: %v", err)
	}

	providerID := "od09-provider-" + time.Now().UTC().Format("150405000000")
	if _, err := queueRoot.Pool().Exec(ctx, `INSERT INTO providers (id,manifest_hash,config,enabled)
VALUES ($1,'od09-test','{}'::jsonb,true) ON CONFLICT (id) DO NOTHING`, providerID); err != nil {
		queueRoot.Close()
		_, dropErr := admin.Pool().Exec(ctx, `DROP DATABASE IF EXISTS "`+dbName+`" WITH (FORCE)`)
		admin.Close()
		if dropErr != nil {
			t.Fatalf("seed provider: %v (and failed to drop database %s: %v)", err, dbName, dropErr)
		}
		t.Fatalf("seed provider: %v", err)
	}

	owner := domain.Owner{Kind: domain.PrincipalAdmin, ID: "od09-owner-" + time.Now().UTC().Format("150405000000")}

	t.Cleanup(func() {
		_, _ = queueRoot.Pool().Exec(context.Background(), `DELETE FROM oneshot_idempotency_keys WHERE principal_kind=$1 AND principal_id=$2`, owner.Kind, owner.ID)
		_, _ = queueRoot.Pool().Exec(context.Background(), `DELETE FROM oneshot_tasks WHERE principal_kind=$1 AND principal_id=$2`, owner.Kind, owner.ID)
		_, _ = queueRoot.Pool().Exec(context.Background(), `DELETE FROM providers WHERE id=$1`, providerID)

		queueRoot.Close()

		if _, dropErr := admin.Pool().Exec(context.Background(), `DROP DATABASE IF EXISTS "`+dbName+`" WITH (FORCE)`); dropErr != nil {
			t.Errorf("failed to DROP DATABASE %s: %v", dbName, dropErr)
		}

		admin.Close()
	})

	return queue.NewPostgresQueue(queueRoot.Pool(), nil), queueRoot, owner, providerID
}

func enqueueLive(t *testing.T, repository queue.Repository, owner domain.Owner, providerID, key string) application.CreateTaskResult {
	t.Helper()
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	policy, err := workspacepolicy.New([]string{workspace})
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewDispatchService(repository,
		application.WithWorkspacePolicy(policy, workspace),
		application.WithModelResolver(modelResolverFn(key)))
	result, err := service.CreateTask(context.Background(), application.CreateTaskCommand{
		Owner: owner, ProjectID: "od09-project", ProviderID: providerID,
		Source:         domain.Source{Kind: domain.SourceAPI, ClientRequestID: key},
		Prompt:         "queue integration " + key,
		WorkspacePath:  workspace,
		Model:          key,
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
) VALUES ($1,$2,$3,$4,'completed',now(),now(),now())`,
		runID, created.Task.ID, created.Delivery.ID, providerID)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE oneshot_deliveries SET run_id=$1 WHERE id=$2`, runID, created.Delivery.ID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE oneshot_tasks SET status='completed',current_run_id=$1,
 version=version+1,updated_at=now() WHERE id=$2`, runID, created.Task.ID)
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
         SET lease_until = now() - interval '1 second'
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
	workspace := filepath.Join(t.TempDir(), "workspace-idem")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	policy, err := workspacepolicy.New([]string{workspace})
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewDispatchService(repository, application.WithWorkspacePolicy(policy, workspace))
	command := application.CreateTaskCommand{
		Owner: owner, ProjectID: "od09-project", ProviderID: providerID,
		Source:        domain.Source{Kind: domain.SourceAPI, ClientRequestID: "idem"},
		WorkspacePath: workspace,
		Prompt:        "idempotent", Input: domain.DeliveryInput{AttachmentRefs: []string{}, Options: map[string]any{}},
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

func TestPostgresQueueClaimPreservesModelAndAdjacentFields(t *testing.T) {
	repository, _, owner, providerID := liveQueue(t)
	ctx := context.Background()
	created := enqueueLive(t, repository, owner, providerID, "claim-model-test")

	claims, err := repository.ClaimDue(ctx, "model-worker", 1, time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("claims=%d; want 1", len(claims))
	}
	claimed := claims[0]

	if claimed.Task.ID != created.Task.ID {
		t.Fatalf("claimed.Task.ID=%s created=%s", claimed.Task.ID, created.Task.ID)
	}
	if claimed.Task.Model != "claim-model-test" {
		t.Fatalf("claimed.Task.Model=%q; want %q", claimed.Task.Model, "claim-model-test")
	}
	if claimed.Task.ProviderID != providerID {
		t.Fatalf("claimed.Task.ProviderID=%q; want %q", claimed.Task.ProviderID, providerID)
	}
	if claimed.Task.Source.Kind != created.Task.Source.Kind || claimed.Task.Source.ClientRequestID != "claim-model-test" {
		t.Fatalf("claimed.Task.Source=%+v; adjacency misaligned", claimed.Task.Source)
	}
	if claimed.Task.Prompt != created.Task.Prompt {
		t.Fatalf("claimed.Task.Prompt=%q; want %q (field shifted)", claimed.Task.Prompt, created.Task.Prompt)
	}
	if claimed.Delivery.ID != created.Delivery.ID {
		t.Fatalf("claimed.Delivery.ID=%s created=%s", claimed.Delivery.ID, created.Delivery.ID)
	}

	// A claimed (reserved) Delivery must not be claimed a second time.
	again, err := repository.ClaimDue(ctx, "model-worker-again", 1, time.Minute)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second claim returned %d deliveries; want 0 (duplicate claim)", len(again))
	}
}

func TestPostgresQueueLifecycleAndCleanup(t *testing.T) {
	dsn := os.Getenv("OPENDRAY_DEV_DB_URL")
	if dsn == "" {
		t.Skip("OPENDRAY_DEV_DB_URL not set")
	}

	ctx := context.Background()
	admin, err := rootstore.Open(ctx, dsn, 1)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()

	var dbName string

	t.Run("SubTestIsolatedQueue", func(t *testing.T) {
		_, root, _, _ := liveQueue(t)
		if root == nil {
			t.Fatal("root is nil")
		}

		err = root.Pool().QueryRow(ctx, "SELECT current_database()").Scan(&dbName)
		if err != nil {
			t.Fatal(err)
		}

		var exists bool
		err = admin.Pool().QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", dbName).Scan(&exists)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("expected database %s to exist during test", dbName)
		}
	})

	var exists bool
	err = admin.Pool().QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", dbName).Scan(&exists)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatalf("expected database %s to be dropped after test completion", dbName)
	}
}
