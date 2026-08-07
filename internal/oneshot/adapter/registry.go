package adapter

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

// ProviderDescriptor is safe to expose through future REST/mobile surfaces.
type ProviderDescriptor struct {
	ProviderID      string       `json:"provider_id"`
	DisplayName     string       `json:"display_name"`
	ProviderVersion string       `json:"provider_version"`
	AdapterVersion  string       `json:"adapter_version"`
	Enabled         bool         `json:"enabled"`
	Capabilities    Capabilities `json:"capabilities"`
}

// ResolvedProvider is the execution-time adapter plus shared metadata.
type ResolvedProvider struct {
	Adapter      OneShotAdapter
	Metadata     ProviderMetadata
	Capabilities Capabilities
}

// Registry resolves provider-specific One-shot adapters without falling back
// to the interactive PTY provider path.
type Registry struct {
	mu          sync.RWMutex
	enabled     bool
	catalog     ProviderCatalog
	credentials CredentialAllocator
	adapters    map[string]OneShotAdapter
	initErr     error
}

// NewRegistry creates an enabled registry. Registration errors remain durable
// and are returned by Resolve/Describe so legacy construction sites stay small.
func NewRegistry(adapters ...OneShotAdapter) *Registry {
	registry := &Registry{enabled: true, adapters: make(map[string]OneShotAdapter, len(adapters))}
	for _, item := range adapters {
		if err := registry.Register(item); err != nil && registry.initErr == nil {
			registry.initErr = err
		}
	}
	return registry
}

