// Package notify is the Phase 6 delivery layer: the four channel kinds the contract allows, their sealed
// configuration, and the single function the rest of the panel uses to send a message. The HTTP layer is
// internal/api/handlers_notify.go.
//
// Two rules hold this package together. A channel's configuration is written once and only ever read back by a
// sender, never by the API: what a client sees is destination_hint, a label computed at creation. And a delivery
// failure is a result, not a panic: an alert that cannot be sent must still be recorded, because the alternative
// is a panel that looks healthy while nobody is being told anything.
package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/codeplo/codeplo/internal/crypto"
)

// Querier is satisfied by both *sql.DB and *sql.Tx, so a channel write can share the caller's transaction.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Service is the notify layer's entry point. Key seals and opens channel configuration; Timeout bounds one
// delivery attempt so a black-holed webhook cannot stall the alert evaluator.
type Service struct {
	DB      *sql.DB
	Key     []byte
	Timeout time.Duration
	// HTTP is injectable so a test can stand in for Telegram or Discord. Its per-call deadline comes from
	// Timeout, not from the client, so changing the setting takes effect without rebuilding the service.
	HTTP *http.Client
	// Now is injectable so a test can freeze time; it is the daemon clock in production.
	Now func() time.Time
}

func New(db *sql.DB, key []byte) *Service {
	return &Service{DB: db, Key: key, Timeout: 10 * time.Second, HTTP: &http.Client{}}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) ts() string { return s.now().UTC().Format("2006-01-02T15:04:05.000Z") }

func (s *Service) id(prefix string) string { return crypto.ULID(prefix, s.now().UnixMilli()) }

// client returns a per-request client whose deadline is the current Timeout, so a setting change is honoured on
// the next send rather than at the next restart.
//
// Redirects are not followed. A channel's URL is operator-supplied text and its body is an alert plus, for a
// signed webhook, a value derived from the signing secret; a redirect would hand both to a host nobody typed.
// ErrUseLastResponse reports the 3xx back as a failed delivery with its status intact, which is a diagnosable
// answer rather than a silent second request.
func (s *Service) client() *http.Client {
	c := s.HTTP
	if c == nil {
		c = &http.Client{}
	}
	t := s.Timeout
	if t <= 0 {
		t = 10 * time.Second
	}
	clone := *c
	clone.Timeout = t
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &clone
}

// FieldErrors is a validation failure keyed by request field; the handler turns it into the 422 envelope.
type FieldErrors map[string]string

func (e FieldErrors) Error() string { return "validation failed" }

// NotFound and Conflict mirror the git package's two non-validation failures so the handler can map them the same
// way it already maps git's.
type NotFound struct{ What string }

func (e NotFound) Error() string { return e.What + " not found" }

type Conflict struct{ Msg string }

func (e Conflict) Error() string { return e.Msg }

// Config is a channel's kind-specific settings. Every value is a string because the contract's request body is
// map[string]string; a sender parses what it needs and reports a field error when a value is unusable.
type Config map[string]string

// Channel is what the API returns. The configuration is deliberately absent: no route reveals a bot token, and
// this type is the only thing a channel list can build.
type Channel struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	DestinationHint string `json:"destination_hint"`
	CreatedAt       string `json:"created_at"`
}

// Message is one alert or test note on its way out. Subject is the short line a provider shows as a title; Body
// carries the detail; Level is "alert" or "info", which is what lets a driver colour a card or prefix a subject.
type Message struct {
	Subject string
	Body    string
	Level   string
	At      time.Time
}

// Report counts one Deliver call across a rule's channels. Detail is the first failure's reason, which is what
// the alert log and the channel test endpoint show.
type Report struct {
	Sent   int
	Failed int
	Detail string
}

// Kinds are the channel types the contract allows, in the order the UI lists them.
var Kinds = []string{"telegram", "discord", "email", "webhook"}

func validKind(k string) bool {
	for _, x := range Kinds {
		if x == k {
			return true
		}
	}
	return false
}

