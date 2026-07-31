package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/channel"
)

type fakeRegistry struct{ channels map[string]channel.Channel }

func (r fakeRegistry) ChannelByID(id string) channel.Channel { return r.channels[id] }

type recordingChannel struct {
	id, kind string
	mu       sync.Mutex
	calls    int
	failures int
	texts    []string
}

func (c *recordingChannel) ID() string                                       { return c.id }
func (c *recordingChannel) Kind() string                                     { return c.kind }
func (c *recordingChannel) Start(context.Context, channel.InboundFunc) error { return nil }
func (c *recordingChannel) Stop(context.Context) error                       { return nil }
func (c *recordingChannel) Send(_ context.Context, msg channel.ChannelMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.failures > 0 {
		c.failures--
		return errors.New("temporary transport failure")
	}
	c.texts = append(c.texts, msg.Text)
	if msg.Metadata == nil {
		panic("delivery service must allocate metadata")
	}
	msg.Metadata[channel.MetaOutboundMessageID] = fmt.Sprintf("native-%d", len(c.texts))
	msg.Metadata[channel.MetaOutboundConversationID] = "conversation-real"
	return nil
}

func (c *recordingChannel) snapshot() (calls int, texts []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, append([]string(nil), c.texts...)
}

type editingChannel struct {
	*recordingChannel
	updateErr   error
	updateCalls int
}

func (c *editingChannel) UpdateMessage(context.Context, channel.ChannelMessage, string, string) error {
	c.mu.Lock()
	c.updateCalls++
	c.mu.Unlock()
	return c.updateErr
}

type memoryRecorder struct {
	mu       sync.Mutex
	messages []channel.ChannelMessage
}

func (r *memoryRecorder) PersistOutbound(_ context.Context, msg channel.ChannelMessage) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, msg)
	return int64(len(r.messages)), nil
}

func testService(ch channel.Channel, store OutboxStore, options ...Option) *Service {
	base := []Option{
		WithRateLimiter(noRateLimit{}),
		WithRetryPolicy(RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}),
	}
	base = append(base, options...)
	return NewService(fakeRegistry{channels: map[string]channel.Channel{ch.ID(): ch}}, nil, store, nil, base...)
}

