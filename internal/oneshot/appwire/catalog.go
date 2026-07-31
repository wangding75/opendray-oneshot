// Package appwire contains narrow adapters from the existing application
// catalog to the isolated One-shot contracts.
package appwire

import (
	"context"
	"fmt"
	"strings"

	"github.com/opendray/opendray-v2/internal/catalog"
	"github.com/opendray/opendray-v2/internal/oneshot/adapter"
	"github.com/opendray/opendray-v2/internal/oneshot/channeladapter"
	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

type providerCatalogLookup interface {
	Get(context.Context, string) (catalog.Provider, error)
}

type providerRuntimeProber interface {
	Installed(context.Context, catalog.Manifest) catalog.RuntimeInfo
}

type Catalog struct {
	catalog providerCatalogLookup
	prober  providerRuntimeProber
}

func NewCatalog(value *catalog.Catalog) *Catalog {
	return &Catalog{catalog: value, prober: catalog.NewProber()}
}

func catalogID(providerID string) string {
	if providerID == adapter.ClaudeProviderID {
		return "claude"
	}
	return providerID
}

func (c *Catalog) OneShotProvider(ctx context.Context, providerID string) (adapter.ProviderMetadata, error) {
	if c == nil || c.catalog == nil {
		return adapter.ProviderMetadata{}, domain.NewDomainError(domain.ErrorProviderUnavailable, "provider catalog is unavailable", nil)
	}
	item, err := c.catalog.Get(ctx, catalogID(providerID))
	if err != nil {
		return adapter.ProviderMetadata{}, domain.NewDomainError(domain.ErrorUnsupportedProvider, "provider is not registered", err)
	}
	if c.prober == nil {
		return adapter.ProviderMetadata{}, domain.NewDomainError(domain.ErrorProviderUnavailable, "provider runtime prober is unavailable", nil)
	}
	runtime := c.prober.Installed(ctx, item.Manifest)
	if !runtime.Installed || strings.TrimSpace(runtime.Path) == "" {
		return adapter.ProviderMetadata{}, domain.NewDomainError(domain.ErrorProviderUnavailable, "provider executable is not installed", nil)
	}
	if strings.TrimSpace(runtime.VersionError) != "" {
		return adapter.ProviderMetadata{}, domain.NewDomainError(domain.ErrorProviderUnavailable, "provider version probe failed", fmt.Errorf("%s", runtime.VersionError))
	}
	if strings.TrimSpace(runtime.InstalledVersion) == "" {
		return adapter.ProviderMetadata{}, domain.NewDomainError(domain.ErrorProviderUnavailable, "provider version is unavailable", nil)
	}
	return adapter.ProviderMetadata{ID: providerID, DisplayName: item.Manifest.DisplayName, Version: runtime.InstalledVersion, Executable: runtime.Path, Enabled: item.Enabled, Environment: map[string]adapter.EnvironmentValue{}}, nil
}

type CapabilityCatalog struct{ registry *adapter.Registry }

func NewCapabilityCatalog(registry *adapter.Registry) *CapabilityCatalog {
	return &CapabilityCatalog{registry: registry}
}

// DescribeCatalogProvider maps the interactive catalog identity to the
// authoritative One-shot adapter identity and returns its client descriptor.
func (c *CapabilityCatalog) DescribeCatalogProvider(
	ctx context.Context, catalogProviderID string,
) (any, error) {
	if c == nil || c.registry == nil {
		return nil, domain.NewDomainError(
			domain.ErrorProviderUnavailable,
			"One-shot capability registry is unavailable", nil,
		)
	}
	providerID := strings.TrimSpace(catalogProviderID)
	if providerID == "claude" {
		providerID = adapter.ClaudeProviderID
	}
	return c.registry.Describe(ctx, providerID)
}
func (c *CapabilityCatalog) ListCapabilities(ctx context.Context) ([]channeladapter.ProviderOption, error) {
	if c == nil || c.registry == nil {
		return nil, nil
	}
	items, err := c.registry.ListCapabilities(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]channeladapter.ProviderOption, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.DisplayName)
		if name == "" {
			name = item.ProviderID
		}
		out = append(out, channeladapter.ProviderOption{ID: item.ProviderID, DisplayName: name, Enabled: item.Enabled, CanResume: item.Capabilities.SupportsResume, CanAttach: item.Capabilities.Attachments})
	}
	return out, nil
}
