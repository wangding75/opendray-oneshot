package catalog

import "context"

// OneShotCapabilityResolver resolves a client-safe execution-domain descriptor
// without making Catalog import the One-shot implementation package.
type OneShotCapabilityResolver func(context.Context, string) (any, error)

// WithOneShotCapabilityResolver adds an optional One-shot descriptor to each
// provider response. Resolution failures are non-fatal: the provider remains
// visible for interactive sessions while One-shot controls stay hidden.
func (h *Handlers) WithOneShotCapabilityResolver(
	resolve OneShotCapabilityResolver,
) *Handlers {
	h.oneShotCapability = resolve
	return h
}

func (h *Handlers) attachOneShotCapability(ctx context.Context, provider *Provider) {
	if h == nil || provider == nil || h.oneShotCapability == nil {
		return
	}
	descriptor, err := h.oneShotCapability(ctx, provider.Manifest.ID)
	if err != nil {
		h.log.Debug("One-shot capability unavailable",
			"provider", provider.Manifest.ID, "err", err)
		return
	}
	provider.OneShot = descriptor
}
