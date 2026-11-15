package sync

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TestParseSettingsHistoryModes pins the section's reading modes: none reads
// back as none, full and the absent field are the default, and a typo is
// refused with db.ErrInvalid rather than read back as the default.
func TestParseSettingsHistoryModes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		body    string
		history string
		none    bool
		refused bool
	}{
		{body: `{}`, history: "", none: false},
		{body: `{"history":""}`, history: "", none: false},
		{body: `{"history":"full"}`, history: HistoryFull, none: false},
		{body: `{"history":"none"}`, history: HistoryNone, none: true},
		{body: `{"history":"all"}`, refused: true},
		{body: `{"history":"None"}`, refused: true},
		{body: `{"history":"none "}`, refused: true},
	} {
		st, err := ParseSettings([]byte(tc.body))
		if tc.refused {
			if err == nil {
				t.Errorf("ParseSettings(%s) accepted, want a refusal", tc.body)
				continue
			}
			if !errors.Is(err, db.ErrInvalid) {
				t.Errorf("ParseSettings(%s) = %v, want db.ErrInvalid", tc.body, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSettings(%s): %v", tc.body, err)
			continue
		}
		if st.History != tc.history {
			t.Errorf("ParseSettings(%s).History = %q, want %q", tc.body, st.History, tc.history)
		}
		if st.HistoryNone() != tc.none {
			t.Errorf("ParseSettings(%s).HistoryNone() = %v, want %v", tc.body, st.HistoryNone(), tc.none)
		}
	}
}

// TestParseHistoryStoredForm pins the stored form each mode maps to -- full
// stored as the empty string, none as none -- and that an unknown mode is
// refused with db.ErrInvalid and the fix named.
func TestParseHistoryStoredForm(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode    string
		stored  string
		refused bool
	}{
		{mode: "", stored: ""},
		{mode: HistoryFull, stored: ""},
		{mode: HistoryNone, stored: HistoryNone},
		{mode: "all", refused: true},
		{mode: "FULL", refused: true},
		{mode: "none ", refused: true},
	} {
		got, err := ParseHistory(tc.mode)
		if tc.refused {
			if err == nil {
				t.Errorf("ParseHistory(%q) accepted, want a refusal", tc.mode)
				continue
			}
			if !errors.Is(err, db.ErrInvalid) {
				t.Errorf("ParseHistory(%q) = %v, want db.ErrInvalid", tc.mode, err)
			}
			if !strings.Contains(err.Error(), "want full or none") {
				t.Errorf("ParseHistory(%q) = %v, want the fix named", tc.mode, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseHistory(%q): %v", tc.mode, err)
			continue
		}
		if got != tc.stored {
			t.Errorf("ParseHistory(%q) = %q, want %q", tc.mode, got, tc.stored)
		}
	}
}

// TestDefaultSectionOmitsHistory pins that a default machine's section carries
// no history key: full is stored as the empty string, so the bytes stay the
// ones an older binary's DisallowUnknownFields parse already reads.
func TestDefaultSectionOmitsHistory(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(Settings{Enabled: true})
	if err != nil {
		t.Fatalf("marshal the default section: %v", err)
	}
	if strings.Contains(string(body), "history") {
		t.Errorf("default section = %s, want no history key", body)
	}
	st, err := ParseSettings(body)
	if err != nil {
		t.Fatalf("ParseSettings of the default section: %v", err)
	}
	if st.HistoryNone() {
		t.Error("the default section reads as none, want full")
	}
}

// TestHistoryNoneReadsOnlyTheStoredNone pins the method against the stored
// forms: none is none, and both spellings of full are not.
func TestHistoryNoneReadsOnlyTheStoredNone(t *testing.T) {
	t.Parallel()
	if !(Settings{History: HistoryNone}).HistoryNone() {
		t.Error("History none does not read as none")
	}
	if (Settings{History: HistoryFull}).HistoryNone() {
		t.Error("History full reads as none")
	}
	if (Settings{}).HistoryNone() {
		t.Error("the empty History reads as none, want the full default")
	}
}
