package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/opendray/opendray-v2/internal/channel"
)

// ChannelRegistry resolves currently running transport implementations.
type ChannelRegistry interface {
	ChannelByID(channelID string) channel.Channel
}

// TextFormatter owns transport-specific plain-text escaping. Rich card
// renderers remain transport-native; the default formatter preserves existing
// visible content exactly.
type TextFormatter interface {
	Format(kind, text string) string
}

type passthroughFormatter struct{}

func (passthroughFormatter) Format(_ string, text string) string { return text }

// RetryPolicy applies only to outbound channel delivery attempts.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 3, BaseDelay: 250 * time.Millisecond, MaxDelay: 5 * time.Second}
}

func (p RetryPolicy) delay(attempt int) time.Duration {
	if p.BaseDelay <= 0 {
		return 0
	}
	if attempt < 1 {
		attempt = 1
	}
	delay := p.BaseDelay
	for i := 1; i < attempt; i++ {
		if p.MaxDelay > 0 && delay >= p.MaxDelay/2 {
			return p.MaxDelay
		}
		delay *= 2
	}
	if p.MaxDelay > 0 && delay > p.MaxDelay {
		return p.MaxDelay
	}
	return delay
}

type Option func(*Service)

func WithRetryPolicy(policy RetryPolicy) Option {
	return func(service *Service) { service.retry = policy }
}

func WithRateLimiter(limiter RateLimiter) Option {
	return func(service *Service) {
		if limiter != nil {
			service.limiter = limiter
		}
	}
}

func WithFormatter(formatter TextFormatter) Option {
	return func(service *Service) {
		if formatter != nil {
			service.formatter = formatter
		}
	}
}

func WithWorkerTiming(interval, lease time.Duration) Option {
	return func(service *Service) {
		if interval > 0 {
			service.workerInterval = interval
		}
		if lease > 0 {
			service.leaseDuration = lease
		}
	}
}

// Service is the durable outbound transport pipeline. It contains no Session
// or One-shot business logic and can therefore be shared by both domains.
type Service struct {
	registry ChannelRegistry
	recorder MessageRecorder
	store    OutboxStore
	log      *slog.Logger
	owner    string

	retry          RetryPolicy
	limiter        RateLimiter
	formatter      TextFormatter
	workerInterval time.Duration
	leaseDuration  time.Duration
	batchSize      int

	replyContexts sync.Map // delivery id -> opaque in-process ReplyCtx

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewService(registry ChannelRegistry, recorder MessageRecorder, store OutboxStore, log *slog.Logger, options ...Option) *Service {
	if log == nil {
		log = slog.Default()
	}
	if store == nil {
		store = NewMemoryOutboxStore()
	}
	service := &Service{
		registry:       registry,
		recorder:       recorder,
		store:          store,
		log:            log.With("component", "channel-delivery"),
		owner:          "delivery-" + uuid.NewString(),
		retry:          DefaultRetryPolicy(),
		limiter:        newChannelLimiter(),
		formatter:      passthroughFormatter{},
		workerInterval: 500 * time.Millisecond,
		leaseDuration:  30 * time.Second,
		batchSize:      32,
	}
	for _, option := range options {
		option(service)
	}
	if service.retry.MaxAttempts <= 0 {
		service.retry.MaxAttempts = 1
	}
	return service
}

func (s *Service) Start(parent context.Context) {
	if s == nil || s.store == nil {
		return
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	done := s.done
	s.mu.Unlock()
	go func() {
		defer close(done)
		s.run(ctx)
	}()
}

func (s *Service) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) run(ctx context.Context) {
	s.drain(ctx)
	ticker := time.NewTicker(s.workerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.drain(ctx)
		}
	}
}

func (s *Service) drain(ctx context.Context) {
	records, err := s.store.ClaimDue(ctx, s.owner, s.batchSize, s.leaseDuration)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("claim delivery outbox", "err", err)
		}
		return
	}
	for _, record := range records {
		if ctx.Err() != nil {
			return
		}
		if _, err := s.processClaimed(ctx, record); err != nil {
			s.log.Warn("background delivery attempt failed", "delivery", record.ID, "err", err)
		}
	}
}