func TestTelegramLongTextIsSegmentedInOrder(t *testing.T) {
	ch := &recordingChannel{id: "tg", kind: "telegram"}
	store := NewMemoryOutboxStore()
	service := testService(ch, store)
	text := strings.Repeat("界", 8001)

	out, err := service.Deliver(context.Background(), channel.ChannelMessage{
		ChannelID: ch.ID(), ConversationID: "42", Text: text,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls, parts := ch.snapshot()
	if calls != 3 || len(parts) != 3 {
		t.Fatalf("calls=%d parts=%d; want 3", calls, len(parts))
	}
	for index, part := range parts {
		if len([]rune(part)) > 3800 {
			t.Fatalf("part %d has %d runes", index, len([]rune(part)))
		}
	}
	if got := strings.Join(parts, ""); got != text {
		t.Fatal("segmented text did not preserve content/order")
	}
	if got := out.Metadata[MetaSegmentCount]; got != 3 {
		t.Fatalf("segment count=%v; want 3", got)
	}
}

func TestTransportRetryAndIdempotencyDoNotRepeatLogicalDelivery(t *testing.T) {
	ch := &recordingChannel{id: "tg", kind: "telegram", failures: 2}
	store := NewMemoryOutboxStore()
	recorder := &memoryRecorder{}
	service := NewService(
		fakeRegistry{channels: map[string]channel.Channel{ch.ID(): ch}},
		recorder,
		store,
		nil,
		WithRateLimiter(noRateLimit{}),
		WithRetryPolicy(RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}),
	)
	msg := channel.ChannelMessage{
		ChannelID: ch.ID(), ConversationID: "42", Text: "result",
		Metadata: map[string]any{MetaIdempotencyKey: "task-result-123"},
	}

	first, err := service.Deliver(context.Background(), msg, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls, _ := ch.snapshot()
	if calls != 3 {
		t.Fatalf("transport calls=%d; want 3", calls)
	}
	second, err := service.Deliver(context.Background(), msg, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls, _ = ch.snapshot()
	if calls != 3 {
		t.Fatalf("idempotent replay made another transport call: %d", calls)
	}
	recorder.mu.Lock()
	recorded := len(recorder.messages)
	recorder.mu.Unlock()
	if recorded != 1 {
		t.Fatalf("logical outbound history rows=%d; want 1", recorded)
	}
	if first.Metadata[MetaDeliveryID] != second.Metadata[MetaDeliveryID] {
		t.Fatalf("delivery ids differ: %v vs %v", first.Metadata[MetaDeliveryID], second.Metadata[MetaDeliveryID])
	}
	id, _ := first.Metadata[MetaDeliveryID].(string)
	attempts := store.Attempts(id)
	if len(attempts) != 3 || attempts[0].Status != "retry" || attempts[2].Status != "delivered" {
		t.Fatalf("attempts=%+v", attempts)
	}
}

func TestEditFailureFallsBackToNewMessage(t *testing.T) {
	base := &recordingChannel{id: "slack", kind: "slack"}
	ch := &editingChannel{recordingChannel: base, updateErr: errors.New("edit rejected")}
	store := NewMemoryOutboxStore()
	service := testService(ch, store)

	out, err := service.Deliver(context.Background(), channel.ChannelMessage{
		ChannelID: ch.ID(), ConversationID: "C1", Text: "replacement",
		Metadata: map[string]any{"preview_handle": "old-message"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls, texts := base.snapshot()
	if ch.updateCalls != 1 || calls != 1 || len(texts) != 1 || texts[0] != "replacement" {
		t.Fatalf("update=%d send=%d texts=%v", ch.updateCalls, calls, texts)
	}
	id, _ := out.Metadata[MetaDeliveryID].(string)
	record, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !record.Receipt.FallbackUsed || record.Receipt.Edited {
		t.Fatalf("receipt=%+v", record.Receipt)
	}
}

func TestOutboxCrashRecoveryDeliversPendingRecord(t *testing.T) {
	ch := &recordingChannel{id: "tg", kind: "telegram"}
	store := NewMemoryOutboxStore()
	msg := channel.ChannelMessage{ChannelID: ch.ID(), ConversationID: "42", Text: "recover me", Timestamp: time.Now().UTC()}
	outbound := buildOutboundMessage(msg, nil)
	payload, err := json.Marshal(outbound)
	if err != nil {
		t.Fatal(err)
	}
	oldLease := time.Now().UTC().Add(-time.Minute)
	_, _, err = store.Create(context.Background(), OutboxRecord{
		ID: outbound.ID, IdempotencyKey: outbound.IdempotencyKey,
		ChannelID: outbound.ChannelID, Payload: payload, Status: StatusSending,
		LeaseOwner: "crashed-process", LeaseUntil: oldLease,
		NextAttemptAt: time.Now().UTC(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	service := testService(ch, store, WithWorkerTiming(5*time.Millisecond, 25*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	service.Start(ctx)
	defer func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
		defer shutdownCancel()
		_ = service.Shutdown(shutdownCtx)
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		record, getErr := store.Get(context.Background(), outbound.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if record.Status == StatusDelivered {
			calls, texts := ch.snapshot()
			if calls != 1 || len(texts) != 1 || texts[0] != "recover me" {
				t.Fatalf("calls=%d texts=%v", calls, texts)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("pending outbox record was not recovered")
}

func TestCardAndAttachmentUseTransportCapabilities(t *testing.T) {
	base := &recordingChannel{id: "rich", kind: "bridge"}
	ch := &cardAndFileChannel{recordingChannel: base}
	store := NewMemoryOutboxStore()
	service := testService(ch, store)
	card := &channel.Card{Header: &channel.CardHeader{Title: "Done"}, Elements: []channel.CardElement{channel.CardMarkdown{Content: "body"}}}

	_, err := service.Deliver(context.Background(), channel.ChannelMessage{
		ChannelID: ch.ID(), ConversationID: "C", Text: card.RenderText(),
		Attachments: []channel.Attachment{{Kind: "file", Name: "report.txt", Path: "/tmp/report.txt"}},
	}, card)
	if err != nil {
		t.Fatal(err)
	}
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if len(ch.cards) != 1 || len(ch.files) != 1 || ch.files[0] != "report.txt" {
		t.Fatalf("cards=%d files=%v", len(ch.cards), ch.files)
	}
}

type cardAndFileChannel struct {
	*recordingChannel
	cards []*channel.Card
	files []string
}

func (c *cardAndFileChannel) SendCard(_ context.Context, msg channel.ChannelMessage, card *channel.Card) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.cards = append(c.cards, card)
	msg.Metadata[channel.MetaOutboundMessageID] = fmt.Sprintf("card-%d", len(c.cards))
	return nil
}

func (c *cardAndFileChannel) SendFile(_ context.Context, msg channel.ChannelMessage, file channel.FileAttachment) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.files = append(c.files, file.Filename)
	msg.Metadata[channel.MetaOutboundMessageID] = fmt.Sprintf("file-%d", len(c.files))
	return nil
}
