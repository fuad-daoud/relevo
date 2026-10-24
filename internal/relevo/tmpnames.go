package relevo

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strconv"
	"sync"
)

// tmpSocketSuffixLen is the longest socket file name held under a handed-out
// temp directory: every TMPDIR relevo hands out must leave room for it under
// Linux's 108-byte sun_path limit.
const tmpSocketSuffixLen = 40

// maxTmpDirLen is the longest temp directory relevo hands out: sun_path
// holds 108 bytes NUL included, so 107 usable bytes must fit the directory
// plus the socket suffix above.
const maxTmpDirLen = 107 - tmpSocketSuffixLen

// tmpShortHexLen is the width of every short temp name: 8 hex characters (32
// bits), so a served root reads t/<8> and a leaf rounds/<8> or checks/<8>.
const tmpShortHexLen = 8

// shortTmpID hashes input to tmpShortHexLen lower-case hex characters. The
// digest is uniform, so truncation stays deterministic in the full long
// identity while remaining filesystem-safe. A collision would share one
// directory between two live identities; at 32 bits that takes on the order
// of 77k names in one scope, far above the handful of live entries a root
// ever holds, and a second prepare of the same identity reuses its
// directory by design.
func shortTmpID(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])[:tmpShortHexLen]
}

// isShortTmpID reports whether name has the shape shortTmpID makes, so the
// sweep can tell a stale short entry (remove) from a pre-change long one
// (the old rules own it).
func isShortTmpID(name string) bool {
	if len(name) != tmpShortHexLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// shortRoundID is one round's leaf name: the binding name and round once
// spelled rounds/<name>-NNN, hashed. The owner stays in the root above it
// for served runtimes (one root per owner) and is always empty for local
// ones, so name plus round is the whole identity the leaf must cover.
func shortRoundID(name string, round int) string {
	return shortTmpID(name + "\x00" + strconv.Itoa(round))
}

// shortCheckID is one check's leaf name: the gate scope unit once spelled
// checks/<unit>, hashed. The unit already carries owner, binding and round,
// so it alone is the whole identity the leaf must cover.
func shortCheckID(unit string) string {
	return shortTmpID(unit)
}

// tmpOverBudget is the long-root selection rule: a temp directory this long
// leaves no room for the socket suffix under the sun_path limit. Callers log
// one warning and proceed with the long path; the round never fails for it.
func tmpOverBudget(dir string) bool {
	return len(dir) > maxTmpDirLen
}

// tmpBudgetWarned makes the over-budget warning once per process: the state
// dir decides the length, so every round on the host would repeat it.
var tmpBudgetWarned sync.Once

// warnTmpOverBudget warns, once, when a temp directory leaves less than
// tmpSocketSuffixLen bytes for a socket path under it. The round proceeds:
// most sockets need far less, and a long state dir is not the round's fault.
func warnTmpOverBudget(dir string) {
	if !tmpOverBudget(dir) {
		return
	}
	tmpBudgetWarned.Do(func() {
		slog.Warn("tmp dir leaves under 40 bytes for a socket path; tests that bind a socket under TMPDIR may fail", "dir", dir, "len", len(dir))
	})
}
