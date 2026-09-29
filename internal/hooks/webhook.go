package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/policy"
)

// webhookTimeout bounds one webhook POST so a dead endpoint never holds a
// goroutine open.
const webhookTimeout = 5 * time.Second

// WebhookSink posts matching lifecycle events to policy.json's configured
// webhooks. Dispatch fires a detached goroutine per match and returns
// immediately, like a hook script.
type WebhookSink struct {
	Client *http.Client // nil -> a client with a 5s timeout
	Hooks  []policy.Webhook

	// Log is where a delivery failure is written when Runs is nil; nil ->
	// discard.
	Log io.Writer
	// Runs is the run log a failure is appended to; nil falls back to Log.
	Runs RunLog
}

var _ Dispatcher = (*WebhookSink)(nil)

// Dispatch posts to every webhook whose Events filter matches ev, each in
// its own detached goroutine.
func (s *WebhookSink) Dispatch(_ context.Context, ev Event) {
	if s == nil {
		return
	}
	for _, h := range s.Hooks {
		if matches(h, ev) {
			go s.post(h, ev)
		}
	}
}

// matches reports whether ev passes h's Events filter: empty matches
// everything; "state_changed:<state>" matches only that State; anything
// else matches on event type alone.
func matches(h policy.Webhook, ev Event) bool {
	if len(h.Events) == 0 {
		return true
	}
	for _, entry := range h.Events {
		if entry == string(ev.Type) || entry == string(ev.Type)+":"+ev.State {
			return true
		}
	}
	return false
}

type jsonPayload struct {
	Type      string    `json:"type"`
	Binding   string    `json:"binding"`
	State     string    `json:"state"`
	OldState  string    `json:"old_state"`
	Round     int       `json:"round"`
	Timestamp time.Time `json:"timestamp"`
	Text      string    `json:"text"`
}

// humanEvent renders ev as its notification word(s), e.g. "NEEDS YOU".
func humanEvent(ev Event) string {
	if ev.Type == EventStateChanged {
		return strings.ToUpper(strings.ReplaceAll(ev.State, "_", " "))
	}
	return strings.ReplaceAll(string(ev.Type), "_", " ")
}

// text renders ev as the one-line notification shared by every format.
func text(ev Event) string {
	stateSuffix := ""
	if ev.Type == EventStateChanged && ev.OldState != "" {
		stateSuffix = fmt.Sprintf(" (was %s)", ev.OldState)
	}
	return fmt.Sprintf("relevo: %s %s round %d%s", ev.BindingID, humanEvent(ev), ev.Round, stateSuffix)
}

// payload renders ev as h.Format's POST body and its content type.
func payload(h policy.Webhook, ev Event) ([]byte, string) {
	msg := text(ev)

	switch h.Format {
	case "slack":
		b, _ := json.Marshal(struct {
			Text string `json:"text"`
		}{Text: msg})
		return b, "application/json"
	case "discord":
		b, _ := json.Marshal(struct {
			Content string `json:"content"`
		}{Content: msg})
		return b, "application/json"
	default: // "json" or ""
		b, _ := json.Marshal(jsonPayload{
			Type: string(ev.Type), Binding: ev.BindingID, State: ev.State,
			OldState: ev.OldState, Round: ev.Round, Timestamp: ev.Timestamp, Text: msg,
		})
		return b, "application/json"
	}
}

// post sends one webhook POST. A failure -- a non-2xx status or a transport
// error -- is logged, never returned, since Dispatch has already moved on.
func (s *WebhookSink) post(h policy.Webhook, ev Event) {
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: webhookTimeout}
	}

	body, contentType := payload(h, ev)

	ctx, cancel := context.WithTimeout(context.Background(), webhookTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		s.logFailure(h, ev, err.Error())
		return
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := client.Do(req)
	if err != nil {
		s.logFailure(h, ev, err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.logFailure(h, ev, fmt.Sprintf("%d", resp.StatusCode))
	}
}

// logFailure records one failed delivery keyed by the URL's host only: the
// full URL is often a bearer secret, e.g. a Slack incoming-webhook path.
func (s *WebhookSink) logFailure(h policy.Webhook, ev Event, detail string) {
	host := h.URL
	if u, err := url.Parse(h.URL); err == nil && u.Host != "" {
		host = u.Host
	}

	if s.Runs != nil {
		_ = s.Runs.Append(HookRun{
			At:    time.Now().UTC(),
			Event: string(ev.Type),
			Argv:  []string{"webhook", host},
			Error: detail,
		})
		return
	}

	w := s.Log
	if w == nil {
		w = io.Discard
	}
	_, _ = fmt.Fprintf(w, "%s webhook %s %s: %s\n", time.Now().UTC().Format(time.RFC3339), host, ev.Type, detail)
}
