package channeladapter

import (
	"fmt"
	"strings"

	"github.com/opendray/opendray-v2/internal/channel"
	"github.com/opendray/opendray-v2/internal/eventbus"
)

func sessionControlButtons(sessionID string) []channel.ButtonOption {
	return []channel.ButtonOption{
		{Text: "⏸ Stop", Value: "cmd:/confirm stop " + sessionID, Style: "danger"},
		{Text: "🔄 Restart", Value: "cmd:/confirm restart " + sessionID},
		{Text: "🔀 Switch", Value: "cmd:/list"},
	}
}

func buildReplyCard(sessionID, reply string) *channel.Card {
	return &channel.Card{Elements: []channel.CardElement{
		channel.CardMarkdown{Content: reply},
		channel.CardActions{Buttons: [][]channel.ButtonOption{sessionControlButtons(sessionID)}},
	}}
}

func trimReply(text string, max int) (body, footer string) {
	text = strings.TrimSpace(text)
	if max <= 0 {
		return text, ""
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text, ""
	}
	cut := string(runes[:max])
	if index := strings.LastIndexByte(cut, '\n'); index > max/2 {
		cut = cut[:index]
	}
	cut = strings.TrimRight(cut, "\n ")
	omitted := len(runes) - len([]rune(cut))
	return cut, fmt.Sprintf("\n\n… (truncated %d more characters — open the dashboard for the full reply)", omitted)
}

func buildSessionNotificationCard(event eventbus.Event, policy channel.AdapterChatPolicy) *channel.Card {
	data, _ := event.Data.(map[string]any)
	sessionID, _ := data["session_id"].(string)
	switch event.Topic {
	case "session.idle":
		milliseconds := toInt64(data["idle_for_ms"])
		body := fmt.Sprintf("Session %s went idle (silent for %ds).", sessionID, milliseconds/1000)
		if policy.IncludeSnippet {
			recent, _ := data["recent_output"].(string)
			if policy.SnippetMaxChars > 0 {
				recent = trimTail(recent, policy.SnippetMaxChars)
			}
			if recent = strings.TrimSpace(recent); recent != "" {
				body += "\n\n" + recent
			}
		}
		return &channel.Card{
			Header: &channel.CardHeader{Title: "Session idle", Color: "yellow"},
			Elements: []channel.CardElement{
				channel.CardMarkdown{Content: body},
				channel.CardActions{Buttons: [][]channel.ButtonOption{{
					{Text: "Resume", Value: "cmd:/resume " + sessionID, Style: "primary"},
					{Text: "End", Value: "cmd:/end " + sessionID, Style: "danger"},
					{Text: "Mute", Value: "cmd:/notify off"},
				}}},
			},
		}
	case "session.ended":
		exitCode := toInt64(data["exit_code"])
		color := "green"
		if exitCode != 0 {
			color = "red"
		}
		return &channel.Card{
			Header: &channel.CardHeader{Title: "Session ended", Color: color},
			Elements: []channel.CardElement{
				channel.CardMarkdown{Content: fmt.Sprintf("Session `%s` ended with exit_code=%d.", sessionID, exitCode)},
				channel.CardActions{Buttons: [][]channel.ButtonOption{{
					{Text: "Resume", Value: "cmd:/resume " + sessionID, Style: "primary"},
					{Text: "Open log", Value: "nav:/sessions/" + sessionID},
				}}},
			},
		}
	default:
		return nil
	}
}

func trimTail(text string, max int) string {
	if max <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return "[…] " + string(runes[len(runes)-max:])
}

func toInt64(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	default:
		return 0
	}
}
