package ui

import (
	"encoding/json"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fuad-daoud/relevo/internal/db"
)

// prefs is what the ui's preference document holds (§4.6, reduced in round
// 2): the sort order, and the dashboard's applied query and sort column. It
// lives in the machine database's kv row "ui" (or "serve.ui" for the server
// ui) -- the file ui.json before an earlier round -- written by the ui alone
// and read by nothing else.
//
// compact, rail_cols and scope are gone (X1–X3): encoding/json ignores the
// unknown fields of an older document, so an old prefs JSON still loads.
type prefs struct {
	Sort string `json:"sort"` // "attention" | "name"
	// Dashboard is the :rounds view's applied query text and its sort
	// column (docs/specs/2026-09-21-dashboard-design.md §6). Empty means
	// the defaults: every round, newest first.
	Dashboard     string `json:"dashboard,omitempty"`
	DashboardSort string `json:"dashboard_sort,omitempty"`
}

// PrefsStore is where the ui's preferences live (P3b plan §4.4): the kv row Key
// in KV. A zero PrefsStore -- KV nil -- keeps the ui stateless, nothing loaded,
// nothing saved, exactly as an empty Options.PrefsPath did.
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
