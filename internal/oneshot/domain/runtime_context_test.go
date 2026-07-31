package domain

import (
	"testing"
	"time"
)

func TestRuntimeContextLegalTransitions(t *testing.T) {
	t.Run("active acquire release", func(t *testing.T) {
		ctx := mustContext(t, testOwner(), "prj_demo", "codex", testNow)
		v1 := ctx.Snapshot().Version
		if err := ctx.Acquire(testOwner(), "prj_demo", "codex", v1, testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		v2 := ctx.Snapshot().Version
		if err := ctx.Release(v2, testNow.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("active invalidate", func(t *testing.T) {
		ctx := mustContext(t, testOwner(), "prj_demo", "codex", testNow)
		if err := ctx.Invalidate(ctx.Snapshot().Version, testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("active revoke", func(t *testing.T) {
		ctx := mustContext(t, testOwner(), "prj_demo", "codex", testNow)
		if err := ctx.Revoke(ctx.Snapshot().Version, testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("busy invalidate", func(t *testing.T) {
		ctx := mustContext(t, testOwner(), "prj_demo", "codex", testNow)
		if err := ctx.Acquire(testOwner(), "prj_demo", "codex", ctx.Snapshot().Version, testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := ctx.Invalidate(ctx.Snapshot().Version, testNow.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("busy revoke", func(t *testing.T) {
		ctx := mustContext(t, testOwner(), "prj_demo", "codex", testNow)
		if err := ctx.Acquire(testOwner(), "prj_demo", "codex", ctx.Snapshot().Version, testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := ctx.Revoke(ctx.Snapshot().Version, testNow.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRuntimeContextRejectsPrincipalProjectProviderMismatch(t *testing.T) {
	cases := []struct {
		name      string
		owner     Owner
		projectID string
		provider  string
	}{
		{"principal kind", Owner{Kind: PrincipalIntegration, ID: "operator"}, "prj_demo", "codex"},
		{"principal id", Owner{Kind: PrincipalAdmin, ID: "other"}, "prj_demo", "codex"},
		{"project", testOwner(), "prj_other", "codex"},
		{"provider", testOwner(), "prj_demo", "claude"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := mustContext(t, testOwner(), "prj_demo", "codex", testNow)
			err := ctx.Acquire(tc.owner, tc.projectID, tc.provider, ctx.Snapshot().Version, testNow.Add(time.Second))
			requireCode(t, err, ErrorContextOwnerMismatch)
			if ctx.Snapshot().Status != ContextActive {
				t.Fatal("context mutated after owner mismatch")
			}
		})
	}
}

func TestRuntimeContextPreventsConcurrentAcquire(t *testing.T) {
	ctx := mustContext(t, testOwner(), "prj_demo", "codex", testNow)
	if err := ctx.Acquire(testOwner(), "prj_demo", "codex", ctx.Snapshot().Version, testNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	err := ctx.Acquire(testOwner(), "prj_demo", "codex", ctx.Snapshot().Version, testNow.Add(2*time.Second))
	requireCode(t, err, ErrorRunConflict)
}

func TestRuntimeContextOptimisticVersionGuard(t *testing.T) {
	ctx := mustContext(t, testOwner(), "prj_demo", "codex", testNow)
	err := ctx.Acquire(testOwner(), "prj_demo", "codex", 99, testNow.Add(time.Second))
	requireCode(t, err, ErrorRunConflict)
	if ctx.Snapshot().Version != 1 || ctx.Snapshot().Status != ContextActive {
		t.Fatal("context mutated after version conflict")
	}
}

func TestRuntimeContextTerminalStatesAreIrreversible(t *testing.T) {
	for _, target := range []RuntimeContextStatus{ContextInvalid, ContextRevoked} {
		t.Run(target.String(), func(t *testing.T) {
			ctx := mustContext(t, testOwner(), "prj_demo", "codex", testNow)
			var err error
			if target == ContextInvalid {
				err = ctx.Invalidate(ctx.Snapshot().Version, testNow.Add(time.Second))
			} else {
				err = ctx.Revoke(ctx.Snapshot().Version, testNow.Add(time.Second))
			}
			if err != nil {
				t.Fatal(err)
			}
			err = ctx.Release(ctx.Snapshot().Version, testNow.Add(2*time.Second))
			requireCode(t, err, ErrorInvalidTransition)
			if ctx.Snapshot().Status != target {
				t.Fatal("terminal context mutated")
			}
		})
	}
}

func TestRuntimeContextContainsNoSessionOrProcessState(t *testing.T) {
	ctx := mustContext(t, testOwner(), "prj_demo", "codex", testNow).Snapshot()
	if ctx.ProviderContextID == "" || ctx.WorkspacePath == "" {
		t.Fatal("expected provider continuity metadata")
	}
}
