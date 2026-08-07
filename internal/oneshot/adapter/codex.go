package adapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

const (
	CodexProviderID             = "codex"
	CodexAdapterVersion         = "1.0.0"
	CodexMinimumProviderVersion = "0.132.0"
)

// CodexConfig freezes the supported non-interactive Codex CLI surface. The
// executable itself comes from ProviderCatalog so account and installation
// discovery remain shared with Interactive mode without sharing PTY behavior.
type CodexConfig struct {
	Enabled            bool
	MinimumVersion     string
	Model              string
	ReasoningEffort    string
	Sandbox            string
	SkipGitRepoCheck   bool
	AllowedEnvironment []string
	SecretEnvironment  []string
}

type CodexAdapter struct {
	config     CodexConfig
	allowedEnv map[string]struct{}
	secretEnv  map[string]struct{}
	states     providerStateStore
}

func NewCodexAdapter(config CodexConfig) *CodexAdapter {
	allowed, secret := environmentSets(config.AllowedEnvironment, append(config.SecretEnvironment,
		"OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_HOME"))
	return &CodexAdapter{config: config, allowedEnv: allowed, secretEnv: secret}
}

func (a *CodexAdapter) ProviderID() string     { return CodexProviderID }
func (a *CodexAdapter) AdapterVersion() string { return CodexAdapterVersion }
func (a *CodexAdapter) MinimumProviderVersion() string {
	if a != nil && strings.TrimSpace(a.config.MinimumVersion) != "" {
		return strings.TrimSpace(a.config.MinimumVersion)
	}
	return CodexMinimumProviderVersion
}
func (a *CodexAdapter) Enabled() bool { return a != nil && a.config.Enabled }
func (a *CodexAdapter) Capabilities() Capabilities {
	return Capabilities{SupportsNonInteractive: true, SupportsResume: true, StructuredOutput: true, Attachments: false, Cancellation: true}
}

func (a *CodexAdapter) BuildCommand(ctx context.Context, input ExecutionInput) (CommandSpec, error) {
	if err := ctx.Err(); err != nil {
		return CommandSpec{}, err
	}
	if !a.Enabled() {
		return CommandSpec{}, domain.NewDomainError(domain.ErrorDisabled, "Codex One-shot adapter is disabled", nil)
	}
	workspace, err := providerWorkspace(input)
	if err != nil {
		return CommandSpec{}, err
	}
	prompt, err := providerPrompt(input)
	if err != nil {
		return CommandSpec{}, err
	}
	environment, err := buildProviderEnvironment(input.Environment, a.allowedEnv, a.secretEnv)
	if err != nil {
		return CommandSpec{}, err
	}
	a.states.prepare(input.Run.ID, input.Delivery.Operation == domain.DeliveryContinue)

	args := []string{"exec", "--json", "--color", "never"}
	if a.config.SkipGitRepoCheck {
		args = append(args, "--skip-git-repo-check")
	}
	if model := strings.TrimSpace(input.Run.Model); model != "" {
		args = append(args, "--model", model)
	} else {
		return CommandSpec{}, domain.NewDomainError(domain.ErrorInvalidRequest, "Codex execution requires a model snapshot", nil)
	}
	if effort := strings.TrimSpace(a.config.ReasoningEffort); effort != "" {
		switch effort {
		case "none", "minimal", "low", "medium", "high", "xhigh":
		default:
			return CommandSpec{}, domain.InvalidRequestf("unsupported Codex reasoning effort %q", effort)
		}
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=\"%s\"", effort))
	}
	if sandbox := strings.TrimSpace(a.config.Sandbox); sandbox != "" {
		switch sandbox {
		case "read-only", "workspace-write", "danger-full-access":
		default:
			return CommandSpec{}, domain.InvalidRequestf("unsupported Codex sandbox %q", sandbox)
		}
		args = append(args, "--sandbox", sandbox)
	}
	args = append(args, "-C", workspace)
	if input.Delivery.Operation == domain.DeliveryContinue {
		if input.RuntimeContext == nil || strings.TrimSpace(input.RuntimeContext.ProviderContextID) == "" {
			return CommandSpec{}, domain.NewDomainError(domain.ErrorContextNotFound, "Codex resume requires RuntimeContext", nil)
		}
		args = append(args, "resume", input.RuntimeContext.ProviderContextID, "-")
	} else {
		args = append(args, "-")
	}
	return CommandSpec{Args: args, Dir: workspace, Environment: environment, Stdin: []byte(prompt)}, nil
}

func (a *CodexAdapter) NormalizeOutput(ctx context.Context, chunk OutputChunk) ([]NormalizedOutputEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return decodeJSONLines(&a.states, chunk, "codex", func(payload map[string]any, state *providerRunState) (string, map[string]any) {
		typeName, _ := payload["type"].(string)
		if threadID, _ := payload["thread_id"].(string); strings.TrimSpace(threadID) != "" {
			state.providerContextID = strings.TrimSpace(threadID)
		}
		if sessionID, _ := payload["session_id"].(string); strings.TrimSpace(sessionID) != "" && state.providerContextID == "" {
			state.providerContextID = strings.TrimSpace(sessionID)
		}
		if typeName == "error" || strings.Contains(typeName, "failed") {
			if message, _ := payload["message"].(string); message != "" {
				state.errorText.WriteString(message)
				state.errorText.WriteByte('\n')
			}
		}
		if typeName == "" {
			return "codex.unknown", payload
		}
		return "codex." + typeName, payload
	})
}

func (a *CodexAdapter) DefaultModel() string {
	if a == nil {
		return ""
	}
	return a.config.Model
}

func (a *CodexAdapter) RuntimeContextEvidence(ctx context.Context, runID string) (RuntimeContextEvidence, bool, error) {
	return contextEvidence(ctx, &a.states, runID)
}
func (a *CodexAdapter) ForgetRun(runID string) { a.states.forget(runID) }
func (a *CodexAdapter) InterpretResult(ctx context.Context, runID string, result ExecutionResult) ExecutionResult {
	if err := ctx.Err(); err != nil && result.Err == nil {
		result.Err = err
		return result
	}
	return classifyProviderFailure("Codex", a.states.isResume(runID), a.states.errorText(runID), result)
}