// Deliver implements channel.OutboundDeliveryService. It persists the logical
// outbound message before any transport side effect, performs bounded
// transport retries, and returns the durable receipt metadata on success.
func (s *Service) Deliver(ctx context.Context, msg channel.ChannelMessage, card *channel.Card) (channel.ChannelMessage, error) {
	if s == nil || s.registry == nil || s.store == nil {
		return msg, errors.New("channel delivery service is unavailable")
	}
	ch := s.registry.ChannelByID(msg.ChannelID)
	if ch == nil {
		return msg, channel.ErrNotFound
	}
	msg = normalizeMessage(ch, msg, card)
	outbound := buildOutboundMessage(msg, card)
	if msg.ReplyCtx != nil {
		s.replyContexts.Store(outbound.ID, msg.ReplyCtx)
		defer s.replyContexts.Delete(outbound.ID)
	}
	payload, err := json.Marshal(outbound)
	if err != nil {
		return msg, fmt.Errorf("marshal outbound delivery: %w", err)
	}
	now := time.Now().UTC()
	record, _, err := s.store.Create(ctx, OutboxRecord{
		ID:             outbound.ID,
		IdempotencyKey: outbound.IdempotencyKey,
		ChannelID:      outbound.ChannelID,
		Payload:        payload,
		Status:         StatusPending,
		NextAttemptAt:  now,
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	if err != nil {
		return msg, err
	}

	for {
		if record.Status == StatusDelivered {
			return materialize(record), nil
		}
		if record.Status == StatusDead {
			return materialize(record), fmt.Errorf("channel delivery %s is dead: %s", record.ID, record.LastError)
		}
		claimed, ok, claimErr := s.store.Claim(ctx, record.ID, s.owner, s.leaseDuration)
		if claimErr != nil {
			return msg, claimErr
		}
		if !ok {
			select {
			case <-ctx.Done():
				return msg, ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
			record, err = s.store.Get(ctx, record.ID)
			if err != nil {
				return msg, err
			}
			continue
		}

		receipt, processErr := s.processClaimed(ctx, claimed)
		if processErr == nil {
			claimed.Receipt = receipt
			claimed.Status = StatusDelivered
			return materialize(claimed), nil
		}
		record, err = s.store.Get(ctx, claimed.ID)
		if err != nil {
			return msg, err
		}
		if record.Status == StatusDead || !retryable(processErr) {
			return materialize(record), processErr
		}
		delay := time.Until(record.NextAttemptAt)
		if delay < 0 {
			delay = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return materialize(record), ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Service) processClaimed(ctx context.Context, record OutboxRecord) (DeliveryReceipt, error) {
	started := time.Now().UTC()
	receipt, operation, err := s.execute(ctx, record)
	finished := time.Now().UTC()
	attempt := ChannelDeliveryAttempt{
		DeliveryID: record.ID,
		Attempt:    record.AttemptCount,
		Operation:  operation,
		StartedAt:  started,
		FinishedAt: finished,
	}
	if err == nil {
		receipt.AttemptCount = record.AttemptCount
		receipt.DeliveredAt = finished
		attempt.Status = "delivered"
		persistCtx, cancel := persistenceContext()
		markErr := s.store.MarkDelivered(persistCtx, record.ID, s.owner, receipt)
		cancel()
		if markErr != nil {
			attempt.Status = "failed"
			attempt.Error = markErr.Error()
			s.appendAttempt(attempt)
			return receipt, markErr
		}
		s.appendAttempt(attempt)
		persistCtx, cancel = persistenceContext()
		s.recordLogical(persistCtx, record, receipt)
		cancel()
		return receipt, nil
	}

	attempt.Error = err.Error()
	contextInterrupted := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	if contextInterrupted || (retryable(err) && record.AttemptCount < s.retry.MaxAttempts) {
		delay := s.retry.delay(record.AttemptCount)
		if contextInterrupted {
			delay = 0
		}
		next := time.Now().UTC().Add(delay)
		attempt.Status = "retry"
		attempt.RetryAt = next
		persistCtx, cancel := persistenceContext()
		markErr := s.store.MarkRetry(persistCtx, record.ID, s.owner, err.Error(), next)
		cancel()
		if markErr != nil {
			attempt.Status = "failed"
			attempt.Error = err.Error() + "; mark retry: " + markErr.Error()
		}
	} else {
		attempt.Status = "dead"
		persistCtx, cancel := persistenceContext()
		markErr := s.store.MarkDead(persistCtx, record.ID, s.owner, err.Error())
		cancel()
		if markErr != nil {
			attempt.Status = "failed"
			attempt.Error = err.Error() + "; mark dead: " + markErr.Error()
		}
	}
	s.appendAttempt(attempt)
	return receipt, err
}

func (s *Service) appendAttempt(attempt ChannelDeliveryAttempt) {
	ctx, cancel := persistenceContext()
	defer cancel()
	if err := s.store.AppendAttempt(ctx, attempt); err != nil {
		s.log.Warn("persist channel delivery attempt", "delivery", attempt.DeliveryID, "attempt", attempt.Attempt, "err", err)
	}
}

func (s *Service) execute(ctx context.Context, record OutboxRecord) (DeliveryReceipt, string, error) {
	var outbound OutboundMessage
	if err := json.Unmarshal(record.Payload, &outbound); err != nil {
		return record.Receipt, "decode", fmt.Errorf("decode outbound delivery: %w", err)
	}
	ch := s.registry.ChannelByID(outbound.ChannelID)
	if ch == nil {
		return record.Receipt, "resolve_transport", channel.ErrNotFound
	}
	msg := channelMessage(outbound)
	if replyCtx, ok := s.replyContexts.Load(outbound.ID); ok {
		msg.ReplyCtx = replyCtx
	}
	msg = normalizeMessage(ch, msg, outbound.Card.restore())
	kind := ch.Kind()
	receipt := record.Receipt
	receipt.DeliveryID = record.ID
	receipt.ChannelID = outbound.ChannelID
	receipt.ConversationID = msg.ConversationID
	receipt.ThreadID = msg.ThreadID

	if outbound.Edit != nil && record.Progress == 0 {
		if updater, ok := ch.(channel.MessageUpdater); ok {
			if err := s.limiter.Wait(ctx, ch.ID(), kind); err != nil {
				return receipt, "edit", err
			}
			if err := updater.UpdateMessage(ctx, msg, outbound.Edit.PreviewHandle, s.formatter.Format(kind, outbound.Text)); err == nil {
				receipt.Edited = true
				receipt.PartCount = 1
				receipt.CompletedParts = 1
				persistCtx, cancel := persistenceContext()
				err := s.store.MarkProgress(persistCtx, record.ID, s.owner, 1, receipt)
				cancel()
				if err != nil {
					return receipt, "edit", err
				}
				return receipt, "edit", nil
			}
			receipt.FallbackUsed = true
		} else {
			receipt.FallbackUsed = true
		}
	}

	parts := buildParts(ch, msg, outbound.Card.restore(), outbound.Attachments, s.formatter)
	receipt.PartCount = len(parts)
	if record.Progress > len(parts) {
		return receipt, "resume", fmt.Errorf("delivery progress %d exceeds part count %d", record.Progress, len(parts))
	}
	for index := record.Progress; index < len(parts); index++ {
		if err := s.limiter.Wait(ctx, ch.ID(), kind); err != nil {
			return receipt, parts[index].operation, err
		}
		partMsg := msg
		partMsg.Metadata = cloneMetadata(msg.Metadata)
		partMsg.Metadata[MetaDeliveryID] = record.ID
		fallback, err := sendPart(ctx, ch, partMsg, parts[index])
		if err != nil {
			return receipt, parts[index].operation, err
		}
		if fallback {
			receipt.FallbackUsed = true
		}
		if nativeID := metadataString(partMsg.Metadata, channel.MetaOutboundMessageID); nativeID != "" {
			receipt.MessageIDs = appendUnique(receipt.MessageIDs, nativeID)
		}
		if conversation := metadataString(partMsg.Metadata, channel.MetaOutboundConversationID); conversation != "" {
			receipt.ConversationID = conversation
		}
		if thread := metadataString(partMsg.Metadata, channel.MetaOutboundThreadID); thread != "" {
			receipt.ThreadID = thread
		}
		receipt.CompletedParts = index + 1
		persistCtx, cancel := persistenceContext()
		err = s.store.MarkProgress(persistCtx, record.ID, s.owner, index+1, receipt)
		cancel()
		if err != nil {
			return receipt, parts[index].operation, err
		}
	}
	return receipt, "send", nil
}

func (s *Service) recordLogical(ctx context.Context, record OutboxRecord, receipt DeliveryReceipt) {
	if s.recorder == nil {
		return
	}
	var outbound OutboundMessage
	if err := json.Unmarshal(record.Payload, &outbound); err != nil {
		s.log.Warn("decode delivered message for history", "delivery", record.ID, "err", err)
		return
	}
	msg := channelMessage(outbound)
	msg.ConversationID = receipt.ConversationID
	msg.ThreadID = receipt.ThreadID
	msg.Metadata = cloneMetadata(msg.Metadata)
	msg.Metadata[MetaDeliveryID] = record.ID
	msg.Metadata[MetaDeliveryState] = string(StatusDelivered)
	msg.Metadata[MetaSegmentCount] = receipt.PartCount
	if len(receipt.MessageIDs) > 0 {
		msg.Metadata[MetaMessageIDs] = receipt.MessageIDs
		msg.Metadata[channel.MetaOutboundMessageID] = receipt.MessageIDs[len(receipt.MessageIDs)-1]
	}
	if receipt.ConversationID != "" {
		msg.Metadata[channel.MetaOutboundConversationID] = receipt.ConversationID
	}
	if receipt.ThreadID != "" {
		msg.Metadata[channel.MetaOutboundThreadID] = receipt.ThreadID
	}
	if _, err := s.recorder.PersistOutbound(ctx, msg); err != nil {
		s.log.Warn("persist delivered channel message", "delivery", record.ID, "err", err)
	}
}

func normalizeMessage(ch channel.Channel, msg channel.ChannelMessage, card *channel.Card) channel.ChannelMessage {
	if msg.Direction == "" {
		msg.Direction = channel.DirectionOutbound
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}
	msg.Metadata = cloneMetadata(msg.Metadata)
	if card != nil && msg.Text == "" {
		msg.Text = card.RenderText()
	}
	if resolver, ok := ch.(channel.OutboundAddressResolver); ok {
		address := resolver.ResolveOutboundAddress(msg)
		if address.ConversationID != "" {
			msg.ConversationID = address.ConversationID
			msg.Metadata[channel.MetaOutboundConversationID] = address.ConversationID
		}
		if address.ThreadID != "" {
			msg.ThreadID = address.ThreadID
			msg.Metadata[channel.MetaOutboundThreadID] = address.ThreadID
		}
		if address.MessageID != "" && msg.SourceMessageID == "" {
			msg.SourceMessageID = address.MessageID
		}
		if address.ReplyCtx != nil {
			msg.ReplyCtx = address.ReplyCtx
		}
	}
	return msg
}

func buildOutboundMessage(msg channel.ChannelMessage, card *channel.Card) OutboundMessage {
	id := uuid.NewString()
	key := metadataString(msg.Metadata, MetaIdempotencyKey)
	if key == "" {
		key = "delivery:" + id
	}
	outbound := OutboundMessage{
		ID:             id,
		IdempotencyKey: key,
		ChannelID:      msg.ChannelID,
		Address: channel.ReplyAddress{
			ChannelID:      msg.ChannelID,
			ConversationID: msg.ConversationID,
			ThreadID:       msg.ThreadID,
			MessageID:      msg.SourceMessageID,
			Metadata:       cloneMetadata(msg.Metadata),
		},
		Text:        msg.Text,
		Card:        snapshotCard(card),
		Attachments: append([]channel.Attachment(nil), msg.Attachments...),
		Metadata:    cloneMetadata(msg.Metadata),
		CreatedAt:   msg.Timestamp,
	}
	if previewHandle := metadataString(msg.Metadata, "preview_handle"); previewHandle != "" {
		outbound.Edit = &MessageEdit{PreviewHandle: previewHandle}
	}
	return outbound
}

func channelMessage(outbound OutboundMessage) channel.ChannelMessage {
	return channel.ChannelMessage{
		ChannelID:       outbound.ChannelID,
		Direction:       channel.DirectionOutbound,
		ConversationID:  outbound.Address.ConversationID,
		ThreadID:        outbound.Address.ThreadID,
		SourceMessageID: outbound.Address.MessageID,
		Text:            outbound.Text,
		Attachments:     append([]channel.Attachment(nil), outbound.Attachments...),
		Metadata:        cloneMetadata(outbound.Metadata),
		Timestamp:       outbound.CreatedAt,
	}
}

func materialize(record OutboxRecord) channel.ChannelMessage {
	var outbound OutboundMessage
	_ = json.Unmarshal(record.Payload, &outbound)
	msg := channelMessage(outbound)
	msg.ConversationID = firstNonEmpty(record.Receipt.ConversationID, msg.ConversationID)
	msg.ThreadID = firstNonEmpty(record.Receipt.ThreadID, msg.ThreadID)
	msg.Metadata = cloneMetadata(msg.Metadata)
	msg.Metadata[MetaDeliveryID] = record.ID
	msg.Metadata[MetaDeliveryState] = string(record.Status)
	msg.Metadata[MetaSegmentCount] = record.Receipt.PartCount
	if len(record.Receipt.MessageIDs) > 0 {
		msg.Metadata[MetaMessageIDs] = record.Receipt.MessageIDs
		msg.Metadata[channel.MetaOutboundMessageID] = record.Receipt.MessageIDs[len(record.Receipt.MessageIDs)-1]
	}
	if msg.ConversationID != "" {
		msg.Metadata[channel.MetaOutboundConversationID] = msg.ConversationID
	}
	if msg.ThreadID != "" {
		msg.Metadata[channel.MetaOutboundThreadID] = msg.ThreadID
	}
	return msg
}

func retryable(err error) bool {
	if err == nil {
		return false
	}
	return !errors.Is(err, channel.ErrNotFound)
}

func cloneMetadata(source map[string]any) map[string]any {
	out := make(map[string]any, len(source)+4)
	for key, value := range source {
		out[key] = value
	}
	return out
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	switch value := metadata[key].(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	case int:
		return fmt.Sprintf("%d", value)
	case int64:
		return fmt.Sprintf("%d", value)
	case float64:
		return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.6f", value), "0"), ".")
	default:
		return ""
	}
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func persistenceContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

var _ ChannelDeliveryService = (*Service)(nil)
var _ channel.OutboundDeliveryService = (*Service)(nil)
var _ channel.OutboundDeliveryLifecycle = (*Service)(nil)
