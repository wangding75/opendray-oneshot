package domain

// SourceKind is the immutable Task origin kind.
type SourceKind string

const (
	SourceAPI      SourceKind = "api"
	SourceTelegram SourceKind = "telegram"
	SourceMobile   SourceKind = "mobile"
	SourceWeb      SourceKind = "web"
)

func (k SourceKind) String() string { return string(k) }

func (k SourceKind) Valid() bool {
	switch k {
	case SourceAPI, SourceTelegram, SourceMobile, SourceWeb:
		return true
	default:
		return false
	}
}

// ReplyAddress is an immutable snapshot of where a source can receive replies.
// It is transport-neutral and contains no Session or execution-domain identifier.
type ReplyAddress struct {
	ChannelID      string            `json:"channel_id,omitempty"`
	ConversationID string            `json:"conversation_id,omitempty"`
	ThreadID       string            `json:"thread_id,omitempty"`
	MessageID      string            `json:"message_id,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

func (r ReplyAddress) Validate() error {
	if r.ChannelID == "" && r.ConversationID == "" && r.ThreadID == "" && r.MessageID == "" && len(r.Metadata) == 0 {
		return InvalidRequestf("reply_address must contain at least one routing field")
	}
	if r.ConversationID != "" && r.ChannelID == "" {
		return InvalidRequestf("reply_address.channel_id is required when conversation_id is set")
	}
	return nil
}

func cloneReplyAddress(input *ReplyAddress) *ReplyAddress {
	if input == nil {
		return nil
	}
	out := *input
	out.Metadata = cloneStringMap(input.Metadata)
	return &out
}

// Source is captured when a Task is created and never changes.
type Source struct {
	Kind            SourceKind        `json:"kind"`
	ClientRequestID string            `json:"client_request_id,omitempty"`
	ChannelID       string            `json:"channel_id,omitempty"`
	SourceMessageID string            `json:"source_message_id,omitempty"`
	ReplyAddress    *ReplyAddress     `json:"reply_address,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

func (s Source) Validate() error {
	if !s.Kind.Valid() {
		return InvalidRequestf("invalid source.kind %q", s.Kind)
	}
	if s.Kind == SourceTelegram {
		if err := requireNonEmpty(s.ChannelID, "source.channel_id"); err != nil {
			return err
		}
		if err := requireNonEmpty(s.SourceMessageID, "source.source_message_id"); err != nil {
			return err
		}
	}
	if s.ReplyAddress != nil {
		if err := s.ReplyAddress.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func cloneSource(input Source) Source {
	out := input
	out.ReplyAddress = cloneReplyAddress(input.ReplyAddress)
	out.Metadata = cloneStringMap(input.Metadata)
	return out
}
