package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// sendEmail delivers over SMTP. The configuration is sealed like every other credential here, and the plaintext
// password only ever reaches the SMTP client, never a log or a response.
//
// TLS is the default and is only skipped when the operator writes it explicitly: a channel that silently sends
// a bot token or an alert over a plain connection is worse than one that fails with a clear message.
func (s *Service) sendEmail(ctx context.Context, cfg Config, msg Message) (string, error) {
	host, from, to := strings.TrimSpace(cfg["host"]), strings.TrimSpace(cfg["from"]), strings.TrimSpace(cfg["to"])
	switch {
	case host == "":
		return "", FieldErrors{"config.host": "Enter the SMTP host"}
	case from == "":
		return "", FieldErrors{"config.from": "Enter the sender address"}
	}
	port := strings.TrimSpace(cfg["port"])
	if port == "" {
		port = "587"
	}
	if !validPort(port) {
		return "", FieldErrors{"config.port": "Port must be 1-65535"}
	}
	user, pass := strings.TrimSpace(cfg["username"]), cfg["password"]
	mode := strings.ToLower(strings.TrimSpace(cfg["tls"]))
	if mode == "" {
		mode = "starttls"
	}
	if mode == "smtps" {
		mode = "ssl"
	}
	switch mode {
	case "starttls", "ssl", "plain":
	default:
		return "", FieldErrors{"config.tls": "TLS must be starttls, ssl or plain"}
	}

	subject := orDefault(strings.TrimSpace(msg.Subject), "codeplo notification")
	at := msg.At
	if at.IsZero() {
		at = s.now()
	}
	body := buildMail(from, to, subject, plainText(msg), at)

	dialer := &net.Dialer{Timeout: s.timeout()}
	connCtx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	c, err := dialSMTP(connCtx, dialer, host, port, mode)
	if err != nil {
		return "", fmt.Errorf("delivery failed: %w", err)
	}
	defer c.Close()

	if mode == "starttls" {
		tlsCfg := &tls.Config{ServerName: serverName(host), MinVersion: tls.VersionTLS12}
		if err := c.StartTLS(tlsCfg); err != nil {
			return "", fmt.Errorf("delivery failed: starttls: %w", err)
		}
	}
	if user != "" && strings.TrimSpace(cfg["password"]) != "" {
		if err := c.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
			return "", fmt.Errorf("delivery failed: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return "", fmt.Errorf("delivery failed: sender: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return "", fmt.Errorf("delivery failed: recipient: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return "", fmt.Errorf("delivery failed: %w", err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		return "", fmt.Errorf("delivery failed: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("delivery failed: %w", err)
	}
	_ = c.Quit()
	return "delivered to " + maskAddress(to), nil
}

// dialSMTP opens the TCP (or TLS) connection to the server. "ssl" means the implicit-TLS port, usually 465,
// where the conversation starts encrypted; "starttls" and "plain" both begin with a cleartext socket and the
// caller upgrades it afterwards.
func dialSMTP(ctx context.Context, dialer *net.Dialer, host, port, mode string) (*smtp.Client, error) {
	addr := net.JoinHostPort(host, port)
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	name := serverName(host)
	if mode == "ssl" {
		return smtp.NewClient(tls.Client(conn, &tls.Config{ServerName: name, MinVersion: tls.VersionTLS12}), name)
	}
	return smtp.NewClient(conn, name)
}

// serverName is the TLS server name for a host that may be written as a bare IPv6 literal.
func serverName(host string) string {
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}

func buildMail(from, to, subject, text string, at time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Date: %s\r\n", at.UTC().Format(time.RFC1123Z))
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("MIME-Version: 1.0\r\n\r\n")
	b.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}
