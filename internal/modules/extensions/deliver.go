package extensions

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const maxAttempts = 8

// Enqueue stores a durable delivery for every extension subscribed to event.
func (s *Service) Enqueue(ctx context.Context, event string, payload any) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT extension_id FROM extension_subscriptions WHERE event=?`, event)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	now := s.ts()
	for rows.Next() {
		var eid string
		if err := rows.Scan(&eid); err != nil {
			return n, err
		}
		id := s.id("xdl_")
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO extension_deliveries(id,extension_id,event,payload_json,status,attempts,next_attempt_at,created_at,updated_at)
			VALUES (?,?,?,?, 'pending', 0, ?, ?, ?)`, id, eid, event, string(body), now, now, now); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

// DeliverDue attempts pending/failed deliveries whose next_attempt_at has passed.
func (s *Service) DeliverDue(ctx context.Context, limit int) (int, error) {
	if !s.OutboundEnabled(ctx) {
		return 0, nil
	}
	if limit <= 0 {
		limit = 20
	}
	now := s.ts()
	rows, err := s.DB.QueryContext(ctx, `SELECT d.id, d.extension_id, d.event, d.payload_json, d.attempts, e.endpoint_url
		FROM extension_deliveries d JOIN extensions e ON e.id=d.extension_id
		WHERE d.status IN ('pending','failed') AND d.next_attempt_at<=? AND d.attempts<?
		ORDER BY d.next_attempt_at, d.id LIMIT ?`, now, maxAttempts, limit)
	if err != nil {
		return 0, err
	}
	type row struct {
		id, eid, event, payload, endpoint string
		attempts                          int
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.eid, &r.event, &r.payload, &r.attempts, &r.endpoint); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, r)
	}
	rows.Close()
	done := 0
	for _, r := range list {
		if err := s.attempt(ctx, r.id, r.eid, r.event, r.payload, r.endpoint, r.attempts); err == nil {
			done++
		}
	}
	return done, nil
}

func (s *Service) attempt(ctx context.Context, id, eid, event, payload, endpoint string, attempts int) error {
	secret, err := s.openSecret(ctx, eid)
	if err != nil {
		return s.mark(ctx, id, attempts+1, false, err.Error())
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	headers := map[string]string{
		"Content-Type":            "application/json",
		"X-Codeplo-Event":         event,
		"X-Codeplo-Delivery":      id,
		"X-Codeplo-Signature-256": sig,
	}
	do := s.HTTPDo
	if do == nil {
		do = defaultHTTPDo
	}
	status, err := do(ctx, endpoint, headers, []byte(payload))
	if err != nil {
		return s.mark(ctx, id, attempts+1, false, err.Error())
	}
	if status < 200 || status >= 300 {
		return s.mark(ctx, id, attempts+1, false, fmt.Sprintf("HTTP %d", status))
	}
	return s.mark(ctx, id, attempts+1, true, "")
}

func (s *Service) mark(ctx context.Context, id string, attempts int, ok bool, lastErr string) error {
	now := s.ts()
	if ok {
		_, err := s.DB.ExecContext(ctx, `UPDATE extension_deliveries SET status='delivered', attempts=?, last_error='', updated_at=? WHERE id=?`,
			attempts, now, id)
		return err
	}
	status := "failed"
	if attempts >= maxAttempts {
		status = "dead"
	}
	// exponential backoff: 30s * 2^(attempts-1), capped ~1h
	backoff := 30 * time.Second
	for i := 1; i < attempts && backoff < time.Hour; i++ {
		backoff *= 2
	}
	next := s.now().UTC().Add(backoff).Format("2006-01-02T15:04:05.000Z")
	_, err := s.DB.ExecContext(ctx, `UPDATE extension_deliveries SET status=?, attempts=?, next_attempt_at=?, last_error=?, updated_at=? WHERE id=?`,
		status, attempts, next, lastErr, now, id)
	return err
}

func defaultHTTPDo(ctx context.Context, url string, headers map[string]string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode, nil
}

// DeadLetters lists deliveries in dead state for operator visibility.
func (s *Service) DeadLetters(ctx context.Context, limit int) ([]Delivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, extension_id, event, status, attempts, last_error, created_at, updated_at
		FROM extension_deliveries WHERE status='dead' ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.ExtensionID, &d.Event, &d.Status, &d.Attempts, &d.LastError, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

type Delivery struct {
	ID          string
	ExtensionID string
	Event       string
	Status      string
	Attempts    int
	LastError   string
	CreatedAt   string
	UpdatedAt   string
}

// Ensure sql import used when only types referenced in other files
