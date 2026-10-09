package main

import (
	"context"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// freshenBudget bounds the hint's dial. The hint is a courtesy to the read that
// sent it, so a daemon that does not answer in this long costs the read this
// much and no more.
const freshenBudget = 500 * time.Millisecond

// freshenHint sends the daemon the hint that a reader wants current data. It is
// a variable so a test counts sends without a daemon.
var freshenHint = sendFreshenToDaemon

// shouldSendFreshen is the rule for which reads hint: a full read does, the
// statusline never does -- it runs inside every prompt and must stay a pure
// read of local rows -- and a read that failed has nothing to freshen.
func shouldSendFreshen(line bool, err error) bool { return !line && err == nil }

// hintFreshen sends the hint when the rule says to.
func hintFreshen(line bool, err error) {
	if shouldSendFreshen(line, err) {
		freshenHint()
	}
}

// sendFreshenToDaemon dials the owner socket and sends the hint. It dials the
// socket itself rather than through the helpers that start a daemon: no daemon
// running means nobody to hint, and a read must never bring one up.
func sendFreshenToDaemon() {
	root, err := store.DefaultRoot()
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
