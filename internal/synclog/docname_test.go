package synclog

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDocNameReadsTheDocumentAfterThePrefix(t *testing.T) {
	t.Parallel()
	if name, ok := DocName("doc:config"); name != "config" || !ok {
		t.Errorf("DocName(doc:config) = %q, %t", name, ok)
	}
	if name, ok := DocName("binding"); name != "" || ok {
		t.Errorf("DocName(binding) = %q, %t; want not a doc", name, ok)
	}
}

func TestDocBodyHashIgnoresFieldOrderAndRefusesAMalformedBody(t *testing.T) {
	t.Parallel()
	a, err := docBodyHash(json.RawMessage(`{"stamp":"5","value":{"x":1}}`))
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	b, err := docBodyHash(json.RawMessage(`{"value":{"x":1},"stamp":"5"}`))
	if err != nil || a != b {
		t.Errorf("hash of the reordered body = %q, %v; want %q", b, err, a)
	}
	c, err := docBodyHash(json.RawMessage(`{"stamp":"6","value":{"x":1}}`))
	if err != nil || c == a {
		t.Errorf("hash of another stamp = %q, %v; want it to differ from %q", c, err, a)
	}
	if _, err := docBodyHash(json.RawMessage(`{"stamp":"5"}`)); !errors.Is(err, ErrInvalid) {
		t.Errorf("hash of a body with no value = %v, want ErrInvalid", err)
	}
}

func TestDocEntryBuildersRefuseWhatCannotRoundTrip(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	if _, err := NewDocUpsert("m1", nil, "k", 1, 1, json.RawMessage(`1`), at); !errors.Is(err, ErrInvalid) {
		t.Errorf("no doc = %v, want ErrInvalid", err)
	}
	if _, err := NewDocUpsert("m1", newFakeDoc("a:b"), "k", 1, 1, json.RawMessage(`1`), at); !errors.Is(err, ErrInvalid) {
		t.Errorf("a doc name with a separator = %v, want ErrInvalid", err)
	}
	if _, err := NewDocDelete("m1", newFakeDoc("config"), "", 1, 1, at); !errors.Is(err, ErrInvalid) {
		t.Errorf("an empty key = %v, want ErrInvalid", err)
	}
	e, err := NewDocDelete("m1", newFakeDoc("config"), "k", 1, 7, at)
	if err != nil {
		t.Fatalf("NewDocDelete: %v", err)
	}
	if e.Table != "doc:config" || e.Op != OpUpsert || !strings.Contains(string(e.Body), `"value":null`) {
		t.Errorf("delete entry = %+v, want an upsert carrying a null value", e)
	}
}

func TestKeyValueBindsNumbersAsTheirColumnsHoldThem(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   any
		want any
	}{
		{json.Number("7"), int64(7)},
		{json.Number("1.5"), 1.5},
		{json.Number("1e999"), "1e999"},
		{"text", "text"},
		{true, true},
	}
	for _, c := range cases {
		if got := keyValue(c.in); got != c.want {
			t.Errorf("keyValue(%v) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestDocForFindsOnlyTheDocumentsItIsBoundTo(t *testing.T) {
	t.Parallel()
	bound := newFakeDoc("config")
	var k keyed
	k.UseDocs(bound)
	if got, err := k.docFor("doc:config"); err != nil || got != SharedDoc(bound) {
		t.Errorf("docFor(doc:config) = %v, %v; want the bound doc", got, err)
	}
	if _, err := k.docFor("doc:other"); !errors.Is(err, ErrInvalid) {
		t.Errorf("docFor of an unbound doc = %v, want ErrInvalid", err)
	}
	if _, err := k.docFor("binding"); !errors.Is(err, ErrInvalid) {
		t.Errorf("docFor of a table with no prefix = %v, want ErrInvalid", err)
	}
	if got, ok := k.docNamed("config"); !ok || got != SharedDoc(bound) {
		t.Errorf("docNamed(config) = %v, %t", got, ok)
	}
	if _, ok := k.docNamed("other"); ok {
		t.Error("docNamed found a doc it is not bound to")
	}
}

func TestDocNamedFallsBackToTheRegistry(t *testing.T) {
	doc := newFakeDoc("config")
	registerDoc(t, doc)
	var k keyed
	if got, ok := k.docNamed("config"); !ok || got != SharedDoc(doc) {
		t.Errorf("docNamed(config) = %v, %t; want the registered doc", got, ok)
	}
	if got, err := k.docFor("doc:config"); err != nil || got != SharedDoc(doc) {
		t.Errorf("docFor(doc:config) = %v, %v; want the registered doc", got, err)
	}
	if _, ok := k.docNamed("absent"); ok {
		t.Error("docNamed found a doc nobody registered")
	}
}
