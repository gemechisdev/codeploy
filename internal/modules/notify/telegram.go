package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// sendTelegram posts to the Bot API. Telegram answers 200 with {"ok":false} for some failures, so the body is
// checked as well as the status: trusting the status alone would report a delivery that never happened.
func (s *Service) sendTelegram(ctx context.Context, cfg Config, msg Message) (string, error) {
	token, chat := strings.TrimSpace(cfg["bot_token"]), strings.TrimSpace(cfg["chat_id"])
	if token == "" || chat == "" {
		return "", FieldErrors{"config.bot_token": "Enter the bot token from @BotFather"}
	}
	body, err := json.Marshal(map[string]any{"chat_id": chat, "text": plainText(msg)})
	if err != nil {
		return "", err
	}
	resp, err := s.post(ctx, "https://api.telegram.org/bot"+token+"/sendMessage", body, "application/json")
	if err != nil {
		return "", err
	}
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(resp, &out)
	if !out.OK {
		return "", fmt.Errorf("telegram refused the send: %s", telegramReason(resp))
	}
	return "delivered to chat …" + tail(chat, 4), nil
}

// telegramReason pulls the operator-facing sentence out of a Bot API error body, falling back to the truncated
// raw body when the shape is not what we expected (a proxy's error page, for instance).
func telegramReason(b []byte) string {
	var out struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(b, &out); err == nil && out.Description != "" {
		return out.Description
	}
	return truncate(string(b))
}

// post sends one JSON body and returns the response bytes, mapping a non-2xx status to an error that names the
// provider's status line: "401 Unauthorized from the provider" is what a wrong token looks like in the UI.
func (s *Service) post(ctx context.Context, url string, body []byte, contentType string, headers ...string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	for i := 1; i < len(headers); i += 2 {
		req.Header.Set(headers[i-1], headers[i])
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("delivery failed: %w", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading the provider response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("delivery failed: %s from the provider", resp.Status)
	}
	return out, nil
}

// plainText is the shape every driver sends: a subject line, the body, and no markup that a channel might
// interpret as something else.
func plainText(msg Message) string {
	subject := strings.TrimSpace(msg.Subject)
	body := strings.TrimSpace(msg.Body)
	switch {
	case subject == "":
		return body
	case body == "":
		return subject
	default:
		return subject + "\n\n" + body
	}
}

func truncate(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= 300 {
		return s
	}
	return s[:300] + "…"
}
