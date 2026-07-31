package adapter

import (
	"context"
	"strconv"
	"strings"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

const (
	ClaudeProviderID             = "claude-code"
	ClaudeAdapterVersion         = "1.0.0"
	ClaudeMinimumProviderVersion = "2.1.146"
)

type ClaudeConfig struct {
	Enabled                    bool
	MinimumVersion             string
	Model                      string
	PermissionMode             string
	MaxTurns                   int
	DangerouslySkipPermissions bool
	AllowedEnvironment         []string
	SecretEnvironment          []string
}

type ClaudeAdapter struct {
	config     ClaudeConfig
	allowedEnv map[string]struct{}
	secretEnv  map[string]struct{}
	states     providerStateStore
}

func NewClaudeAdapter(config ClaudeConfig) *ClaudeAdapter {
	allowed, secret := environmentSets(config.AllowedEnvironment, append(config.SecretEnvironment,
		"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CONFIG_DIR"))
	return &ClaudeAdapter{config: config, allowedEnv: allowed, secretEnv: secret}
}

func (a *ClaudeAdapter) ProviderID() string     { return ClaudeProviderID }
func (a *ClaudeAdapter) AdapterVersion() string { return ClaudeAdapterVersion }
func (a *ClaudeAdapter) MinimumProviderVersion() string {
	if a != nil && strings.TrimSpace(a.config.MinimumVersion) != "" {
		return strings.TrimSpace(a.config.MinimumVersion)
	}
	return ClaudeMinimumProviderVersion
}
func (a *ClaudeAdapter) Enabled() bool { return a != nil && a.config.Enabled }
func (a *ClaudeAdapter) Capabilities() Capabilities {
	return Capabilities{SupportsNonInteractive: true, SupportsResume: true, StructuredOutput: true, Attachments: false, Cancellation: true}
}

func (a *ClaudeAdapter) BuildCommand(ctx context.Context, input ExecutionInput) (CommandSpec, error) {
	if err := ctx.Err(); err != nil {
		return CommandSpec{}, err
	}
	if !a.Enabled() {
		return CommandSpec{}, domain.NewDomainError(domain.ErrorDisabled, "Claude Code One-shot adapter is disabled", nil)
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
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if model := strings.TrimSpace(a.config.Model); model != "" {
		args = append(args, "--model", model)
	}
	if mode := strings.TrimSpace(a.config.PermissionMode); mode != "" {
		switch mode {
		case "default", "acceptEdits", "plan", "bypassPermissions":
		default:
			return CommandSpec{}, domain.InvalidRequestf("unsupported Claude permission mode %q", mode)
		}
		args = append(args, "--permission-mode", mode)
	}
	if a.config.MaxTurns < 0 {
		return CommandSpec{}, domain.InvalidRequestf("Claude max_turns must not be negative")
	}
	if a.config.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(a.config.MaxTurns))
	}
	if a.config.DangerouslySkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	if input.Delivery.Operation == domain.DeliveryContinue {
		if input.RuntimeContext == nil || strings.TrimSpace(input.RuntimeContext.ProviderContextID) == "" {
			return CommandSpec{}, domain.NewDomainError(domain.ErrorContextNotFound, "Claude resume requires RuntimeContext", nil)
		}
		args = append(args, "--resume", input.RuntimeContext.ProviderContextID)
	}
	return CommandSpec{Args: args, Dir: workspace, Environment: environment, Stdin: []byte(prompt)}, nil
}

func (a *ClaudeAdapter) NormalizeOutput(ctx context.Context, chunk OutputChunk) ([]NormalizedOutputEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return decodeJSONLines(&a.states, chunk, "claude", func(payload map[string]any, state *providerRunState) (string, map[string]any) {
		typeName, _ := payload["type"].(string)
		if sessionID, _ := payload["session_id"].(string); strings.TrimSpace(sessionID) != "" {
			state.providerContextID = strings.TrimSpace(sessionID)
		}
		if isError, _ := payload["is_error"].(bool); isError {
			if result, _ := payload["result"].(string); result != "" {
				state.errorText.WriteString(result)
				state.errorText.WriteByte('\n')
			}
		}
		if typeName == "" {
			return "claude.unknown", payload
		}
		return "claude." + typeName, payload
	})
}

func (a *ClaudeAdapter) RuntimeContextEvidence(ctx context.Context, runID string) (RuntimeContextEvidence, bool, error) {
	return contextEvidence(ctx, &a.states, runID)
}
func (a *ClaudeAdapter) ForgetRun(runID string) { a.states.forget(runID) }
func (a *ClaudeAdapter) InterpretResult(ctx context.Context, runID string, result ExecutionResult) ExecutionResult {
	if err := ctx.Err(); err != nil && result.Err == nil {
		result.Err = err
		return result
	}
	return classifyProviderFailure("Claude Code", a.states.isResume(runID), a.states.errorText(runID), result)
}
