package appwire

import (
	"context"
	"testing"

	"github.com/opendray/opendray-v2/internal/catalog"
	"github.com/opendray/opendray-v2/internal/oneshot/adapter"
)

func TestDescribeCatalogProviderMapsClaudeIdentity(t *testing.T) {
	registry := adapter.NewRegistry(
		adapter.NewClaudeAdapter(adapter.ClaudeConfig{Enabled: true}),
	)
	catalog := NewCapabilityCatalog(registry)

	raw, err := catalog.DescribeCatalogProvider(context.Background(), "claude")
	if err != nil {
		t.Fatalf("DescribeCatalogProvider: %v", err)
	}
	descriptor, ok := raw.(adapter.ProviderDescriptor)
	if !ok {
		t.Fatalf("descriptor type = %T", raw)
	}
	if descriptor.ProviderID != adapter.ClaudeProviderID {
		t.Fatalf("provider ID = %q, want %q", descriptor.ProviderID, adapter.ClaudeProviderID)
	}
	if descriptor.Capabilities.Attachments {
		t.Fatal("Claude One-shot attachments unexpectedly enabled")
	}
}

type appwireCatalogFixture struct {
	provider catalog.Provider
	err      error
}

func (f appwireCatalogFixture) Get(context.Context, string) (catalog.Provider, error) {
	return f.provider, f.err
}

type appwireProberFixture struct{ info catalog.RuntimeInfo }

func (f appwireProberFixture) Installed(context.Context, catalog.Manifest) catalog.RuntimeInfo {
	return f.info
}

func TestOneShotProviderUsesLiveExecutableVersion(t *testing.T) {
	fixture := &Catalog{
		catalog: appwireCatalogFixture{provider: catalog.Provider{
			Manifest: catalog.Manifest{ID: "codex", Executable: "codex", DisplayName: "Codex"},
			Enabled:  true,
		}},
		prober: appwireProberFixture{info: catalog.RuntimeInfo{
			Installed: true, Path: "/usr/local/bin/codex", InstalledVersion: "codex-cli 0.140.0",
		}},
	}
	metadata, err := fixture.OneShotProvider(context.Background(), adapter.CodexProviderID)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Executable != "/usr/local/bin/codex" || metadata.Version != "codex-cli 0.140.0" {
		t.Fatalf("metadata=%+v", metadata)
	}
}

func TestOneShotProviderRejectsBrokenOrUnversionedCLI(t *testing.T) {
	provider := catalog.Provider{Manifest: catalog.Manifest{ID: "codex", Executable: "codex"}, Enabled: true}
	for _, info := range []catalog.RuntimeInfo{
		{Installed: true, Path: "/usr/bin/codex", VersionError: "broken install"},
		{Installed: true, Path: "/usr/bin/codex"},
		{Installed: false},
	} {
		fixture := &Catalog{catalog: appwireCatalogFixture{provider: provider}, prober: appwireProberFixture{info: info}}
		if _, err := fixture.OneShotProvider(context.Background(), adapter.CodexProviderID); err == nil {
			t.Fatalf("accepted invalid runtime info: %+v", info)
		}
	}
}
