package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// guideRepoState resolves the repository's answer and label: a known
// repository is unset until the database says otherwise, and the label is the
// origin URL or the directory. It also opens the database the session half
// reads.
func guideRepoState(ctx context.Context, rt relevo.Runtime, cwd string) (db.Consent, string, *db.DB) {
	state := db.ConsentNo
	ref := mastermindRepoOf(ctx, rt, cwd)
	d, _ := rt.Store.DB()
	if !mastermindRepoKnown(ref) {
		return state, "", d
	}
	label := ""
	if ref.OriginURL != nil {
		label = *ref.OriginURL
	} else {
		label = *ref.CommonDir
	}
	state = db.ConsentUnset
	if d != nil {
		if c, err := d.RepoConsent(ref); err == nil {
			state = c
		}
	}
	return state, label, d
}

// guideSessionRecord resolves the session's own answer and reports its
// record, for every state but no: a session that answered for itself
// (`relevo mastermind enable`) is briefed whatever the repository says, and
// one that answered no is not briefed at all. An enabled session creates the
// record here, so the identity sentence names the MasterMind the TUI shows; a
// registration failure leaves the text without the sentence rather than
// failing the session.
func guideSessionRecord(rt relevo.Runtime, d *db.DB, cwd, kind, session string, repoState db.Consent) (db.Consent, *mastermind.Record) {
	sessionState := db.ConsentUnset
	if kind != "" && session != "" && d != nil {
		if c, err := d.SessionConsent(kind, session); err == nil {
			sessionState = c
		}
	}
	state := mastermind.EffectiveConsent(sessionState, repoState)

	var rec *mastermind.Record
	if kind != "" && session != "" && state != db.ConsentNo {
		if reg, err := mastermindRegistry(rt); err == nil {
			if r, err := reg.BySession(kind, session); err == nil {
				rec = &r
			}
			if state == db.ConsentYes && rec == nil {
				in := mastermind.InitInput{Kind: kind, SessionID: session, CWD: cwd, Now: rt.Now()}
				in.PriorID = mastermindPriorIDFunc(d)
				if r, _, err := mastermind.Init(reg, in); err == nil {
					rec = &r
				} else {
					fmt.Fprintf(os.Stderr, "relevo mastermind guide: register %s: %v\n", session, err)
				}
			}
		}
	}
	return state, rec
}

// printGuideResult prints the guide answer: the text on the human surface,
// the state document under --json.
func printGuideResult(state db.Consent, rec *mastermind.Record, repoLabel string, asJSON bool) error {
	text := mastermind.ConsentText(state, rec)
	if !asJSON {
		if text != "" {
			fmt.Println(text)
		}
		return nil
	}

	word := "disabled"
	switch state {
	case db.ConsentYes:
		word = "enabled"
	case db.ConsentUnset:
		word = "ask"
	}
	out := struct {
		State string `json:"state"`
		Text  string `json:"text"`
		Repo  string `json:"repo,omitempty"`
		ID    string `json:"id,omitempty"`
		Name  string `json:"name,omitempty"`
	}{State: word, Text: text, Repo: repoLabel}
	if rec != nil {
		out.ID, out.Name = rec.ID, rec.Name
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	fmt.Println(string(raw))
	return nil
}
