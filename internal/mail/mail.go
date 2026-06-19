// Package mail sends GuestPass guest-invite emails. MAIL_MODE=log prints the magic link
// to stdout for dev / hermetic tests (D-2); production posts to the Resend HTTP API (one
// POST, no SMTP). Only host-initiated invites flow through here — backstage chat has no
// mailer and is never persisted or logged (EN-20).
package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
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
