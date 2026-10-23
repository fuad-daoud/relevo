package relevo

import (
	"errors"
	"strings"
	"testing"
)

func okFacts() HandoverFacts {
	return HandoverFacts{
		Name: "webshop", To: "zen", This: "laptop-id", SyncEnabled: true,
		Installations: []InstallationFact{
			{ID: "laptop-id", Label: "laptop", NewestSchema: 31},
			{ID: "zen-id", Label: "zen", NewestSchema: 31},
		},
	}
}

// Each refusal is one sentence the owner can act on, and the first match wins.
func TestHandoverRefusalNamesEachReason(t *testing.T) {
	t.Parallel()
	chainErr := errors.New("binding \"webshop\" belongs to running chain c")
	cases := []struct {
		name string
		edit func(*HandoverFacts)
		want string
	}{
		{"sync off", func(f *HandoverFacts) { f.SyncEnabled = false }, "sync is off"},
		{"locked", func(f *HandoverFacts) { f.Locked = true }, "locked"},
		{"server binding", func(f *HandoverFacts) { f.Server = true }, "runs on a server"},
		{"open round", func(f *HandoverFacts) { f.RoundOpen, f.Round = true, 3 }, "round 3 open"},
		{"running chain member", func(f *HandoverFacts) { f.ChainErr = chainErr }, "running chain"},
		{"dirty worktree", func(f *HandoverFacts) { f.Dirty = true }, "uncommitted changes"},
		{"commits the push cannot carry", func(f *HandoverFacts) { f.Unpushable = "the checkout has no remote named origin" }, "push cannot carry"},
		{"to is this machine by id", func(f *HandoverFacts) { f.To = "laptop-id" }, "names this machine"},
		{"to is this machine by label", func(f *HandoverFacts) { f.To = "laptop" }, "names this machine"},
		{"unknown installation", func(f *HandoverFacts) { f.To = "mars" }, "matches no installation"},
		{"missing to", func(f *HandoverFacts) { f.To = "" }, "needs --to"},
		{"target below schema 31", func(f *HandoverFacts) {
			f.Installations[1].NewestSchema = 30
		}, "schema 31"},
		{"target never seen", func(f *HandoverFacts) { f.Installations[1].NewestSchema = 0 }, "schema 31"},
		{"already handed to another machine", func(f *HandoverFacts) { f.HandedTo = "third-id" }, "already handed over to third-id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := okFacts()
			tc.edit(&f)
			_, err := HandoverRefusal(f)
			if err == nil {
				t.Fatalf("got no refusal, want one naming %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestHandoverRefusalAcceptsIDAndUniqueLabel(t *testing.T) {
	t.Parallel()
	for _, to := range []string{"zen", "zen-id"} {
		f := okFacts()
		f.To = to
		got, err := HandoverRefusal(f)
		if err != nil || got.ID != "zen-id" || got.Already {
			t.Errorf("--to %s = (%+v, %v), want zen-id and no refusal", to, got, err)
		}
	}
}

// The laptop and zen report the same hostname, so a label can match two
// installations: the refusal lists the ids, and an id still resolves.
func TestHandoverRefusalRefusesAmbiguousLabelWithIDs(t *testing.T) {
	t.Parallel()
	f := okFacts()
	f.Installations = append(f.Installations, InstallationFact{ID: "zen2-id", Label: "zen", NewestSchema: 31})
	_, err := HandoverRefusal(f)
	if err == nil || !strings.Contains(err.Error(), "zen-id") || !strings.Contains(err.Error(), "zen2-id") {
		t.Fatalf("ambiguous label refusal = %v, want both ids listed", err)
	}
	f.To = "zen2-id"
	if got, err := HandoverRefusal(f); err != nil || got.ID != "zen2-id" {
		t.Errorf("by id = (%+v, %v), want zen2-id", got, err)
	}
}

// A rerun against a binding already given to the same machine is idempotent;
// the record is gone from this machine, so the binding-state rules do not apply.
func TestHandoverRefusalRerunWithSameTargetIsIdempotent(t *testing.T) {
	t.Parallel()
	f := okFacts()
	f.HandedTo = "zen-id"
	f.Dirty, f.RoundOpen = true, true
	got, err := HandoverRefusal(f)
	if err != nil || !got.Already {
		t.Fatalf("rerun = (%+v, %v), want Already and no refusal", got, err)
	}
}

func TestResumeRefusalSaysWhereTheBindingIs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		f    ResumeFacts
		want string
	}{
		{"mine", ResumeFacts{Name: "w", Mine: true}, ""},
		{"nothing known", ResumeFacts{Name: "w"}, ""},
		{"handed over from here", ResumeFacts{Name: "w", LeftTo: "zen"}, "handed over to zen"},
		{"written elsewhere, not imported", ResumeFacts{Name: "w", ForeignWriter: "laptop-id"}, "not handed to this machine yet"},
	}
	for _, tc := range cases {
		err := ResumeRefusal(tc.f)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: refused: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestSameRepoComparesNormalisedURLs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want bool
	}{
		{"https://github.com/acme/shop.git", "git@github.com:acme/shop.git", true},
		{"https://github.com/acme/shop", "https://github.com/acme/shop.git", true},
		{"https://github.com/acme/shop", "https://github.com/acme/other", false},
		{"", "", false},
		{"https://github.com/acme/shop", "", false},
	}
	for _, tc := range cases {
		if got := SameRepo(tc.a, tc.b); got != tc.want {
			t.Errorf("SameRepo(%q, %q) = %t, want %t", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestTakeoverPlanRules(t *testing.T) {
	t.Parallel()
	ok := TakeoverFacts{Name: "w", This: "me", Force: true, SyncEnabled: true, RootFound: true, Writer: "old", Mark: 7}
	cases := []struct {
		name string
		edit func(*TakeoverFacts)
		want string
	}{
		{"sync off", func(f *TakeoverFacts) { f.SyncEnabled = false }, "sync is off"},
		{"locked", func(f *TakeoverFacts) { f.Locked = true }, "locked"},
		{"no force", func(f *TakeoverFacts) { f.Force = false }, "--force"},
		{"no root", func(f *TakeoverFacts) { f.RootFound = false }, "db sync pull"},
		{"this machine writes it", func(f *TakeoverFacts) { f.Writer = "me" }, "already writes"},
	}
	for _, tc := range cases {
		f := ok
		tc.edit(&f)
		if _, err := TakeoverPlan(f); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	dec, err := TakeoverPlan(ok)
	if err != nil || dec.OldWriter != "old" || dec.Upto != 7 {
		t.Errorf("plan = (%+v, %v), want old writer and the import mark 7 as the fence", dec, err)
	}
	f := ok
	f.Mark = 0
	if dec, err := TakeoverPlan(f); err != nil || dec.Upto != 0 {
		t.Errorf("a taker that never imported the writer but holds the root = (%+v, %v), want fence 0", dec, err)
	}
}

func TestHandoverRefusalRefusesARemoteThatIsNotOrigins(t *testing.T) {
	t.Parallel()
	const https = "https://github.com/acme/webshop.git"
	cases := []struct {
		name                         string
		remote, remoteURL, originURL string
		want                         string
	}{
		{"origin by name", "origin", "", "", ""},
		{"same URL in ssh and https form", "mirror", "git@github.com:acme/webshop.git", https, ""},
		{"different URL", "mirror", "https://github.com/other/webshop.git", https, "without --remote"},
		{"empty origin URL", "mirror", https, "", "origin"},
		{"unreadable remote URL", "mirror", "", https, "origin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := okFacts()
			f.Remote, f.RemoteURL, f.OriginURL = tc.remote, tc.remoteURL, tc.originURL
			_, err := HandoverRefusal(f)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "without --remote") {
				t.Fatalf("refusal = %v, want it to contain %q and the way forward", err, tc.want)
			}
		})
	}
}
