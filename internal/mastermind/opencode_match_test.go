package mastermind

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var matchNow = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

var matchOpencodeCases = []struct {
	name     string
	cwd      string
	sessions []OpencodeSession
	now      time.Time
	wantID   string
	wantErr  error
	checkErr func(t *testing.T, err error)
}{
	{
		name: "exact dir",
		cwd:  "/a/b",
		sessions: []OpencodeSession{
			{ID: "ses_exact", Directory: "/a/b", Updated: matchNow},
		},
		now:    matchNow,
		wantID: "ses_exact",
	},
	{
		name: "ancestor dir",
		cwd:  "/a/b/c",
		sessions: []OpencodeSession{
			{ID: "ses_ancestor", Directory: "/a/b", Updated: matchNow},
		},
		now:    matchNow,
		wantID: "ses_ancestor",
	},
	{
		name: "longest match wins over a shorter ancestor",
		cwd:  "/a/b/c/d",
		sessions: []OpencodeSession{
			{ID: "ses_short", Directory: "/a/b", Updated: matchNow},
			{ID: "ses_long", Directory: "/a/b/c", Updated: matchNow},
		},
		now:    matchNow,
		wantID: "ses_long",
	},
	{
		name: "/a/bc is not under /a/b",
		cwd:  "/a/bc",
		sessions: []OpencodeSession{
			{ID: "ses_other", Directory: "/a/b", Updated: matchNow},
		},
		now:     matchNow,
		wantErr: ErrNoOpencodeSession,
	},
	{
		name: "child session (ParentID set) ignored",
		cwd:  "/a/b",
		sessions: []OpencodeSession{
			{ID: "ses_child", Directory: "/a/b", ParentID: "ses_parent", Updated: matchNow},
		},
		now:     matchNow,
		wantErr: ErrNoOpencodeSession,
	},
	{
		name: "archived ignored",
		cwd:  "/a/b",
		sessions: []OpencodeSession{
			{ID: "ses_archived", Directory: "/a/b", Archived: true, Updated: matchNow},
		},
		now:     matchNow,
		wantErr: ErrNoOpencodeSession,
	},
	{
		name:     "none -> errors.Is ErrNoOpencodeSession",
		cwd:      "/a/b",
		sessions: []OpencodeSession{},
		now:      matchNow,
		wantErr:  ErrNoOpencodeSession,
	},
	{
		name: "two within 60 s -> ErrAmbiguousOpencodeSession naming both titles",
		cwd:  "/a/b",
		sessions: []OpencodeSession{
			{ID: "ses_1", Directory: "/a/b", Title: "first title", Updated: matchNow},
			{ID: "ses_2", Directory: "/a/b", Title: "second title", Updated: matchNow.Add(-30 * time.Second)},
		},
		now: matchNow,
		checkErr: func(t *testing.T, err error) {
			var amb ErrAmbiguousOpencodeSession
			if !errors.As(err, &amb) {
				t.Fatalf("expected ErrAmbiguousOpencodeSession, got %v", err)
			}
			msg := amb.Error()
			if !strings.Contains(msg, "first title") || !strings.Contains(msg, "second title") {
				t.Errorf("error message %q does not contain both titles", msg)
			}
			if len(amb.Titles) != 2 || amb.Titles[0] != "first title" || amb.Titles[1] != "second title" {
				t.Errorf("titles = %v, want [first title, second title]", amb.Titles)
			}
		},
	},
	{
		name: "two where the second is 61 s old -> the newest id",
		cwd:  "/a/b",
		sessions: []OpencodeSession{
			{ID: "ses_1", Directory: "/a/b", Title: "first title", Updated: matchNow},
			{ID: "ses_2", Directory: "/a/b", Title: "second title", Updated: matchNow.Add(-61 * time.Second)},
		},
		now:    matchNow,
		wantID: "ses_1",
	},
	{
		name: "the only candidate is 11 minutes idle -> ErrNoOpencodeSession",
		cwd:  "/a/b",
		sessions: []OpencodeSession{
			{ID: "ses_idle", Directory: "/a/b", Title: "idle", Updated: matchNow.Add(-11 * time.Minute)},
		},
		now:     matchNow,
		wantErr: ErrNoOpencodeSession,
	},
	{
		name: "idle exact dir is discarded, so an active ancestor wins",
		cwd:  "/a/b/c",
		sessions: []OpencodeSession{
			{ID: "ses_ancestor", Directory: "/a/b", Title: "ancestor", Updated: matchNow.Add(-1 * time.Minute)},
			{ID: "ses_exact", Directory: "/a/b/c", Title: "exact", Updated: matchNow.Add(-2 * time.Hour)},
		},
		now:    matchNow,
		wantID: "ses_ancestor",
	},
	{
		name: "idle exact dir and idle ancestor -> ErrNoOpencodeSession",
		cwd:  "/a/b/c",
		sessions: []OpencodeSession{
			{ID: "ses_ancestor", Directory: "/a/b", Title: "ancestor", Updated: matchNow.Add(-2 * time.Hour)},
			{ID: "ses_exact", Directory: "/a/b/c", Title: "exact", Updated: matchNow.Add(-2 * time.Hour)},
		},
		now:     matchNow,
		wantErr: ErrNoOpencodeSession,
	},
}

func TestMatchOpencodeSession(t *testing.T) {
	for _, tc := range matchOpencodeCases {
		t.Run(tc.name, func(t *testing.T) {
			gotID, err := MatchOpencodeSession(tc.cwd, tc.sessions, tc.now)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if tc.checkErr != nil {
				tc.checkErr(t, err)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotID != tc.wantID {
				t.Errorf("got id %q, want %q", gotID, tc.wantID)
			}
		})
	}
}
