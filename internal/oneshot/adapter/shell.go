package adapter

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

const (
	ShellProviderID     = "shell-oneshot-fixture"
	ShellAdapterVersion = "1.0.0"
)

// ShellConfig defines a test-only command allowlist. Production construction
// must leave Enabled false unless an explicit test harness enables it.
type ShellConfig struct {
	Enabled            bool
	Commands           map[string]CommandSpec
	AllowedEnvironment []string
	SecretEnvironment  []string
}

// ShellAdapter is a deterministic fixture adapter. It never evaluates Prompt
// as shell source; CommandName must match a pre-registered command.
type ShellAdapter struct {
	enabled    bool
	commands   map[string]CommandSpec
	allowedEnv map[string]struct{}
	secretEnv  map[string]struct{}
}

func NewShellAdapter(config ShellConfig) *ShellAdapter {
	adapter := &ShellAdapter{
		enabled:    config.Enabled,
		commands:   make(map[string]CommandSpec, len(config.Commands)),
		allowedEnv: make(map[string]struct{}, len(config.AllowedEnvironment)),
		secretEnv:  make(map[string]struct{}, len(config.SecretEnvironment)),
	}
	for name, command := range config.Commands {
		adapter.commands[strings.TrimSpace(name)] = cloneCommandSpec(command)
	}
	for _, key := range config.AllowedEnvironment {
		key = strings.TrimSpace(key)
		if key != "" {
			adapter.allowedEnv[key] = struct{}{}
		}
	}
	for _, key := range config.SecretEnvironment {
		key = strings.TrimSpace(key)
		if key != "" {
			adapter.secretEnv[key] = struct{}{}
			adapter.allowedEnv[key] = struct{}{}
		}
	}
	return adapter
}

func (a *ShellAdapter) ProviderID() string             { return ShellProviderID }
func (a *ShellAdapter) AdapterVersion() string         { return ShellAdapterVersion }
func (a *ShellAdapter) MinimumProviderVersion() string { return "0.0.0" }
func (a *ShellAdapter) Enabled() bool                  { return a != nil && a.enabled }
func (a *ShellAdapter) Capabilities() Capabilities {
	return Capabilities{
		SupportsNonInteractive: true,
		SupportsResume:         false,
		StructuredOutput:       false,
		Attachments:            false,
		Cancellation:           true,
	}
}

func (a *ShellAdapter) BuildCommand(ctx context.Context, input ExecutionInput) (CommandSpec, error) {
	if err := ctx.Err(); err != nil {
		return CommandSpec{}, err
	}
	if !a.Enabled() {
		return CommandSpec{}, domain.NewDomainError(domain.ErrorDisabled, "test Shell One-shot adapter is disabled", nil)
	}
	if input.Run.Model != "" && input.Run.Model != "shell" {
		return CommandSpec{}, domain.NewDomainError(domain.ErrorInvalidRequest, "Shell fixture does not support model selection: "+input.Run.Model, nil)
	}
	name := strings.TrimSpace(input.CommandName)
	command, ok := a.commands[name]
	if !ok || name == "" {
		return CommandSpec{}, domain.NewDomainError(domain.ErrorInvalidRequest, "Shell One-shot command is not in the test allowlist", nil)
	}
	if !filepath.IsAbs(command.Executable) {
		return CommandSpec{}, domain.InvalidRequestf("Shell fixture executable must be absolute")
	}
	if !filepath.IsAbs(command.Dir) {
		return CommandSpec{}, domain.InvalidRequestf("Shell fixture cwd must be absolute")
	}

	out := cloneCommandSpec(command)
	if out.Environment == nil {
		out.Environment = make(map[string]EnvironmentValue)
	}
	keys := make([]string, 0, len(input.Environment))
	for key := range input.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, allowed := a.allowedEnv[key]; !allowed {
			return CommandSpec{}, domain.InvalidRequestf("environment variable %q is not allowed", key)
		}
		_, secret := a.secretEnv[key]
		out.Environment[key] = EnvironmentValue{Value: input.Environment[key], Secret: secret}
	}
	return out, nil
}

func cloneCommandSpec(input CommandSpec) CommandSpec {
	out := CommandSpec{
		Executable: input.Executable,
		Args:       append([]string(nil), input.Args...),
		Dir:        input.Dir,
		Stdin:      append([]byte(nil), input.Stdin...),
	}
	if input.Environment != nil {
		out.Environment = make(map[string]EnvironmentValue, len(input.Environment))
		for key, value := range input.Environment {
			out.Environment[key] = value
		}
	}
	return out
}

// NormalizeOutput emits a deterministic passthrough event for each persisted
// Shell fixture stream chunk. It never replaces the raw StreamRecord/Artifact.
func (a *ShellAdapter) NormalizeOutput(ctx context.Context, chunk OutputChunk) ([]NormalizedOutputEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	content := map[string]any{
		"stream":           chunk.Stream.String(),
		"stream_sequence":  chunk.Sequence,
		"stream_record_id": chunk.StreamRecordID,
		"raw_artifact_id":  chunk.RawArtifactID,
		"byte_offset":      chunk.ByteOffset,
		"byte_length":      chunk.ByteLength,
		"decode_status":    chunk.DecodeStatus.String(),
		"sha256":           chunk.SHA256,
	}
	if chunk.Text != nil {
		content["text"] = *chunk.Text
	}
	return []NormalizedOutputEvent{{
		Type: "shell.passthrough", Content: content, OccurredAt: chunk.ReceivedAt,
	}}, nil
}
func (a *ShellAdapter) DefaultModel() string {
	return "shell"
}
