package classify

import (
	"context"
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/policy"
)

type resolveCase struct {
	name           string
	cfg            *policy.Classify
	key            string
	getenv         func(string) string
	wantConfigured bool
	wantKeySource  string
	wantModel      string
	wantClientKey  string
	wantUnavail    bool
}

func envKey(v string) func(string) string {
	return func(key string) string {
		if key == "TYPESAFE_API_KEY" {
			return v
		}
		return ""
	}
}

var resolveCases = []resolveCase{
	{
		name:   "nil cfg -> nil classifier, Configured false",
		getenv: osGetenv,
	},
	{
		name:           "env key wins and is trimmed",
		cfg:            &policy.Classify{Provider: "jev"},
		getenv:         envKey("  test-env-key  \n"),
		wantConfigured: true,
		wantKeySource:  "env",
		wantModel:      "jev-latest",
		wantClientKey:  "test-env-key",
	},
	{
		name:           "db key is trimmed",
		cfg:            &policy.Classify{Provider: "jev", Model: "custom-model"},
		key:            "  db-secret-key  \n",
		getenv:         osGetenv,
		wantConfigured: true,
		wantKeySource:  "db",
		wantModel:      "custom-model",
		wantClientKey:  "db-secret-key",
	},
	{
		name:           "env key wins over the db key",
		cfg:            &policy.Classify{Provider: "jev"},
		key:            "db-secret-key",
		getenv:         envKey("env-secret-key"),
		wantConfigured: true,
		wantKeySource:  "env",
		wantModel:      "jev-latest",
		wantClientKey:  "env-secret-key",
	},
	{
		name:           "no key -> Unavailable",
		cfg:            &policy.Classify{Provider: "jev"},
		getenv:         osGetenv,
		wantConfigured: true,
		wantUnavail:    true,
	},
	{
		name:           "blank db key is no key",
		cfg:            &policy.Classify{Provider: "jev"},
		key:            "   \n\t  \n",
		getenv:         osGetenv,
		wantConfigured: true,
		wantUnavail:    true,
	},
}

func TestResolve(t *testing.T) {
	for _, tc := range resolveCases {
		t.Run(tc.name, func(t *testing.T) {
			cls, st := Resolve(tc.cfg, tc.key, tc.getenv)
			assertResolve(t, tc, cls, st)
		})
	}
}

func assertResolve(t *testing.T, tc resolveCase, cls Classifier, st Status) {
	t.Helper()
	if st.Configured != tc.wantConfigured {
		t.Errorf("Configured = %v, want %v", st.Configured, tc.wantConfigured)
	}
	if st.KeySource != tc.wantKeySource {
		t.Errorf("KeySource = %q, want %q", st.KeySource, tc.wantKeySource)
	}
	if tc.wantModel != "" && st.Model != tc.wantModel {
		t.Errorf("Model = %q, want %q", st.Model, tc.wantModel)
	}

	switch {
	case tc.wantClientKey != "":
		client, ok := cls.(*Client)
		if !ok {
			t.Fatalf("expected *Client, got %T", cls)
		}
		if client.Key != tc.wantClientKey {
			t.Errorf("client.Key = %q, want %q", client.Key, tc.wantClientKey)
		}
		if client.Model != tc.wantModel {
			t.Errorf("client.Model = %q, want %q", client.Model, tc.wantModel)
		}
	case tc.wantUnavail:
		unavail, ok := cls.(Unavailable)
		if !ok {
			t.Fatalf("expected Unavailable, got %T", cls)
		}
		if unavail.Reason != "no classifier key" {
			t.Errorf("Reason = %q, want %q", unavail.Reason, "no classifier key")
		}
	default:
		if cls != nil {
			t.Errorf("expected nil classifier, got %T", cls)
		}
	}
}

func TestUnavailableJudge(t *testing.T) {
	_, err := Unavailable{Reason: "no classifier key"}.Judge(context.Background(), Request{})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("expected ErrUnavailable, got %v", err)
	}
}

func osGetenv(string) string { return "" }