// Validate checks a channel configuration and returns the hint the API will show in its place. The messages are
// the ones the executable mock uses, so a field error reads the same in the UI either way.
//
// The patterns are deliberately the shape of a credential rather than its value: a bot token that cannot parse
// was never going to work, and refusing it at creation costs the operator nothing.
func Validate(kind string, cfg Config) (hint string, fields FieldErrors) {
	get := func(k string) string { return strings.TrimSpace(cfg[k]) }
	switch kind {
	case "telegram":
		token, chat := get("bot_token"), get("chat_id")
		if !telegramTokenRe.MatchString(token) {
			return "", FieldErrors{"config.bot_token": "Enter the bot token from @BotFather"}
		}
		if !chatIDRe.MatchString(chat) {
			return "", FieldErrors{"config.chat_id": "Enter a numeric chat id"}
		}
		return "chat …" + tail(chat, 4), nil
	case "discord":
		url := get("webhook_url")
		if !discordURLRe.MatchString(url) {
			return "", FieldErrors{"config.webhook_url": "Enter a Discord webhook URL"}
		}
		return "discord webhook", nil
	case "email":
		to := get("to")
		if !emailRe.MatchString(to) {
			return "", FieldErrors{"config.to": "Enter a recipient email address"}
		}
		f := FieldErrors{}
		if get("host") == "" {
			f["config.host"] = "Enter the SMTP host"
		}
		if get("from") == "" {
			f["config.from"] = "Enter the sender address"
		}
		if get("password") == "" {
			f["config.password"] = "Enter the SMTP password"
		}
		if p := get("port"); p != "" && !validPort(p) {
			f["config.port"] = "Port must be 1-65535"
		}
		if len(f) > 0 {
			return "", f
		}
		return maskAddress(to), nil
	case "webhook":
		url := get("url")
		if !strings.HasPrefix(url, "https://") {
			return "", FieldErrors{"config.url": "Enter an HTTPS URL"}
		}
		return hostOf(url), nil
	default:
		return "", FieldErrors{"kind": "Choose a channel type"}
	}
}

var (
	telegramTokenRe = regexp.MustCompile(`^\d+:[\w-]{20,}$`)
	chatIDRe        = regexp.MustCompile(`^-?\d+$`)
	discordURLRe    = regexp.MustCompile(`^https://discord\.com/api/webhooks/`)
	emailRe         = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	portRe          = regexp.MustCompile(`^([1-9][0-9]{0,4})$`)
)

// validPort is the port check the field error promises. portRe alone would accept 99999, which is a syntactically
// clean number that no SMTP server listens on and that would fail as a connection error far from this form.
func validPort(s string) bool {
	if !portRe.MatchString(s) {
		return false
	}
	n, err := strconv.Atoi(s)
	return err == nil && n >= 1 && n <= 65535
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// maskAddress keeps an inbox readable in the panel without copying it out of the sealed configuration: the first
// character, an ellipsis, and everything from the @.
func maskAddress(a string) string {
	i := strings.Index(a, "@")
	if i <= 0 {
		return a
	}
	return a[:1] + "…" + a[i:]
}

func hostOf(url string) string {
	h := strings.TrimPrefix(url, "https://")
	if i := strings.IndexAny(h, "/?"); i >= 0 {
		h = h[:i]
	}
	if h == "" {
		return "webhook"
	}
	return h
}

// CreateChannel stores a channel and returns its public form. The plaintext configuration is sealed on the way
// in, so a database dump yields nothing usable.
func (s *Service) CreateChannel(ctx context.Context, q Querier, kind, name string, cfg Config) (Channel, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Channel{}, FieldErrors{"name": "Give the channel a name"}
	}
	if !validKind(kind) {
		return Channel{}, FieldErrors{"kind": "Choose a channel type"}
	}
	hint, fields := Validate(kind, cfg)
	if fields != nil {
		return Channel{}, fields
	}
	blob, err := crypto.Seal(s.Key, marshalConfig(cfg))
	if err != nil {
		return Channel{}, err
	}
	ch := Channel{ID: s.id("chn_"), Kind: kind, Name: name, Enabled: true, DestinationHint: hint, CreatedAt: s.ts()}
	_, err = q.ExecContext(ctx, `INSERT INTO notification_channels(id,kind,name,destination_hint,config_enc,enabled,created_at) VALUES (?,?,?,?,?,1,?)`,
		ch.ID, ch.Kind, ch.Name, ch.DestinationHint, blob, ch.CreatedAt)
	if err != nil {
		return Channel{}, err
	}
	return ch, nil
}

