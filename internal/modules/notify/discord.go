package notify

import (
	"context"
	"encoding/json"
	"strings"
)

// discordContentMax is Discord's hard limit for a message's content field. A payload over it is rejected with
// error 50007, so the panel truncates instead of losing the alert: the first lines of a disk alert are the part
// that has to arrive.
const discordContentMax = 2000

// sendDiscord posts to a channel webhook. The webhook URL carries the token, which is why the whole configuration
// is sealed and why no route ever returns it.
func (s *Service) sendDiscord(ctx context.Context, cfg Config, msg Message) (string, error) {
	url := strings.TrimSpace(cfg["webhook_url"])
	if !discordURLRe.MatchString(url) {
		return "", FieldErrors{"config.webhook_url": "Enter a Discord webhook URL"}
	}
	text := plainText(msg)
	// The limit is characters, not bytes: a slice at 1999 bytes can cut a multi-byte rune and send invalid UTF-8.
	if r := []rune(text); len(r) > discordContentMax {
		text = string(r[:discordContentMax-1]) + "…"
	}
	// allowed_mentions is emptied on purpose. Discord parses the content field, so an alert whose text happens to
	// contain "@everyone" — a resource name, a log tail pasted into a message — would ping the whole channel.
	body, err := json.Marshal(map[string]any{"content": text, "username": "codeplo",
		"allowed_mentions": map[string]any{"parse": []string{}}})
	if err != nil {
		return "", err
	}
	if _, err := s.post(ctx, url, body, "application/json"); err != nil {
		return "", err
	}
	return "delivered to the Discord webhook", nil
}
