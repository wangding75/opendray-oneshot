package adapter

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

type registryTestAdapter struct {
	id           string
	adapterVer   string
	minimumVer   string
	enabled      bool
	capabilities Capabilities
}

func (a registryTestAdapter) ProviderID() string             { return a.id }
func (a registryTestAdapter) AdapterVersion() string         { return a.adapterVer }
func (a registryTestAdapter) MinimumProviderVersion() string { return a.minimumVer }
func (a registryTestAdapter) Enabled() bool                  { return a.enabled }
func (a registryTestAdapter) Capabilities() Capabilities     { return a.capabilities }
func (a registryTestAdapter) DefaultModel() string           { return "default-model" }
func (a registryTestAdapter) BuildCommand(context.Context, ExecutionInput) (CommandSpec, error) {
	return CommandSpec{}, nil
}
func (a registryTestAdapter) NormalizeOutput(context.Context, OutputChunk) ([]NormalizedOutputEvent, error) {
	return nil, nil
}

type registryTestCatalog struct {
	metadata map[string]ProviderMetadata
	err      error
}

func (c registryTestCatalog) OneShotProvider(_ context.Context, id string) (ProviderMetadata, error) {
	if c.err != nil {
		return ProviderMetadata{}, c.err
	}
	metadata, ok := c.metadata[id]
	if !ok {
		return ProviderMetadata{}, errors.New("provider metadata missing")
	}
	return cloneProviderMetadata(metadata), nil
}

type registryTestCredentials struct {
	mu       sync.Mutex
	requests []CredentialRequest
	released []string
	lease    CredentialLease
	err      error
}

func (c *registryTestCredentials) Acquire(_ context.Context, request CredentialRequest) (CredentialLease, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, request)
	if c.err != nil {
		return CredentialLease{}, c.err
	}
	return cloneCredentialLease(c.lease), nil
}

func (c *registryTestCredentials) Release(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.released = append(c.released, id)
	return c.err
}

func requireRegistryCode(t *testing.T, err error, code domain.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s", code)
	}
	actual, ok := domain.CodeOf(err)
	if !ok || actual != code {
		t.Fatalf("error = %v, code=%q, want=%q", err, actual, code)
	}
}

func capableRegistryAdapter(id string) registryTestAdapter {
	return registryTestAdapter{
		id: id, adapterVer: "2.0.0", minimumVer: "1.5.0", enabled: true,
		capabilities: Capabilities{
			SupportsNonInteractive: true,
			SupportsResume:         true,
			StructuredOutput:       true,
			Attachments:            true,
			Cancellation:           true,
		},
	}
}

func TestRegistryRegisterResolveAndExposeCapabilities(t *testing.T) {
	item := capableRegistryAdapter("provider-a")
	catalog := registryTestCatalog{metadata: map[string]ProviderMetadata{
		"provider-a": {
			ID: "provider-a", DisplayName: "Provider A", Version: "1.8.3",
			Executable: "/usr/bin/provider-a", Enabled: true,
			Environment: map[string]EnvironmentValue{"SHARED": {Value: "yes"}},
		},
	}}
	registry, err := NewConfiguredRegistry(true, catalog, nil, item)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := registry.ResolveProvider(context.Background(), "provider-a")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Metadata.Executable != "/usr/bin/provider-a" || !reflect.DeepEqual(resolved.Capabilities, item.capabilities) {
		t.Fatalf("resolved = %+v", resolved)
	}
	descriptor, err := registry.Describe(context.Background(), "provider-a")
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.ProviderID != "provider-a" || descriptor.ProviderVersion != "1.8.3" || !descriptor.Capabilities.SupportsResume {
		t.Fatalf("descriptor = %+v", descriptor)
	}
	listed, err := registry.ListCapabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0] != descriptor {
		t.Fatalf("listed = %+v", listed)
	}
}

func TestRegistryRejectsDuplicateUnknownUnsupportedAndDisabled(t *testing.T) {
	item := capableRegistryAdapter("provider-a")
	registry := NewRegistry(item)
	requireRegistryCode(t, registry.Register(item), domain.ErrorRunConflict)
	requireRegistryCode(t, func() error { _, err := registry.Resolve("missing"); return err }(), domain.ErrorUnsupportedProvider)

	unsupported := item
	unsupported.id = "interactive-only"
	unsupported.capabilities.SupportsNonInteractive = false
	unsupportedRegistry := NewRegistry(unsupported)
	requireRegistryCode(t, func() error { _, err := unsupportedRegistry.Resolve(unsupported.id); return err }(), domain.ErrorUnsupportedProvider)

	disabledAdapter := item
	disabledAdapter.id = "disabled-adapter"
	disabledAdapter.enabled = false
	disabledRegistry := NewRegistry(disabledAdapter)
	requireRegistryCode(t, func() error { _, err := disabledRegistry.Resolve(disabledAdapter.id); return err }(), domain.ErrorDisabled)

	globallyDisabled, err := NewConfiguredRegistry(false, nil, nil, item)
	if err != nil {
		t.Fatal(err)
	}
	requireRegistryCode(t, func() error { _, err := globallyDisabled.Resolve(item.id); return err }(), domain.ErrorDisabled)
}

