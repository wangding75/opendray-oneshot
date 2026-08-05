package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/adapter"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
	"github.com/opendray/opendray-v2/internal/oneshot/workspacepolicy"
)

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cwd, "..", "testdata", "fixtures", name)
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return absolute
}

func TestProcessExecutorSuccessAndNonZeroExit(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix fixture")
	}
	executor := NewProcessExecutor()
	for _, test := range []struct {
		name     string
		fixture  string
		exitCode int
		errCode  domain.ErrorCode
	}{
		{name: "success", fixture: "success.sh", exitCode: 0},
		{name: "non-zero", fixture: "nonzero.sh", exitCode: 7, errCode: domain.ErrorExecutionFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			process, err := executor.Start(context.Background(), adapter.CommandSpec{
				Executable: "/bin/sh",
				Args:       []string{fixturePath(t, test.fixture)},
				Dir:        t.TempDir(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if process.PID() <= 0 || process.StartedAt().IsZero() {
				t.Fatalf("invalid process identity pid=%d started=%v", process.PID(), process.StartedAt())
			}
			result := process.Wait()
			if result.ExitCode != test.exitCode || result.FinishedAt.Before(result.StartedAt) {
				t.Fatalf("result = %+v", result)
			}
			if test.errCode == "" && result.Err != nil {
				t.Fatalf("unexpected error: %v", result.Err)
			}
			if test.errCode != "" && !domain.HasCode(result.Err, test.errCode) {
				t.Fatalf("error = %v", result.Err)
			}
		})
	}
}

func TestProcessExecutorCommandAndCWDFailures(t *testing.T) {
	executor := NewProcessExecutor()
	_, err := executor.Start(context.Background(), adapter.CommandSpec{
		Executable: filepath.Join(t.TempDir(), "missing-command"),
		Dir:        t.TempDir(),
	})
	if !domain.HasCode(err, domain.ErrorProviderUnavailable) {
		t.Fatalf("missing command error = %v", err)
	}

	_, err = executor.Start(context.Background(), adapter.CommandSpec{
		Executable: "/bin/sh",
		Dir:        filepath.Join(t.TempDir(), "missing-cwd"),
	})
	if !domain.HasCode(err, domain.ErrorExecutionFailed) {
		t.Fatalf("missing cwd error = %v", err)
	}
}

func TestProcessExecutorTimeout(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	process, err := NewProcessExecutor().Start(ctx, adapter.CommandSpec{
		Executable: "/bin/sh",
		Args:       []string{"-c", "exec /bin/sleep 5"},
		Dir:        t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := process.Wait()
	if !domain.HasCode(result.Err, domain.ErrorTimeout) {
		t.Fatalf("timeout result = %+v", result)
	}
}

func TestProcessExecutorRejectsInvalidSpec(t *testing.T) {
	executor := NewProcessExecutor()
	for _, spec := range []adapter.CommandSpec{
		{Executable: "sh", Dir: t.TempDir()},
		{Executable: "/bin/sh", Dir: "."},
		{Executable: "/bin/sh", Dir: t.TempDir(), Environment: map[string]adapter.EnvironmentValue{"BAD=KEY": {Value: "x"}}},
	} {
		if _, err := executor.Start(context.Background(), spec); !domain.HasCode(err, domain.ErrorInvalidRequest) {
			t.Fatalf("spec %+v error = %v", spec.Redacted(), err)
		}
	}
}

func TestProcessExecutorWorkspacePolicyRejectsDirectoryOutsideAllowedRoots(t *testing.T) {
	root := t.TempDir()
	policy, err := workspacepolicy.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if os.PathSeparator != '\\' {
		executable = "/bin/sh"
	}
	_, err = NewProcessExecutor(WithWorkspacePolicy(policy)).Start(context.Background(), adapter.CommandSpec{
		Executable: executable,
		Dir:        t.TempDir(),
	})
	if !domain.HasCode(err, domain.ErrorForbidden) {
		t.Fatalf("err=%v", err)
	}
}
