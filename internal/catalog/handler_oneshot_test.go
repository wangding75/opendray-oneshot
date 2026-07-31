package catalog

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

func TestAttachOneShotCapabilityAddsDescriptorWithoutCatalogDependency(t *testing.T) {
	h := &Handlers{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.WithOneShotCapabilityResolver(func(_ context.Context, providerID string) (any, error) {
		if providerID != "claude" {
			t.Fatalf("provider ID = %q, want claude", providerID)
		}
		return map[string]any{
			"provider_id":  "claude-code",
			"capabilities": map[string]any{"attachments": false},
		}, nil
	})
	provider := Provider{Manifest: Manifest{ID: "claude"}}

	h.attachOneShotCapability(context.Background(), &provider)

	descriptor, ok := provider.OneShot.(map[string]any)
	if !ok || descriptor["provider_id"] != "claude-code" {
		t.Fatalf("unexpected One-shot descriptor: %#v", provider.OneShot)
	}
}

func TestAttachOneShotCapabilityFailureKeepsInteractiveProviderVisible(t *testing.T) {
	h := &Handlers{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.WithOneShotCapabilityResolver(func(context.Context, string) (any, error) {
		return nil, errors.New("CLI unavailable")
	})
	provider := Provider{Manifest: Manifest{ID: "claude"}, Enabled: true}

	h.attachOneShotCapability(context.Background(), &provider)

	if provider.OneShot != nil {
		t.Fatalf("OneShot = %#v, want nil", provider.OneShot)
	}
	if !provider.Enabled {
		t.Fatal("interactive provider was disabled by One-shot resolution failure")
	}
}
