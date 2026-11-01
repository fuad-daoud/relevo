package ui

import (
	"encoding/json"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fuad-daoud/relevo/internal/db"
)

// prefs is what the ui's preference document holds: the sort order, and
// the dashboard's applied query and sort column. It lives in the machine
// database's kv row "ui" (or "serve.ui" for the server ui) -- written by
// the ui alone and read by nothing else. An older document's extra fields are
// ignored by encoding/json, so it still loads.
type prefs struct {
	Sort string `json:"sort"` // "attention" | "name"
	// Dashboard is the :rounds view's applied query text and its sort
	// column. Empty means
	// the defaults: every round, newest first.
	Dashboard     string `json:"dashboard,omitempty"`
	DashboardSort string `json:"dashboard_sort,omitempty"`
}

// PrefsStore is where the ui's preferences live: the kv row Key
// in KV. A zero PrefsStore -- KV nil -- keeps the ui stateless, nothing loaded,
// nothing saved.
type PrefsStore struct {
	KV  db.KV
	Key string
}

type prefsSavedMsg struct{}

// loadPrefs reads the KV row; any error -- missing, unreadable, not JSON -- is
// the zero prefs, which the shell reads as the defaults. Never errors: a
// preference record is not worth refusing to start over.
func loadPrefs(ps PrefsStore) prefs {
	if ps.KV == nil {
		return prefs{}
	}
	data, ok, err := ps.KV.KVGet(ps.Key)
	if err != nil || !ok {
		return prefs{}
	}
	var p prefs
	if json.Unmarshal(data, &p) != nil {
		return prefs{}
	}
	return p
}

// savePrefs writes p to the KV row from inside the command, so Update stays
// pure. A failed save is silent: the change still applies for this run.
func savePrefs(ps PrefsStore, p prefs) tea.Cmd {
	return func() tea.Msg {
		if ps.KV == nil {
			return prefsSavedMsg{}
		}
		data, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			return prefsSavedMsg{}
		}
		_ = ps.KV.KVPut(ps.Key, append(data, '\n'))
		return prefsSavedMsg{}
	}
}
