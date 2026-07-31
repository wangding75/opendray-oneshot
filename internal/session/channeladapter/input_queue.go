package channeladapter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/opendray/opendray-v2/internal/channel"
	"github.com/opendray/opendray-v2/internal/eventbus"
)

const defaultInputLaneCapacity = 64

var (
	ErrInputQueueClosed = errors.New("session input queue is closed")
	ErrInputQueueFull   = errors.New("session input queue is full")
)

// queuedInput is a fully resolved interactive message. It deliberately owns a
// copy of the transport-neutral source envelope so the bounded Channel Core
// dispatch context can end immediately after enqueueing without cancelling a
// long PTY write.
type queuedInput struct {
	sessionID          string
	source             channel.ChannelMessage
	channel            channel.Channel
	typingEnabled      bool
	persistedMessageID int64
}

type inputLane struct {
	jobs chan queuedInput
}

// InputQueue serializes PTY writes per Session. Enqueue is bounded and fast;
// the actual rune-by-rune submission runs on an adapter-owned lifecycle
// context rather than the Channel Hub's short routing deadline. Different
// Sessions receive independent lanes and therefore cannot block each other.
type InputQueue struct {
	submitter *InputSubmitter
	host      ChannelHost
	tracker   *ReplyTracker
	bus       *eventbus.Hub
	log       *slog.Logger
	capacity  int

	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool
	lanes  map[string]*inputLane
	wg     sync.WaitGroup
}

func NewInputQueue(submitter *InputSubmitter, host ChannelHost, tracker *ReplyTracker, bus *eventbus.Hub, log *slog.Logger) *InputQueue {
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &InputQueue{
		submitter: submitter,
		host:      host,
		tracker:   tracker,
		bus:       bus,
		log:       log.With("component", "session-channel-input-queue"),
		capacity:  defaultInputLaneCapacity,
		ctx:       ctx,
		cancel:    cancel,
		lanes:     make(map[string]*inputLane),
	}
}

// Enqueue accepts ownership of a resolved message. The caller's context is
// consulted only while adding the item; it is intentionally not inherited by
// the worker because Channel Core uses a short routing timeout that must not
// truncate PTY input after a partial write.
func (q *InputQueue) Enqueue(ctx context.Context, job queuedInput) error {
	if q == nil || q.submitter == nil || q.host == nil {
		return errors.New("session input queue is not configured")
	}
	if job.sessionID == "" {
		return errors.New("session id is required")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-q.ctx.Done():
		return ErrInputQueueClosed
	default:
	}

	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrInputQueueClosed
	}
	lane := q.lanes[job.sessionID]
	if lane == nil {
		lane = &inputLane{jobs: make(chan queuedInput, q.capacity)}
		q.lanes[job.sessionID] = lane
		q.wg.Add(1)
		go q.runLane(job.sessionID, lane)
	}
	q.mu.Unlock()

	select {
	case <-q.ctx.Done():
		return ErrInputQueueClosed
	case <-ctx.Done():
		return ctx.Err()
	case lane.jobs <- job:
		return nil
	default:
		return ErrInputQueueFull
	}
}

func (q *InputQueue) runLane(sessionID string, lane *inputLane) {
	defer q.wg.Done()
	for {
		select {
		case <-q.ctx.Done():
			return
		case job := <-lane.jobs:
			q.deliver(job)
		}
	}
}

func (q *InputQueue) deliver(job queuedInput) {
	err := q.submitter.Submit(q.ctx, job.sessionID, job.source.Text)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			q.log.Warn("forward to session failed", "channel", job.source.ChannelID, "session", job.sessionID, "err", err)
			replyCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, _ = q.host.Reply(replyCtx, job.source, fmt.Sprintf("Could not deliver to %s: %s", job.sessionID, err), false)
			cancel()
		}
		if q.bus != nil {
			q.bus.Publish(eventbus.Event{Topic: "channel.message_forward_failed", Data: map[string]any{
				"channel_id": job.source.ChannelID, "channel_message_id": job.persistedMessageID,
				"session_id": job.sessionID, "error": err.Error(),
			}})
		}
		return
	}

	q.host.ResetNotificationTarget(job.source.ChannelID, job.sessionID)
	ch := job.channel
	if ch == nil {
		ch = q.host.ChannelByID(job.source.ChannelID)
	}
	if ch != nil && q.tracker != nil {
		q.tracker.Begin(ch, job.source, job.sessionID, job.typingEnabled)
	}
	if q.bus != nil {
		q.bus.Publish(eventbus.Event{Topic: "channel.message_forwarded", Data: map[string]any{
			"channel_id": job.source.ChannelID, "channel_message_id": job.persistedMessageID,
			"session_id": job.sessionID, "text": job.source.Text,
		}})
	}
}

// Shutdown stops accepting new input and waits for lane workers to exit. The
// adapter lifecycle owns this queue, independent from Channel Core routing.
func (q *InputQueue) Shutdown(ctx context.Context) error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		q.cancel()
	}
	q.mu.Unlock()

	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
