package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/opendray/opendray-v2/internal/channel"
)

type deliveryPart struct {
	operation  string
	text       string
	card       *channel.Card
	attachment *channel.Attachment
}

func buildParts(ch channel.Channel, msg channel.ChannelMessage, card *channel.Card, attachments []channel.Attachment, formatter TextFormatter) []deliveryPart {
	parts := make([]deliveryPart, 0, 1+len(attachments))
	if card != nil {
		if _, ok := ch.(channel.CardSender); ok {
			parts = append(parts, deliveryPart{operation: "send_card", text: card.RenderText(), card: card})
		} else {
			text := formatter.Format(ch.Kind(), card.RenderText())
			for _, segment := range splitText(text, maxTextRunes(ch.Kind())) {
				parts = append(parts, deliveryPart{operation: "send_text", text: segment})
			}
		}
	} else if msg.Text != "" {
		text := formatter.Format(ch.Kind(), msg.Text)
		for _, segment := range splitText(text, maxTextRunes(ch.Kind())) {
			parts = append(parts, deliveryPart{operation: "send_text", text: segment})
		}
	}
	for index := range attachments {
		copy := attachments[index]
		parts = append(parts, deliveryPart{operation: "send_attachment", attachment: &copy})
	}
	return parts
}

func sendPart(ctx context.Context, ch channel.Channel, msg channel.ChannelMessage, part deliveryPart) (fallback bool, err error) {
	switch {
	case part.card != nil:
		msg.Text = part.text
		if sender, ok := ch.(channel.CardSender); ok {
			if err := sender.SendCard(ctx, msg, part.card); err == nil {
				return false, nil
			} else if !errors.Is(err, channel.ErrNotSupported) {
				return false, err
			}
		}
		return true, ch.Send(ctx, msg)
	case part.attachment != nil:
		return sendAttachment(ctx, ch, msg, *part.attachment)
	default:
		msg.Text = part.text
		return false, ch.Send(ctx, msg)
	}
}

func sendAttachment(ctx context.Context, ch channel.Channel, msg channel.ChannelMessage, attachment channel.Attachment) (bool, error) {
	isImage := strings.HasPrefix(attachment.MIMEType, "image/") || attachment.Kind == "image"
	if isImage {
		if sender, ok := ch.(channel.ImageSender); ok {
			return false, sender.SendImage(ctx, msg, channel.ImageAttachment{
				Path: attachment.Path, URL: attachment.URL, Caption: attachment.Name,
			})
		}
	}
	if sender, ok := ch.(channel.FileSender); ok {
		return false, sender.SendFile(ctx, msg, channel.FileAttachment{
			Path: attachment.Path, URL: attachment.URL, Filename: attachment.Name,
		})
	}
	location := firstNonEmpty(attachment.URL, attachment.Path)
	label := firstNonEmpty(attachment.Name, attachment.ID, "attachment")
	if location == "" {
		return false, fmt.Errorf("channel %s does not support attachment %s", ch.ID(), label)
	}
	msg.Text = fmt.Sprintf("[%s] %s", label, location)
	return true, ch.Send(ctx, msg)
}
