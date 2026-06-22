// Package mail sends GuestPass guest-invite emails. MAIL_MODE=log prints the magic link
// to stdout for dev / hermetic tests (D-2); production uses either the Resend HTTP API or
// a generic SMTP relay (STARTTLS on port 587, implicit TLS on port 465). Only
// host-initiated invites flow through here — backstage chat has no mailer and is never
// persisted or logged (EN-20).
package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	netmail "net/mail"
	"net/smtp"
	"os"
	"time"
)

// Invite is the content of one guest-invite email.
type Invite struct {
	To          string // guest email
	GuestName   string
	StreamTitle string
	MagicLink   string // BASE_URL/p/{token}
}

// Mailer sends invite emails.
type Mailer interface {
	SendInvite(ctx context.Context, inv Invite) error
}

// LogMailer prints the magic link instead of sending (D-2: MAIL_MODE=log). It is for dev
// and hermetic tests; printing the link here is the intended substitute for a mail
// provider, not a logging path — it runs only under MAIL_MODE=log, never in production.
type LogMailer struct{ w io.Writer }

// NewLogMailer writes to w, defaulting to stdout.
func NewLogMailer(w io.Writer) *LogMailer {
	if w == nil {
		w = os.Stdout
	}
	return &LogMailer{w: w}
}

func (m *LogMailer) SendInvite(_ context.Context, inv Invite) error {
	_, err := fmt.Fprintf(m.w, "[MAIL_MODE=log] invite to=%s stream=%q link=%s\n", inv.To, inv.StreamTitle, inv.MagicLink)
	return err
}

// ResendMailer posts invites to the Resend HTTP API (D-2).
type ResendMailer struct {
	apiKey  string
	from    string
	baseURL string // override for tests; defaults to the Resend API
	client  *http.Client
}

// NewResendMailer builds a Resend-backed mailer sending from the given address.
func NewResendMailer(apiKey, from string) *ResendMailer {
	return &ResendMailer{
		apiKey:  apiKey,
		from:    from,
		baseURL: "https://api.resend.com",
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

func (m *ResendMailer) SendInvite(ctx context.Context, inv Invite) error {
	body, err := json.Marshal(map[string]any{
		"from":    m.from,
		"to":      []string{inv.To},
		"subject": fmt.Sprintf("You're invited to join %q", inv.StreamTitle),
		"html":    inviteHTML(inv),
	})
	if err != nil {
		return fmt.Errorf("mail: encoding invite: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/emails", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("mail: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("mail: sending invite: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("mail: resend returned status %d", resp.StatusCode)
	}
	return nil
}

// SMTPMailer sends invites via a generic SMTP relay. Port 465 uses implicit TLS;
// port 587 (and any other port) uses STARTTLS. Gmail, Brevo, Mailgun, and most
// providers support port 587 + STARTTLS.
type SMTPMailer struct {
	host string
	port string // "587" (STARTTLS, default) or "465" (implicit TLS)
	user string
	pass string
	from string
	// serverAddr overrides host:port for tests (plain TCP, no TLS).
	serverAddr string
}

// NewSMTPMailer builds an SMTP-backed mailer. port defaults to "587" when empty.
func NewSMTPMailer(host, port, user, pass, from string) *SMTPMailer {
	if port == "" {
		port = "587"
	}
	return &SMTPMailer{host: host, port: port, user: user, pass: pass, from: from}
}

func (m *SMTPMailer) SendInvite(ctx context.Context, inv Invite) error {
	subject := fmt.Sprintf("You're invited to join %q", inv.StreamTitle)
	fromAddr, err := parseFromAddress(m.from)
	if err != nil {
		return fmt.Errorf("mail: invalid SMTP from address %q: %w", m.from, err)
	}
	msg := buildSMTPMessage(m.from, inv.To, subject, inviteHTML(inv))
	return m.send(ctx, fromAddr, inv.To, msg)
}

func (m *SMTPMailer) send(ctx context.Context, from, to string, msg []byte) error {
	addr := m.serverAddr
	if addr == "" {
		addr = net.JoinHostPort(m.host, m.port)
	}

	var conn net.Conn
	var err error
	if m.serverAddr != "" {
		// Test path: plain TCP, no TLS.
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	} else if m.port == "465" {
		conn, err = tls.DialWithDialer(&net.Dialer{}, "tcp", addr, &tls.Config{ServerName: m.host})
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("mail: smtp dial %s: %w", addr, err)
	}

	c, err := smtp.NewClient(conn, m.host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("mail: smtp client: %w", err)
	}
	defer c.Close()

	// STARTTLS on non-TLS, non-test connections (port 587 etc.).
	if m.serverAddr == "" && m.port != "465" {
		if err := c.StartTLS(&tls.Config{ServerName: m.host}); err != nil {
			return fmt.Errorf("mail: smtp starttls: %w", err)
		}
	}

	if err := c.Auth(smtp.PlainAuth("", m.user, m.pass, m.host)); err != nil {
		return fmt.Errorf("mail: smtp auth: %w", err)
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("mail: smtp MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("mail: smtp RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: smtp DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return fmt.Errorf("mail: smtp write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: smtp close data writer: %w", err)
	}
	return c.Quit()
}

// parseFromAddress extracts the bare email address from a From string which may be
// in "Display Name <addr@example.com>" or "addr@example.com" form.
func parseFromAddress(from string) (string, error) {
	a, err := netmail.ParseAddress(from)
	if err != nil {
		return "", err
	}
	return a.Address, nil
}

// buildSMTPMessage assembles a minimal RFC 5322 message with an HTML body.
func buildSMTPMessage(from, to, subject, htmlBody string) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", to)
	fmt.Fprintf(&buf, "Subject: %s\r\n", subject)
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: text/html; charset=UTF-8\r\n")
	fmt.Fprintf(&buf, "\r\n")
	fmt.Fprint(&buf, htmlBody)
	return buf.Bytes()
}

// inviteHTML renders the invite body. The guest name/link are plain text values from the
// host; html/template-grade escaping for richer bodies lands with the real templates.
func inviteHTML(inv Invite) string {
	name := inv.GuestName
	if name == "" {
		name = "there"
	}
	// "Didn't expect this? report it" link (D-42): the abuse-report form for this invite, derived
	// from the magic link (BASE_URL/p/{token}/report). The host never learns who reported them.
	reportLink := html.EscapeString(inv.MagicLink + "/report")
	return fmt.Sprintf("<p>Hi %s,</p><p>You're invited to join the live stream %q. Open this link when the stream is about to start:</p><p><a href=\"%s\">%s</a></p>%s<p>Didn't expect this invite? <a href=\"%s\">Report it</a>.</p>",
		html.EscapeString(name), html.EscapeString(inv.StreamTitle), html.EscapeString(inv.MagicLink), html.EscapeString(inv.MagicLink), invitePrivacyNotice, reportLink)
}

// invitePrivacyNotice is the GDPR "before" transparency notice carried on every invite email
// (D-37 §8 / AC-6): the guest is told, before sharing anything, that their PII is short-lived. It
// matches the wording on the guest pass page (pass.html) so the message is consistent across the
// invite surfaces. Static copy — no interpolation, so it is injection-safe.
const invitePrivacyNotice = `<p>Your name and email are used only for this stream and are deleted within 24 hours of it ending.</p>`
