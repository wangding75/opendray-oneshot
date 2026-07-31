package channel

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// DispatchStatus is the explicit result of an inbound execution-domain
// handler. A handler must either claim the message or leave it for the next
// handler; errors stop routing so the same message is never delivered to a
// fallback after a partial failure.
type DispatchStatus string

const (
	DispatchNotHandled DispatchStatus = "not_handled"
	DispatchHandled    DispatchStatus = "handled"
)

var (
	ErrInboundDispatchTimeout = errors.New("inbound dispatch timed out")
	ErrInboundDispatchPanic   = errors.New("inbound dispatcher panicked")
)

// InboundPolicy contains channel-level policy already resolved by the Hub.
// It is intentionally execution-domain neutral.
type InboundPolicy struct {
	ChatEnabled   bool
	TypingEnabled bool
}

// InboundDispatchRequest is the canonical envelope sent from Channel Core to
// an execution-domain adapter after authorization and persistence.
type InboundDispatchRequest struct {
	PersistedMessageID int64
	Channel            Channel
	Message            ChannelMessage
	ReplyAddress       ReplyAddress
	Policy             InboundPolicy
}

// InboundDispatchResult records whether a handler claimed the message. Handler
// is populated by DispatcherChain for auditability.
type InboundDispatchResult struct {
	Status  DispatchStatus
	Handler string
}

func (r InboundDispatchResult) Handled() bool { return r.Status == DispatchHandled }

// InboundDispatcher routes one normalized inbound message. Implementations
// must respect ctx cancellation. Returning an error is terminal for that
// message: Channel Core records the failure and does not invoke later handlers
// or the legacy fallback.
type InboundDispatcher interface {
	Dispatch(context.Context, InboundDispatchRequest) (InboundDispatchResult, error)
}

// InboundDispatcherFunc adapts a function to InboundDispatcher.
type InboundDispatcherFunc func(context.Context, InboundDispatchRequest) (InboundDispatchResult, error)

func (f InboundDispatcherFunc) Dispatch(ctx context.Context, req InboundDispatchRequest) (InboundDispatchResult, error) {
	return f(ctx, req)
}

type registeredInboundDispatcher struct {
	name       string
	priority   int
	order      uint64
	dispatcher InboundDispatcher
}

// InboundDispatcherChain runs named handlers in deterministic order. Lower
// priority values run first. Registering the same name again replaces the old
// registration instead of creating duplicate delivery.
type InboundDispatcherChain struct {
	mu       sync.RWMutex
	next     uint64
	handlers map[string]registeredInboundDispatcher
}

func NewInboundDispatcherChain() *InboundDispatcherChain {
	return &InboundDispatcherChain{handlers: make(map[string]registeredInboundDispatcher)}
}

func (c *InboundDispatcherChain) Register(name string, priority int, dispatcher InboundDispatcher) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("inbound dispatcher name is required")
	}
	if dispatcher == nil {
		return errors.New("inbound dispatcher is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.handlers[name]; ok {
		existing.priority = priority
		existing.dispatcher = dispatcher
		c.handlers[name] = existing
		return nil
	}
	c.next++
	c.handlers[name] = registeredInboundDispatcher{
		name: name, priority: priority, order: c.next, dispatcher: dispatcher,
	}
	return nil
}

func (c *InboundDispatcherChain) Dispatch(ctx context.Context, req InboundDispatchRequest) (InboundDispatchResult, error) {
	c.mu.RLock()
	handlers := make([]registeredInboundDispatcher, 0, len(c.handlers))
	for _, h := range c.handlers {
		handlers = append(handlers, h)
	}
	c.mu.RUnlock()

	sort.SliceStable(handlers, func(i, j int) bool {
		if handlers[i].priority != handlers[j].priority {
			return handlers[i].priority < handlers[j].priority
		}
		if handlers[i].order != handlers[j].order {
			return handlers[i].order < handlers[j].order
		}
		return handlers[i].name < handlers[j].name
	})

	for _, h := range handlers {
		result, err := h.dispatcher.Dispatch(ctx, req)
		if err != nil {
			return InboundDispatchResult{Status: DispatchNotHandled, Handler: h.name}, fmt.Errorf("dispatcher %s: %w", h.name, err)
		}
		switch result.Status {
		case "", DispatchNotHandled:
			continue
		case DispatchHandled:
			result.Handler = h.name
			return result, nil
		default:
			return InboundDispatchResult{Status: DispatchNotHandled, Handler: h.name}, fmt.Errorf("dispatcher %s returned invalid status %q", h.name, result.Status)
		}
	}
	return InboundDispatchResult{Status: DispatchNotHandled}, nil
}
