package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// sendWebhook posts a JSON event to the operator's own endpoint. When a signing secret is configured the body is
// HMAC-SHA256 signed in the same shape GitHub's webhooks use, so a receiver can check that the alert came from
// this panel instead of only trusting the URL it was given.
func (s *Service) sendWebhook(ctx context.Context, cfg Config, msg Message) (string, error) {
	url := strings.TrimSpace(cfg["url"])
	if !strings.HasPrefix(url, "https://") {
		return "", FieldErrors{"config.url": "Enter an HTTPS URL"}
	}
	at := msg.At
	if at.IsZero() {
		at = s.now()
	}
	payload := map[string]any{
		"source":  "codeplo",
		"level":   orDefault(msg.Level, "alert"),
		"subject": msg.Subject,
		"body":    msg.Body,
		"at":      at.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	headers := []string{}
	if secret := strings.TrimSpace(cfg["secret"]); secret != "" {
		headers = append(headers, "X-Codeplo-Signature", "sha256="+hmacHex(secret, body))
	}
	if _, err := s.post(ctx, url, body, "application/json", headers...); err != nil {
		return "", err
	}
	return "delivered to " + hostOf(url), nil
}

func hmacHex(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
