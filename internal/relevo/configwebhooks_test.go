package relevo

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/policy"
)

// webhookPolicyDoc is policyTestDoc over the real policy body the settings
// tests share, so an edit has real stored keys to keep.
func webhookPolicyDoc(t *testing.T, raw string) ConfigDoc {
	t.Helper()
	doc := policyTestDoc(t)
	p, _, err := policy.Parse(config.FileName(config.Policy), []byte(raw))
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}
	doc.Policy = p
	doc.PolicyRaw = json.RawMessage(raw)
	return doc
}

// webhookBody is a ConfigEdit's policy body decoded for assertions.
func webhookBody(t *testing.T, edit ConfigEdit) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(edit.Sections[config.Policy], &m); err != nil {
		t.Fatalf("unmarshal policy body: %v", err)
	}
	return m
}

// a. Adding one slack hook stores notify.webhooks[0] with its url and format,
// and keeps the policy's other keys.
func TestSetWebhooksStoresSlackHook(t *testing.T) {
	const raw = `{"max_switches":2,"max_tier":"yolo","serve":{"scope":{"slice":"relevo.slice"}}}`
	doc := webhookPolicyDoc(t, raw)

	edit, err := SetWebhooks(doc, []policy.Webhook{{URL: "https://example.com/hook", Format: "slack"}}, "add webhook https://example.com/hook")
	if err != nil {
		t.Fatalf("SetWebhooks: %v", err)
	}
	m := webhookBody(t, edit)

	notify, ok := m["notify"].(map[string]any)
	if !ok {
		t.Fatalf("notify is not a map: %v", m["notify"])
	}
	hooks, ok := notify["webhooks"].([]any)
	if !ok || len(hooks) != 1 {
		t.Fatalf("webhooks = %v, want one", notify["webhooks"])
	}
	first, ok := hooks[0].(map[string]any)
	if !ok {
		t.Fatalf("webhooks[0] is not a map: %v", hooks[0])
	}
	if first["url"] != "https://example.com/hook" || first["format"] != "slack" {
		t.Errorf("webhooks[0] = %v, want the url and slack format", first)
	}
	if _, exists := first["events"]; exists {
		t.Errorf("webhooks[0] = %v, want no events key for an empty filter", first)
	}
	if m["max_switches"] == nil || m["max_tier"] != "yolo" {
		t.Errorf("body = %v, want the other keys kept", m)
	}
}

// b. A json format and an empty event list store only the url.
func TestSetWebhooksJSONFormatStoresOnlyURL(t *testing.T) {
	doc := webhookPolicyDoc(t, `{"max_switches":2,"max_tier":"yolo"}`)

	edit, err := SetWebhooks(doc, []policy.Webhook{{URL: "https://example.com/hook", Format: "json"}}, "add webhook https://example.com/hook")
	if err != nil {
		t.Fatalf("SetWebhooks: %v", err)
	}
	m := webhookBody(t, edit)
	hooks := m["notify"].(map[string]any)["webhooks"].([]any)
	first := hooks[0].(map[string]any)
	if len(first) != 1 || first["url"] != "https://example.com/hook" {
		t.Errorf("webhooks[0] = %v, want only the url", first)
	}
}

// c. Setting the list back to empty removes the notify key.
func TestSetWebhooksEmptyListRemovesNotify(t *testing.T) {
	doc := webhookPolicyDoc(t, `{"max_switches":2,"max_tier":"yolo","notify":{"webhooks":[{"url":"https://example.com/hook"}]}}`)

	edit, err := SetWebhooks(doc, nil, "delete webhook https://example.com/hook")
	if err != nil {
		t.Fatalf("SetWebhooks: %v", err)
	}
	m := webhookBody(t, edit)
	if _, exists := m["notify"]; exists {
		t.Errorf("notify still present: %v", m["notify"])
	}
	if m["max_switches"] == nil {
		t.Errorf("body = %v, want the other keys kept", m)
	}
}

// d. An ftp url is a FieldError, the way any policy edit reports a bad url.
func TestSetWebhooksBadURLIsFieldError(t *testing.T) {
	doc := webhookPolicyDoc(t, `{"max_switches":2,"max_tier":"yolo"}`)

	_, err := SetWebhooks(doc, []policy.Webhook{{URL: "ftp://example.com/hook"}}, "add webhook ftp://example.com/hook")
	var fe *FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want *FieldError", err)
	}
}
