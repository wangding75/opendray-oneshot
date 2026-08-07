package adapter

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

func TestClaudeBuildCommandGolden(t *testing.T) {
	input := providerTestInput(ClaudeProviderID, domain.DeliveryNew)
	claudeConfigDir := "/tmp/claude-account"
	if filepath.Separator == '\\' {
		claudeConfigDir = `C:\tmp\claude-account`
	}
	input.Environment = map[string]string{"CLAUDE_CONFIG_DIR": claudeConfigDir}
	input.Run.Model = "claude-opus-4-1"
	adapter := NewClaudeAdapter(ClaudeConfig{Enabled: true, Model: "ignored-model", PermissionMode: "plan", MaxTurns: 12})
	spec, err := adapter.BuildCommand(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-p", "--output-format", "stream-json", "--verbose", "--model", "claude-opus-4-1", "--permission-mode", "plan", "--max-turns", "12"}
	if !reflect.DeepEqual(spec.Args, want) {
		t.Fatalf("args got=%#v want=%#v", spec.Args, want)
	}
	if string(spec.Stdin) != "initial prompt" || spec.Redacted().Environment["CLAUDE_CONFIG_DIR"] != "[REDACTED]" {
		t.Fatalf("spec=%+v", spec.Redacted())
	}
}

func TestClaudeResumeGolden(t *testing.T) {
	input := providerTestInput(ClaudeProviderID, domain.DeliveryContinue)
	input.Environment = nil
	workspacePath := "/tmp/workspace"
	if filepath.Separator == '\\' {
		workspacePath = `C:\tmp\workspace`
	}
	ctx, _ := domain.NewRuntimeContext(domain.RuntimeContextArgs{Owner: domain.Owner{Kind: domain.PrincipalAdmin, ID: "user-1"}, ProjectID: "project-1", ProviderID: ClaudeProviderID, ProviderContextID: "session-123", WorkspacePath: workspacePath}, time.Now().UTC())
	snapshot := ctx.Snapshot()
	input.RuntimeContext = &snapshot
	adapter := NewClaudeAdapter(ClaudeConfig{Enabled: true})
	spec, err := adapter.BuildCommand(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-p", "--output-format", "stream-json", "--verbose", "--model", "default-model", "--resume", "session-123"}
	if !reflect.DeepEqual(spec.Args, want) || string(spec.Stdin) != "follow up" {
		t.Fatalf("resume=%#v stdin=%q", spec.Args, string(spec.Stdin))
	}
}

func TestClaudeStreamJSONContextAndErrors(t *testing.T) {
	adapter := NewClaudeAdapter(ClaudeConfig{Enabled: true})
	text := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"session-abc"}`,
		`{"type":"assistant","session_id":"session-abc","message":{"role":"assistant"}}`,
		`{"type":"result","subtype":"error","is_error":true,"result":"rate limit exceeded","session_id":"session-abc"}`,
	}, "\n") + "\n"
	events, err := adapter.NormalizeOutput(context.Background(), outputChunk("run-claude", text))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Type != "claude.system" || events[2].Type != "claude.result" {
		t.Fatalf("events=%+v", events)
	}
	evidence, ok, _ := adapter.RuntimeContextEvidence(context.Background(), "run-claude")
	if !ok || evidence.ProviderContextID != "session-abc" {
		t.Fatalf("evidence=%+v ok=%v", evidence, ok)
	}
	result := adapter.InterpretResult(context.Background(), "run-claude", ExecutionResult{ExitCode: 1})
	if !domain.HasCode(result.Err, domain.ErrorRateLimited) {
		t.Fatalf("result err=%v", result.Err)
	}
}

func TestClaudeFixtureAndResumeFailureMapping(t *testing.T) {
	fixture, err := os.ReadFile("../testdata/claude/success.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	provider := NewClaudeAdapter(ClaudeConfig{Enabled: true})
	input := providerTestInput(ClaudeProviderID, domain.DeliveryNew)
	input.Environment = nil
	if _, err := provider.BuildCommand(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	events, err := provider.NormalizeOutput(context.Background(), outputChunk(input.Run.ID, string(fixture)))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Type != "claude.system" || events[2].Type != "claude.result" {
		t.Fatalf("events=%+v", events)
	}
	evidence, ok, err := provider.RuntimeContextEvidence(context.Background(), input.Run.ID)
	if err != nil || !ok || evidence.ProviderContextID != "session-fixture-001" {
		t.Fatalf("evidence=%+v ok=%v err=%v", evidence, ok, err)
	}

	resumeInput := providerTestInput(ClaudeProviderID, domain.DeliveryContinue)
	resumeInput.Environment = nil
	workspacePath := "/tmp/workspace"
	if filepath.Separator == '\\' {
		workspacePath = `C:\tmp\workspace`
	}
	contextAggregate, err := domain.NewRuntimeContext(domain.RuntimeContextArgs{
		Owner: domain.Owner{Kind: domain.PrincipalAdmin, ID: "user-1"}, ProjectID: "project-1",
		ProviderID: ClaudeProviderID, ProviderContextID: "missing-session", WorkspacePath: workspacePath,
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := contextAggregate.Snapshot()
	resumeInput.RuntimeContext = &snapshot
	if _, err := provider.BuildCommand(context.Background(), resumeInput); err != nil {
		t.Fatal(err)
	}
	errorText := "resume session not found\n"
	_, _ = provider.NormalizeOutput(context.Background(), outputChunk(resumeInput.Run.ID, errorText))
	result := provider.InterpretResult(context.Background(), resumeInput.Run.ID, ExecutionResult{ExitCode: 1})
	if !domain.HasCode(result.Err, domain.ErrorResumeFailed) {
		t.Fatalf("resume err=%v", result.Err)
	}
}

func TestClaudeFailureFixtures(t *testing.T) {
	for _, test := range []struct {
		name string
		file string
		code domain.ErrorCode
	}{
		{name: "auth", file: "auth-error.jsonl", code: domain.ErrorUnauthorized},
		{name: "rate-limit", file: "rate-limit.jsonl", code: domain.ErrorRateLimited},
		{name: "permission", file: "permission-error.jsonl", code: domain.ErrorForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := os.ReadFile("../testdata/claude/" + test.file)
			if err != nil {
				t.Fatal(err)
			}
			provider := NewClaudeAdapter(ClaudeConfig{Enabled: true})
			runID := domain.NewRunID()
			_, _ = provider.NormalizeOutput(context.Background(), outputChunk(runID, string(raw)))
			result := provider.InterpretResult(context.Background(), runID, ExecutionResult{ExitCode: 1})
			if !domain.HasCode(result.Err, test.code) {
				t.Fatalf("err=%v want=%s", result.Err, test.code)
			}
		})
	}
}
