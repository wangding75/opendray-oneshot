// Package adapter defines provider-neutral One-shot command preparation.
//
// Adapters translate immutable Task/Delivery input into an ordinary child
// process specification. They must not allocate a PTY or import the
// interactive Session domain.
package adapter

import (
	"context"
	"sort"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

// EnvironmentValue marks whether an environment value must be redacted from
// diagnostics. The raw value is only used to construct the child process.
type EnvironmentValue struct {
	Value  string
	Secret bool
}

// CommandSpec is the complete ordinary-process launch contract.
// Executable and Dir must be absolute. Environment is explicit and does not
// implicitly inherit the gateway process environment.
type CommandSpec struct {
	Executable  string
	Args        []string
	Dir         string
	Environment map[string]EnvironmentValue
	// Stdin carries the prompt outside argv so credentials and user content are
	// not exposed through process listings. It is never included in Redacted().
	Stdin []byte
}

// RedactedCommandSpec is safe for logs, reports, and API diagnostics.
type RedactedCommandSpec struct {
	Executable  string            `json:"executable"`
	Args        []string          `json:"args"`
	Dir         string            `json:"dir"`
	Environment map[string]string `json:"environment"`
}

// Redacted returns a defensive copy without secret values.
func (s CommandSpec) Redacted() RedactedCommandSpec {
	environment := make(map[string]string, len(s.Environment))
	for key, value := range s.Environment {
		if value.Secret {
			environment[key] = "[REDACTED]"
		} else {
			environment[key] = value.Value
		}
	}
	return RedactedCommandSpec{
		Executable:  s.Executable,
		Args:        append([]string(nil), s.Args...),
		Dir:         s.Dir,
		Environment: environment,
	}
}

// ProcessEnvironment returns a deterministic KEY=VALUE list for os/exec.
func (s CommandSpec) ProcessEnvironment() []string {
	keys := make([]string, 0, len(s.Environment))
	for key := range s.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+s.Environment[key].Value)
	}
	return out
}

// ExecutionInput is immutable provider input. CommandName is resolved by the
// adapter's allowlist rather than being interpreted as arbitrary shell text.
type ExecutionInput struct {
	Task           domain.TaskSnapshot
	Delivery       domain.DeliverySnapshot
	Run            domain.RunSnapshot
	RuntimeContext *domain.RuntimeContextSnapshot
	CommandName    string
	Prompt         string
	Environment    map[string]string
}

// OutputChunk is the adapter-facing normalized view of one persisted raw
// StreamRecord. Raw bytes remain authoritative in Artifact storage.
type OutputChunk struct {
	RunID          string
	Sequence       int64
	Stream         domain.StreamKind
	ByteOffset     int64
	ByteLength     int64
	StreamRecordID string
	RawArtifactID  string
	DecodeStatus   domain.DecodeStatus
	Text           *string
	SHA256         string
	ReceivedAt     time.Time
}

// NormalizedOutputEvent is converted into one immutable StandardEvent.
type NormalizedOutputEvent struct {
	Type       string
	Content    map[string]any
	OccurredAt time.Time
}

// ExecutionResult is the ordinary child process lifecycle result.
type ExecutionResult struct {
	PID             int
	ExitCode        int
	StartedAt       time.Time
	FinishedAt      time.Time
	Cancelled       bool
	TimedOut        bool
	CleanupResolved bool
	Err             error
}

// Capabilities are the client-visible One-shot operations supported by one
// adapter. They are independent from the interactive PTY manifest flags.
type Capabilities struct {
	SupportsNonInteractive bool `json:"supports_non_interactive"`
	SupportsResume         bool `json:"supports_resume"`
	StructuredOutput       bool `json:"structured_output"`
	Attachments            bool `json:"attachments"`
	Cancellation           bool `json:"cancellation"`
}

// ProviderMetadata is the small shared provider view consumed by One-shot.
// It intentionally excludes PTY arguments and interactive input rules.
type ProviderMetadata struct {
	ID           string                      `json:"id"`
	DisplayName  string                      `json:"display_name"`
	Version      string                      `json:"version"`
	Executable   string                      `json:"executable"`
	Enabled      bool                        `json:"enabled"`
	DefaultModel string                      `json:"default_model"`
	Environment  map[string]EnvironmentValue `json:"-"`
}

// ProviderCatalog resolves shared CLI path/version/enabled metadata without
// exposing the interactive catalog implementation to the One-shot domain.
type ProviderCatalog interface {
	OneShotProvider(context.Context, string) (ProviderMetadata, error)
}

// CredentialRequest identifies an isolated credential allocation request.
type CredentialRequest struct {
	ProviderID string
	ProjectID  string
	Owner      domain.Owner
	RunID      string
}

// CredentialLease contains only child-process environment and an opaque lease
// identifier. The identifier is persisted so recovery can release it later.
type CredentialLease struct {
	ID          string
	Environment map[string]EnvironmentValue
}

// CredentialAllocator is the narrow shared credential boundary. Implementors
// may use existing provider/account services, but One-shot never imports the
// interactive Session package.
type CredentialAllocator interface {
	Acquire(context.Context, CredentialRequest) (CredentialLease, error)
	Release(context.Context, string) error
}

// RuntimeContextEvidence is provider output required to persist resumable
// continuity after a successful first Run.
type RuntimeContextEvidence struct {
	ProviderContextID string
}

// RuntimeContextExtractor is implemented by adapters that expose a provider
// context/session/thread identifier through structured output. The executor
// calls it only after output has been fully drained.
type RuntimeContextExtractor interface {
	RuntimeContextEvidence(context.Context, string) (RuntimeContextEvidence, bool, error)
	ForgetRun(string)
}

// ResultInterpreter maps structured provider failures to stable One-shot error
// codes after stdout/stderr collection. It must not discard raw output.
type ResultInterpreter interface {
	InterpretResult(context.Context, string, ExecutionResult) ExecutionResult
}

// OneShotAdapter builds a normal child-process command for one provider.
type OneShotAdapter interface {
	ProviderID() string
	AdapterVersion() string
	MinimumProviderVersion() string
	Enabled() bool
	Capabilities() Capabilities
	DefaultModel() string
	BuildCommand(context.Context, ExecutionInput) (CommandSpec, error)
	NormalizeOutput(context.Context, OutputChunk) ([]NormalizedOutputEvent, error)
}