// NewConfiguredRegistry wires shared metadata and credentials explicitly.
func NewConfiguredRegistry(enabled bool, catalog ProviderCatalog, credentials CredentialAllocator, adapters ...OneShotAdapter) (*Registry, error) {
	registry := &Registry{
		enabled: enabled, catalog: catalog, credentials: credentials,
		adapters: make(map[string]OneShotAdapter, len(adapters)),
	}
	for _, item := range adapters {
		if err := registry.Register(item); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// Register rejects duplicates instead of silently replacing provider behavior.
func (r *Registry) Register(item OneShotAdapter) error {
	if r == nil {
		return domain.NewDomainError(domain.ErrorProviderUnavailable, "One-shot adapter registry is unavailable", nil)
	}
	if item == nil {
		return domain.InvalidRequestf("One-shot adapter is required")
	}
	id := strings.TrimSpace(item.ProviderID())
	if id == "" || strings.TrimSpace(item.AdapterVersion()) == "" {
		return domain.InvalidRequestf("One-shot adapter provider_id and version are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.adapters[id]; exists {
		return domain.NewDomainError(domain.ErrorRunConflict, "One-shot adapter is already registered", nil)
	}
	r.adapters[id] = item
	return nil
}

// Resolve preserves the original execution lookup while enforcing registry and
// adapter enablement. It does not consult the shared catalog because no context
// is available; new execution code should prefer ResolveProvider.
func (r *Registry) Resolve(providerID string) (OneShotAdapter, error) {
	if r == nil {
		return nil, domain.NewDomainError(domain.ErrorProviderUnavailable, "One-shot adapter registry is unavailable", nil)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.initErr != nil {
		return nil, r.initErr
	}
	if !r.enabled {
		return nil, domain.NewDomainError(domain.ErrorDisabled, "One-shot execution is disabled", nil)
	}
	item, ok := r.adapters[strings.TrimSpace(providerID)]
	if !ok {
		return nil, domain.NewDomainError(domain.ErrorUnsupportedProvider, "provider does not support One-shot execution", nil)
	}
	if !item.Enabled() {
		return nil, domain.NewDomainError(domain.ErrorDisabled, "One-shot provider adapter is disabled", nil)
	}
	if !item.Capabilities().SupportsNonInteractive {
		return nil, domain.NewDomainError(domain.ErrorUnsupportedProvider, "provider does not support non-interactive execution", nil)
	}
	return item, nil
}

// ResolveProvider validates shared provider enablement/version and returns the
// exact capability surface. It never falls back to a PTY adapter.
func (r *Registry) ResolveProvider(ctx context.Context, providerID string) (ResolvedProvider, error) {
	item, err := r.Resolve(providerID)
	if err != nil {
		return ResolvedProvider{}, err
	}
	metadata := ProviderMetadata{
		ID: providerID, DisplayName: providerID, Version: item.AdapterVersion(), Enabled: true,
	}
	if r.catalog != nil {
		metadata, err = r.catalog.OneShotProvider(ctx, providerID)
		if err != nil {
			return ResolvedProvider{}, domain.NewDomainError(domain.ErrorProviderUnavailable, "One-shot provider metadata is unavailable", err)
		}
		if !metadata.Enabled {
			return ResolvedProvider{}, domain.NewDomainError(domain.ErrorDisabled, "provider is disabled", nil)
		}
		if metadata.ID != "" && metadata.ID != providerID {
			return ResolvedProvider{}, domain.NewDomainError(domain.ErrorProviderUnavailable, "provider metadata identity mismatch", nil)
		}
	}
	minimum := strings.TrimSpace(item.MinimumProviderVersion())
	if r.catalog != nil && minimum != "" && compareVersions(metadata.Version, minimum) < 0 {
		return ResolvedProvider{}, domain.NewDomainError(domain.ErrorUnsupportedProvider,
			fmt.Sprintf("provider version %q is older than required %q", metadata.Version, minimum), nil)
	}
	return ResolvedProvider{Adapter: item, Metadata: cloneProviderMetadata(metadata), Capabilities: item.Capabilities()}, nil
}

// ResolveModel resolves the model snapshot for a task creation command.
func (r *Registry) ResolveModel(ctx context.Context, providerID, requestedModel string) (string, error) {
	if r == nil {
		return "", domain.NewDomainError(domain.ErrorProviderUnavailable, "One-shot adapter registry is unavailable", nil)
	}
	r.mu.RLock()
	adapter, exists := r.adapters[providerID]
	r.mu.RUnlock()
	if !exists {
		return "", domain.NewDomainError(domain.ErrorUnsupportedProvider, fmt.Sprintf("provider %q is not registered", providerID), nil)
	}

	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel != "" {
		if providerID == "shell-oneshot-fixture" {
			if requestedModel != "shell" {
				return "", domain.NewDomainError(domain.ErrorInvalidRequest, fmt.Sprintf("provider %q does not support model selection: %q", providerID, requestedModel), nil)
			}
		}
		return requestedModel, nil
	}

	// Resolve default model from catalog/database first
	if r.catalog != nil {
		meta, err := r.catalog.OneShotProvider(ctx, providerID)
		if err == nil && strings.TrimSpace(meta.DefaultModel) != "" {
			return strings.TrimSpace(meta.DefaultModel), nil
		}
	}

	// Fallback to adapter configuration (TOML) default model
	defaultModel := strings.TrimSpace(adapter.DefaultModel())
	if defaultModel != "" {
		return defaultModel, nil
	}

	// If no model is configured/resolved, return invalid request (400)
	return "", domain.NewDomainError(domain.ErrorInvalidRequest, fmt.Sprintf("provider %q has no default model configured", providerID), nil)
}

// Describe returns one client-visible capability descriptor.
func (r *Registry) Describe(ctx context.Context, providerID string) (ProviderDescriptor, error) {
	resolved, err := r.ResolveProvider(ctx, providerID)
	if err != nil {
		return ProviderDescriptor{}, err
	}
	return ProviderDescriptor{
		ProviderID: resolved.Metadata.ID, DisplayName: resolved.Metadata.DisplayName,
		ProviderVersion: resolved.Metadata.Version, AdapterVersion: resolved.Adapter.AdapterVersion(),
		Enabled: resolved.Metadata.Enabled && resolved.Adapter.Enabled(), Capabilities: resolved.Capabilities,
	}, nil
}

// ListCapabilities returns deterministic provider capability metadata.
func (r *Registry) ListCapabilities(ctx context.Context) ([]ProviderDescriptor, error) {
	if r == nil {
		return nil, domain.NewDomainError(domain.ErrorProviderUnavailable, "One-shot adapter registry is unavailable", nil)
	}
	r.mu.RLock()
	ids := make([]string, 0, len(r.adapters))
	for id := range r.adapters {
		ids = append(ids, id)
	}
	r.mu.RUnlock()
	sort.Strings(ids)
	out := make([]ProviderDescriptor, 0, len(ids))
	for _, id := range ids {
		descriptor, err := r.Describe(ctx, id)
		if err != nil {
			if domain.HasCode(err, domain.ErrorDisabled) || domain.HasCode(err, domain.ErrorUnsupportedProvider) {
				continue
			}
			return nil, err
		}
		out = append(out, descriptor)
	}
	return out, nil
}

// AcquireCredential allocates a recoverable credential lease. A nil allocator
// means the provider relies on ambient/shared CLI state and returns no lease.
func (r *Registry) AcquireCredential(ctx context.Context, request CredentialRequest) (CredentialLease, error) {
	if r == nil || r.credentials == nil {
		return CredentialLease{}, nil
	}
	lease, err := r.credentials.Acquire(ctx, request)
	if err != nil {
		return CredentialLease{}, domain.NewDomainError(domain.ErrorProviderUnavailable, "credential allocation failed", err)
	}
	return cloneCredentialLease(lease), nil
}

// ReleaseCredential is idempotent for an empty lease id.
func (r *Registry) ReleaseCredential(ctx context.Context, leaseID string) error {
	if strings.TrimSpace(leaseID) == "" || r == nil || r.credentials == nil {
		return nil
	}
	if err := r.credentials.Release(ctx, leaseID); err != nil {
		return domain.NewDomainError(domain.ErrorProviderUnavailable, "credential release failed", err)
	}
	return nil
}

func cloneCredentialLease(input CredentialLease) CredentialLease {
	out := CredentialLease{ID: input.ID}
	if input.Environment != nil {
		out.Environment = make(map[string]EnvironmentValue, len(input.Environment))
		for key, value := range input.Environment {
			out.Environment[key] = value
		}
	}
	return out
}

func cloneProviderMetadata(input ProviderMetadata) ProviderMetadata {
	out := input
	if input.Environment != nil {
		out.Environment = make(map[string]EnvironmentValue, len(input.Environment))
		for key, value := range input.Environment {
			out.Environment[key] = value
		}
	}
	return out
}

func compareVersions(actual, minimum string) int {
	a, aOK := parseProviderVersion(actual)
	b, bOK := parseProviderVersion(minimum)
	if !aOK {
		return -1
	}
	if !bOK {
		return -1
	}
	for i := 0; i < len(a.numbers) || i < len(b.numbers); i++ {
		var av, bv int
		if i < len(a.numbers) {
			av = a.numbers[i]
		}
		if i < len(b.numbers) {
			bv = b.numbers[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return comparePrerelease(a.prerelease, b.prerelease)
}

type parsedProviderVersion struct {
	numbers    []int
	prerelease string
}

var providerVersionPattern = regexp.MustCompile(`(?i)(?:^|[^0-9A-Za-z])v?([0-9]+(?:\.[0-9]+){1,3}(?:-[0-9A-Za-z.-]+)?)(?:[^0-9A-Za-z]|$)`)

func parseProviderVersion(value string) (parsedProviderVersion, bool) {
	match := providerVersionPattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 2 {
		return parsedProviderVersion{}, false
	}
	version := match[1]
	prerelease := ""
	if cut := strings.Index(version, "-"); cut >= 0 {
		prerelease = version[cut+1:]
		version = version[:cut]
	}
	fields := strings.Split(version, ".")
	numbers := make([]int, 0, len(fields))
	for _, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil {
			return parsedProviderVersion{}, false
		}
		numbers = append(numbers, n)
	}
	return parsedProviderVersion{numbers: numbers, prerelease: prerelease}, true
}

func comparePrerelease(actual, minimum string) int {
	if actual == minimum {
		return 0
	}
	if actual == "" {
		return 1
	}
	if minimum == "" {
		return -1
	}
	aParts := strings.Split(actual, ".")
	bParts := strings.Split(minimum, ".")
	for i := 0; i < len(aParts) || i < len(bParts); i++ {
		if i >= len(aParts) {
			return -1
		}
		if i >= len(bParts) {
			return 1
		}
		aNum, aErr := strconv.Atoi(aParts[i])
		bNum, bErr := strconv.Atoi(bParts[i])
		switch {
		case aErr == nil && bErr == nil:
			if aNum < bNum {
				return -1
			}
			if aNum > bNum {
				return 1
			}
		case aErr == nil && bErr != nil:
			return -1
		case aErr != nil && bErr == nil:
			return 1
		default:
			if aParts[i] < bParts[i] {
				return -1
			}
			if aParts[i] > bParts[i] {
				return 1
			}
		}
	}
	return 0
}