// ListChannels returns every channel, oldest first: the list is short and the UI groups it by kind.
func (s *Service) ListChannels(ctx context.Context, q Querier) ([]Channel, error) {
	rows, err := q.QueryContext(ctx, `SELECT id,kind,name,destination_hint,enabled,created_at FROM notification_channels ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		var (
			c     Channel
			yesNo int
		)
		if err := rows.Scan(&c.ID, &c.Kind, &c.Name, &c.DestinationHint, &yesNo, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Enabled = yesNo != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) GetChannel(ctx context.Context, q Querier, id string) (Channel, error) {
	var (
		c     Channel
		yesNo int
	)
	err := q.QueryRowContext(ctx, `SELECT id,kind,name,destination_hint,enabled,created_at FROM notification_channels WHERE id=?`, id).
		Scan(&c.ID, &c.Kind, &c.Name, &c.DestinationHint, &yesNo, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, NotFound{What: "channel"}
	}
	if err != nil {
		return Channel{}, err
	}
	c.Enabled = yesNo != 0
	return c, nil
}

// PatchChannel renames or enables a channel. Configuration is not patchable: a wrong secret is fixed by deleting
// the channel and creating it again, which is also what keeps this route from ever becoming a way to write a
// credential it cannot read.
func (s *Service) PatchChannel(ctx context.Context, q Querier, id string, name *string, enabled *bool) (Channel, error) {
	ch, err := s.GetChannel(ctx, q, id)
	if err != nil {
		return Channel{}, err
	}
	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if trimmed == "" {
			return Channel{}, FieldErrors{"name": "Name cannot be empty"}
		}
		ch.Name = trimmed
	}
	if enabled != nil {
		ch.Enabled = *enabled
	}
	_, err = q.ExecContext(ctx, `UPDATE notification_channels SET name=?,enabled=? WHERE id=?`, ch.Name, boolInt(ch.Enabled), ch.ID)
	if err != nil {
		return Channel{}, err
	}
	return ch, nil
}

// DeleteChannel removes a channel. Alert rules that still name it are pruned by the handler in the same
// transaction, because alert_rules belongs to the monitor package.
func (s *Service) DeleteChannel(ctx context.Context, q Querier, id string) error {
	res, err := q.ExecContext(ctx, `DELETE FROM notification_channels WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return NotFound{What: "channel"}
	}
	return nil
}

// OpenConfig decrypts a channel's configuration. It is used by the senders only; nothing in the API layer calls
// it, and its name says so out loud.
func (s *Service) OpenConfig(ctx context.Context, q Querier, id string) (Config, Channel, error) {
	ch, err := s.GetChannel(ctx, q, id)
	if err != nil {
		return nil, Channel{}, err
	}
	var blob []byte
	if err := q.QueryRowContext(ctx, `SELECT config_enc FROM notification_channels WHERE id=?`, id).Scan(&blob); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, Channel{}, NotFound{What: "channel"}
		}
		return nil, Channel{}, err
	}
	plain, err := crypto.Open(s.Key, blob)
	if err != nil {
		return nil, ch, ErrSealedKey
	}
	cfg := Config{}
	if err := json.Unmarshal(plain, &cfg); err != nil {
		return nil, ch, err
	}
	return cfg, ch, nil
}

