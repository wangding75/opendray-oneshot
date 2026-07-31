package adapter

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

func providerTestInput(provider string, operation domain.DeliveryOperation) ExecutionInput {
	now := time.Now().UTC()
	owner := domain.Owner{Kind: domain.PrincipalAdmin, ID: "user-1"}
	task, _ := domain.NewTask(domain.TaskArgs{Owner: owner, ProjectID: "project-1", ProviderID: provider, Source: domain.Source{Kind: domain.SourceAPI}, Prompt: "initial prompt"}, now)
	delivery, _ := domain.NewDelivery(domain.DeliveryArgs{TaskID: task.Snapshot().ID, Operation: operation, RequestedBy: owner, Input: domain.DeliveryInput{PromptDelta: map[bool]string{true: "follow up"}[operation == domain.DeliveryContinue], Options: map[string]any{"workspace_path": "/tmp/workspace"}}, IdempotencyKey: "key", PayloadSHA256: strings.Repeat("a", 64), MaxAttempts: 3}, now)
	return ExecutionInput{Task: task.Snapshot(), Delivery: delivery.Snapshot(), Run: domain.RunSnapshot{ID: domain.NewRunID()}, Prompt: task.Snapshot().Prompt, Environment: map[string]string{"OPENAI_API_KEY": "secret"}}
}

func TestCodexBuildCommandGolden(t *testing.T) {
	input := providerTestInput(CodexProviderID, domain.DeliveryNew)
	adapter := NewCodexAdapter(CodexConfig{Enabled: true, Model: "gpt-5.3-codex", ReasoningEffort: "high", Sandbox: "workspace-write", SkipGitRepoCheck: true})
	spec, err := adapter.BuildCommand(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"exec", "--json", "--color", "never", "--skip-git-repo-check", "--model", "gpt-5.3-codex", "-c", `model_reasoning_effort="high"`, "--sandbox", "workspace-write", "-C", "/tmp/workspace", "-"}
	if !reflect.DeepEqual(spec.Args, want) {
		t.Fatalf("args\n got: %#v\nwant: %#v", spec.Args, want)
	}
	if string(spec.Stdin) != "initial prompt" || spec.Dir != "/tmp/workspace" {
		t.Fatalf("spec = %+v stdin=%q", spec.Redacted(), string(spec.Stdin))
	}
	if spec.Redacted().Environment["OPENAI_API_KEY"] != "[REDACTED]" {
		t.Fatalf("secret leaked: %+v", spec.Redacted())
	}
}

func TestCodexResumeGoldenAndWorkspaceGuard(t *testing.T) {
	input := providerTestInput(CodexProviderID, domain.DeliveryContinue)
	ctx, _ := domain.NewRuntimeContext(domain.RuntimeContextArgs{Owner: domain.Owner{Kind: domain.PrincipalAdmin, ID: "user-1"}, ProjectID: "project-1", ProviderID: CodexProviderID, ProviderContextID: "thread-123", WorkspacePath: "/tmp/workspace"}, time.Now().UTC())
	snapshot := ctx.Snapshot()
	input.RuntimeContext = &snapshot
	adapter := NewCodexAdapter(CodexConfig{Enabled: true})
	spec, err := adapter.BuildCommand(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"exec", "--json", "--color", "never", "-C", "/tmp/workspace", "resume", "thread-123", "-"}
	if !reflect.DeepEqual(spec.Args, want) || string(spec.Stdin) != "follow up" {
		t.Fatalf("resume spec = %#v stdin=%q", spec.Args, string(spec.Stdin))
	}
	input.Delivery.Input.Options["workspace_path"] = "/tmp/other"
	if _, err := adapter.BuildCommand(context.Background(), input); !domain.HasCode(err, domain.ErrorContextOwnerMismatch) {
		t.Fatalf("workspace error = %v", err)
	}
}

func TestCodexJSONLContextAndUnknownOutput(t *testing.T) {
	adapter := NewCodexAdapter(CodexConfig{Enabled: true})
	first := "{\"type\":\"thread.started\",\"thread_id\":\"thread-abc\"}\n{\"type\":\"item."
	second := "completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"done\"}}\nnot-json\n"
	events1, err := adapter.NormalizeOutput(context.Background(), outputChunk("run-1", first))
	if err != nil {
		t.Fatal(err)
	}
	events2, err := adapter.NormalizeOutput(context.Background(), outputChunk("run-1", second))
	if err != nil {
		t.Fatal(err)
	}
	if len(events1) != 1 || events1[0].Type != "codex.thread.started" {
		t.Fatalf("events1=%+v", events1)
	}
	if len(events2) != 2 || events2[0].Type != "codex.item.completed" || events2[1].Type != "codex.unparsed" {
		t.Fatalf("events2=%+v", events2)
	}
	evidence, ok, err := adapter.RuntimeContextEvidence(context.Background(), "run-1")
	if err != nil || !ok || evidence.ProviderContextID != "thread-abc" {
		t.Fatalf("evidence=%+v ok=%v err=%v", evidence, ok, err)
	}
}

