package relevo

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote/client"
)

// gateSpec is what a forwarded gate carries besides its subject: the reason,
// the expiry (zero for until cleared) and the clock the expiry is measured
// against, which is what a next: command needs to restate the same gate.
type gateSpec struct {
	reason string
	until  time.Time
	now    time.Time
}

// next is the one line a failed forward ends with: the command to run on
// server to gate subject there by hand.
func (g gateSpec) next(server, subject string) string {
	return NextGateLine(server, subject, g.reason, g.until, g.now)
}

// NextGateLine renders the next: line for a gate that did not reach server:
// `relevo gate --serve` with the same subject, remaining time and reason, run
// on that server's host. Pure.
func NextGateLine(server, subject, reason string, until, now time.Time) string {
	args := []string{"relevo", "gate", "--serve", shellWord(subject)}
	if !until.IsZero() {
		d := until.Sub(now).Round(time.Second)
		if d < time.Second {
			d = time.Second
		}
		args = append(args, "--for", d.String())
	}
	if reason != "" {
		args = append(args, "--reason", shellWord(reason))
	}
	return fmt.Sprintf("next: %s  # on %s", strings.Join(args, " "), server)
}

// shellWord quotes s for a POSIX shell unless it is plainly safe.
func shellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/@._:-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// unknownServer reports whether err says this machine does not configure the
// server at all: a binding naming it is an old one, and there is nothing to
// forward or to report.
func unknownServer(err error) bool {
	return errors.Is(err, client.ErrUnknownServer)
}
