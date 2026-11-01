package main

import (
	"context"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
)

// freshenBudget bounds the hint's dial: the hint is a courtesy, so an
// unanswering daemon costs the read this much and no more.
const freshenBudget = 500 * time.Millisecond

// freshenHint is a variable so a test counts sends without a daemon.
var freshenHint = sendFreshenToDaemon

// shouldSendFreshen is the rule for which reads hint: the statusline never
// does (it runs in every prompt and must stay a pure read) and a failed read
// has nothing to freshen.
func shouldSendFreshen(line bool, err error) bool { return !line && err == nil }

func hintFreshen(line bool, err error) {
	if shouldSendFreshen(line, err) {
		freshenHint()
	}
}

// sendFreshenToDaemon dials the socket itself, not through the helpers that
// start a daemon: a read must never bring one up.
func sendFreshenToDaemon() {
	root, err := tmpdir.DefaultRoot()
	if err != nil {
		return
	}
	sock, err := ownerSocket(root)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), freshenBudget)
	defer cancel()
	_ = client.SyncFreshen(ctx, sock)
}