func TestRegistryRejectsProviderDisabledAndVersionMismatch(t *testing.T) {
	item := capableRegistryAdapter("provider-a")
	for _, test := range []struct {
		name     string
		metadata ProviderMetadata
		code     domain.ErrorCode
	}{
		{name: "provider-disabled", metadata: ProviderMetadata{ID: item.id, Version: "2.0.0", Enabled: false}, code: domain.ErrorDisabled},
		{name: "version-too-old", metadata: ProviderMetadata{ID: item.id, Version: "1.4.9", Enabled: true}, code: domain.ErrorUnsupportedProvider},
		{name: "identity-mismatch", metadata: ProviderMetadata{ID: "other", Version: "2.0.0", Enabled: true}, code: domain.ErrorProviderUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry, err := NewConfiguredRegistry(true, registryTestCatalog{metadata: map[string]ProviderMetadata{item.id: test.metadata}}, nil, item)
			if err != nil {
				t.Fatal(err)
			}
			_, err = registry.ResolveProvider(context.Background(), item.id)
			requireRegistryCode(t, err, test.code)
		})
	}
}

func TestRegistryCredentialAllocationUsesNarrowBoundary(t *testing.T) {
	credentials := &registryTestCredentials{lease: CredentialLease{
		ID: "credential-lease-1",
		Environment: map[string]EnvironmentValue{
			"TOKEN": {Value: "secret", Secret: true},
		},
	}}
	registry, err := NewConfiguredRegistry(true, nil, credentials, capableRegistryAdapter("provider-a"))
	if err != nil {
		t.Fatal(err)
	}
	request := CredentialRequest{
		ProviderID: "provider-a", ProjectID: "project-1",
		Owner: domain.Owner{Kind: domain.PrincipalAdmin, ID: "owner-1"}, RunID: "run-1",
	}
	lease, err := registry.AcquireCredential(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if lease.ID != "credential-lease-1" || lease.Environment["TOKEN"].Value != "secret" {
		t.Fatalf("lease = %+v", lease)
	}
	lease.Environment["TOKEN"] = EnvironmentValue{Value: "mutated"}
	if credentials.lease.Environment["TOKEN"].Value != "secret" {
		t.Fatal("credential lease was not defensively copied")
	}
	if err := registry.ReleaseCredential(context.Background(), lease.ID); err != nil {
		t.Fatal(err)
	}
	credentials.mu.Lock()
	defer credentials.mu.Unlock()
	if len(credentials.requests) != 1 || credentials.requests[0] != request {
		t.Fatalf("requests = %+v", credentials.requests)
	}
	if !reflect.DeepEqual(credentials.released, []string{"credential-lease-1"}) {
		t.Fatalf("released = %+v", credentials.released)
	}
}

func TestCompareVersionsAcceptsRealProviderVersionOutput(t *testing.T) {
	for _, test := range []struct {
		actual  string
		minimum string
		want    int
	}{
		{actual: "codex-cli 0.132.0", minimum: "0.132.0", want: 0},
		{actual: "codex-cli 0.140.0", minimum: "0.132.0", want: 1},
		{actual: "2.1.146 (Claude Code)", minimum: "2.1.146", want: 0},
		{actual: "Claude Code v2.1.145", minimum: "2.1.146", want: -1},
		{actual: "codex-cli 0.132.0-beta.1", minimum: "0.132.0", want: -1},
		{actual: "codex-cli 0.132.0", minimum: "0.132.0-beta.1", want: 1},
		{actual: "broken version", minimum: "1.0.0", want: -1},
	} {
		got := compareVersions(test.actual, test.minimum)
		if got < 0 {
			got = -1
		} else if got > 0 {
			got = 1
		}
		if got != test.want {
			t.Errorf("compareVersions(%q,%q)=%d want=%d", test.actual, test.minimum, got, test.want)
		}
	}
}

func TestProviderAdaptersExposeTestedMinimumVersionsAndAllowOverride(t *testing.T) {
	codex := NewCodexAdapter(CodexConfig{Enabled: true})
	if got := codex.MinimumProviderVersion(); got != CodexMinimumProviderVersion || got == "0.0.0" {
		t.Fatalf("Codex minimum version=%q", got)
	}
	claude := NewClaudeAdapter(ClaudeConfig{Enabled: true})
	if got := claude.MinimumProviderVersion(); got != ClaudeMinimumProviderVersion || got == "0.0.0" {
		t.Fatalf("Claude minimum version=%q", got)
	}
	if got := NewCodexAdapter(CodexConfig{Enabled: true, MinimumVersion: "9.8.7"}).MinimumProviderVersion(); got != "9.8.7" {
		t.Fatalf("Codex minimum override=%q", got)
	}
	if got := NewClaudeAdapter(ClaudeConfig{Enabled: true, MinimumVersion: "8.7.6"}).MinimumProviderVersion(); got != "8.7.6" {
		t.Fatalf("Claude minimum override=%q", got)
	}
}