// ErrSealedKey means the row exists but this master key cannot open it: a different database, or a key rotated
// out from under it. Reported as a daemon failure, never as an empty credential.
var ErrSealedKey = errors.New("stored channel configuration could not be decrypted with this master key")

func marshalConfig(cfg Config) []byte {
	b, err := json.Marshal(cfg)
	if err != nil {
		// Every value is a string, so this cannot fail on data the handler just decoded.
		return []byte("{}")
	}
	return b
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Service) timeout() time.Duration {
	if s.Timeout <= 0 {
		return 10 * time.Second
	}
	return s.Timeout
}

// Send delivers one alert to one channel and returns what the provider said. It is the alert path: the detail is
// the driver's own sentence, which the alert log and the notification report show. A disabled channel is not an
// error: it is the operator's own choice, reported as a refusal.
func (s *Service) Send(ctx context.Context, id string, msg Message) (ok bool, detail string, err error) {
	ok, detail, _, err = s.deliver(ctx, id, msg)
	return ok, detail, err
}

// Test sends one probe message to one channel and returns the result the panel shows. The wording is the mock's,
// which names the channel's destination hint rather than whatever the driver happened to report, because the
// operator's question is "does this channel reach the place I typed", not "what did Telegram say".
func (s *Service) Test(ctx context.Context, id string) (ok bool, detail string, err error) {
	ok, detail, ch, err := s.deliver(ctx, id, Message{
		Subject: "codeplo test notification",
		Body:    "If you can read this, the channel is configured correctly.",
		Level:   "info",
	})
	if err != nil || !ok {
		return ok, detail, err
	}
	return true, "Test message delivered to " + ch.DestinationHint, nil
}

// deliver is the shared send: open the sealed configuration, hand it to the kind's driver, and translate a driver
// failure into a result rather than an error. It returns the channel so a caller does not have to read it again.
func (s *Service) deliver(ctx context.Context, id string, msg Message) (ok bool, detail string, ch Channel, err error) {
	cfg, ch, err := s.OpenConfig(ctx, s.DB, id)
	if err != nil {
		return false, "", ch, err
	}
	if !ch.Enabled {
		return false, "Channel is disabled", ch, nil
	}
	if msg.At.IsZero() {
		msg.At = s.now()
	}
	var sendErr error
	switch ch.Kind {
	case "telegram":
		detail, sendErr = s.sendTelegram(ctx, cfg, msg)
	case "discord":
		detail, sendErr = s.sendDiscord(ctx, cfg, msg)
	case "email":
		detail, sendErr = s.sendEmail(ctx, cfg, msg)
	case "webhook":
		detail, sendErr = s.sendWebhook(ctx, cfg, msg)
	default:
		return false, "", ch, NotFound{What: "channel kind " + ch.Kind}
	}
	if sendErr != nil {
		var fe FieldErrors
		if errors.As(sendErr, &fe) {
			return false, "", ch, sendErr
		}
		// A provider failure is a result the caller records, not something that stops the evaluator: the alert
		// has to stay visible in the panel even when nobody can be told about it.
		return false, capitalize(sendErr.Error()), ch, nil
	}
	return true, detail, ch, nil
}

// Deliver fans a message out to every channel a rule names and counts what arrived. A channel that no longer
// exists is skipped rather than fatal: an alert must reach the channels that do work even when one has been
// deleted since the rule was written.
func (s *Service) Deliver(ctx context.Context, ids []string, msg Message) (Report, error) {
	rep := Report{}
	for _, id := range ids {
		ok, detail, err := s.Send(ctx, id, msg)
		if err != nil {
			var nf NotFound
			if errors.As(err, &nf) {
				continue
			}
			return rep, err
		}
		if ok {
			rep.Sent++
			continue
		}
		rep.Failed++
		if rep.Detail == "" {
			rep.Detail = detail
		}
	}
	return rep, nil
}

// capitalize only touches the first letter, because the driver errors are already sentences and the panel shows
// them as-is.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
