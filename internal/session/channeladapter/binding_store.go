package channeladapter

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/opendray/opendray-v2/internal/channel"
)

const DefaultOutboundBindingLimit = 256

// InteractiveBindingStore owns all channel-to-Session routing hints. Keys are
// scoped by both configured channel and conversation, preventing one chat from
// inheriting another chat's selected or last-notified Session.
type InteractiveBindingStore interface {
	Resolve(msg channel.ChannelMessage) (string, bool)
	RecordLast(channelID, conversationID, sessionID string)
	RecordOutbound(channelID, conversationID, outboundMessageID, sessionID string)
	SetActive(channelID, conversationID, sessionID string)
	Active(channelID, conversationID string) string
	ClearChannel(channelID string)
}

type outboundBinding struct {
	sessionID string
	createdAt time.Time
	expiresAt time.Time
}

// MemoryBindingStore preserves the historical in-process behavior while
// isolating it inside the Session domain. ttl==0 means no age expiry (the
// previous behavior); tests and future persistent stores may use a finite TTL.
type MemoryBindingStore struct {
	mu       sync.RWMutex
	max      int
	ttl      time.Duration
	now      func() time.Time
	last     map[string]string
	active   map[string]string
	outbound map[string]map[string]outboundBinding
}

func NewMemoryBindingStore(max int, ttl time.Duration, now func() time.Time) *MemoryBindingStore {
	if max <= 0 {
		max = DefaultOutboundBindingLimit
	}
	if now == nil {
		now = time.Now
	}
	return &MemoryBindingStore{
		max: max, ttl: ttl, now: now,
		last: make(map[string]string), active: make(map[string]string),
		outbound: make(map[string]map[string]outboundBinding),
	}
}

func scopeKey(channelID, conversationID string) string {
	channelID = strings.TrimSpace(channelID)
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		conversationID = "default"
	}
	return channelID + "\x00" + conversationID
}

func (s *MemoryBindingStore) Resolve(msg channel.ChannelMessage) (string, bool) {
	if s == nil {
		return "", false
	}
	scope := scopeKey(msg.ChannelID, msg.ConversationID)
	replyID := metadataString(msg.Metadata, "reply_to_outbound_msg_id")

	s.mu.Lock()
	defer s.mu.Unlock()
	if replyID != "" {
		if byMessage := s.outbound[scope]; byMessage != nil {
			if binding, ok := byMessage[replyID]; ok {
				if !binding.expiresAt.IsZero() && !s.now().Before(binding.expiresAt) {
					delete(byMessage, replyID)
				} else if binding.sessionID != "" {
					return binding.sessionID, true
				}
			}
		}
	}
	if sessionID := s.active[scope]; sessionID != "" {
		return sessionID, true
	}
	if sessionID := s.last[scope]; sessionID != "" {
		return sessionID, true
	}
	return "", false
}

func (s *MemoryBindingStore) RecordLast(channelID, conversationID, sessionID string) {
	if s == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	s.mu.Lock()
	s.last[scopeKey(channelID, conversationID)] = sessionID
	s.mu.Unlock()
}

func (s *MemoryBindingStore) RecordOutbound(channelID, conversationID, outboundMessageID, sessionID string) {
	if s == nil || strings.TrimSpace(outboundMessageID) == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	now := s.now()
	binding := outboundBinding{sessionID: sessionID, createdAt: now}
	if s.ttl > 0 {
		binding.expiresAt = now.Add(s.ttl)
	}
	scope := scopeKey(channelID, conversationID)
	s.mu.Lock()
	defer s.mu.Unlock()
	byMessage := s.outbound[scope]
	if byMessage == nil {
		byMessage = make(map[string]outboundBinding)
		s.outbound[scope] = byMessage
	}
	byMessage[outboundMessageID] = binding
	if len(byMessage) > s.max {
		evictOldest(byMessage, len(byMessage)-s.max)
	}
}

func (s *MemoryBindingStore) SetActive(channelID, conversationID, sessionID string) {
	if s == nil {
		return
	}
	scope := scopeKey(channelID, conversationID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(sessionID) == "" {
		delete(s.active, scope)
		return
	}
	s.active[scope] = sessionID
}

func (s *MemoryBindingStore) Active(channelID, conversationID string) string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active[scopeKey(channelID, conversationID)]
}

func (s *MemoryBindingStore) ClearChannel(channelID string) {
	if s == nil {
		return
	}
	prefix := strings.TrimSpace(channelID) + "\x00"
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.last {
		if strings.HasPrefix(key, prefix) {
			delete(s.last, key)
		}
	}
	for key := range s.active {
		if strings.HasPrefix(key, prefix) {
			delete(s.active, key)
		}
	}
	for key := range s.outbound {
		if strings.HasPrefix(key, prefix) {
			delete(s.outbound, key)
		}
	}
}

func evictOldest(bindings map[string]outboundBinding, count int) {
	if count <= 0 || len(bindings) == 0 {
		return
	}
	type item struct {
		id string
		ts time.Time
	}
	items := make([]item, 0, len(bindings))
	for id, binding := range bindings {
		items = append(items, item{id: id, ts: binding.createdAt})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ts.Before(items[j].ts) })
	for i := 0; i < count && i < len(items); i++ {
		delete(bindings, items[i].id)
	}
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func outboundConversation(msg channel.ChannelMessage) string {
	if conversationID := metadataString(msg.Metadata, channel.MetaOutboundConversationID); conversationID != "" {
		return conversationID
	}
	return msg.ConversationID
}

func recordOutboundFromMessage(store InteractiveBindingStore, msg channel.ChannelMessage, sessionID string) {
	if store == nil {
		return
	}
	outboundID := metadataString(msg.Metadata, channel.MetaOutboundMessageID)
	if outboundID == "" {
		return
	}
	store.RecordOutbound(msg.ChannelID, outboundConversation(msg), outboundID, sessionID)
}