func TestCodexFailureClassification(t *testing.T) {
	adapter := NewCodexAdapter(CodexConfig{Enabled: true})
	_, _ = adapter.NormalizeOutput(context.Background(), outputChunk("run-auth", "{\"type\":\"error\",\"message\":\"authentication required\"}\n"))
	result := adapter.InterpretResult(context.Background(), "run-auth", ExecutionResult{ExitCode: 1, Err: context.Canceled})
	if !domain.HasCode(result.Err, domain.ErrorUnauthorized) {
		t.Fatalf("result err=%v", result.Err)
	}
}

func outputChunk(runID, text string) OutputChunk {
	now := time.Now().UTC()
	return OutputChunk{RunID: runID, Sequence: 1, Stream: domain.StreamStdout, StreamRecordID: "osr_00000000000000000000000001", RawArtifactID: "oar_00000000000000000000000001", DecodeStatus: domain.DecodeValidUTF8, Text: &text, ReceivedAt: now}
}

func TestCodexFixtureAndResumeFailureMapping(t *testing.T) {
	fixture, err := os.ReadFile("../testdata/codex/success.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewCodexAdapter(CodexConfig{Enabled: true})
	input := providerTestInput(CodexProviderID, domain.DeliveryNew)
	if _, err := adapter.BuildCommand(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	text := string(fixture)
	events, err := adapter.NormalizeOutput(context.Background(), outputChunk(input.Run.ID, text))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 || events[0].Type != "codex.thread.started" || events[3].Type != "codex.turn.completed" {
		t.Fatalf("events=%+v", events)
	}
	evidence, ok, err := adapter.RuntimeContextEvidence(context.Background(), input.Run.ID)
	if err != nil || !ok || evidence.ProviderContextID != "thread-fixture-001" {
		t.Fatalf("evidence=%+v ok=%v err=%v", evidence, ok, err)
	}

	resumeInput := providerTestInput(CodexProviderID, domain.DeliveryContinue)
	contextAggregate, err := domain.NewRuntimeContext(domain.RuntimeContextArgs{
		Owner: domain.Owner{Kind: domain.PrincipalAdmin, ID: "user-1"}, ProjectID: "project-1",
		ProviderID: CodexProviderID, ProviderContextID: "missing-thread", WorkspacePath: "/tmp/workspace",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := contextAggregate.Snapshot()
	resumeInput.RuntimeContext = &snapshot
	if _, err := adapter.BuildCommand(context.Background(), resumeInput); err != nil {
		t.Fatal(err)
	}
	errorText := "resume thread not found\n"
	_, _ = adapter.NormalizeOutput(context.Background(), outputChunk(resumeInput.Run.ID, errorText))
	result := adapter.InterpretResult(context.Background(), resumeInput.Run.ID, ExecutionResult{ExitCode: 1})
	if !domain.HasCode(result.Err, domain.ErrorResumeFailed) {
		t.Fatalf("resume err=%v", result.Err)
	}
}

func TestCodexFailureFixtures(t *testing.T) {
	for _, test := range []struct {
		name string
		file string
		code domain.ErrorCode
	}{
		{name: "auth", file: "auth-error.jsonl", code: domain.ErrorUnauthorized},
		{name: "rate-limit", file: "rate-limit.jsonl", code: domain.ErrorRateLimited},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := os.ReadFile("../testdata/codex/" + test.file)
			if err != nil {
				t.Fatal(err)
			}
			provider := NewCodexAdapter(CodexConfig{Enabled: true})
			runID := domain.NewRunID()
			_, _ = provider.NormalizeOutput(context.Background(), outputChunk(runID, string(raw)))
			result := provider.InterpretResult(context.Background(), runID, ExecutionResult{ExitCode: 1})
			if !domain.HasCode(result.Err, test.code) {
				t.Fatalf("err=%v want=%s", result.Err, test.code)
			}
		})
	}
}
