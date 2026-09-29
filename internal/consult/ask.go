package consult

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/store"
)

// Timeout is how long a consult may stay working before relevo gives up on it.
// A consult reads and writes one file; the alternative to a deadline is a
// record that never becomes terminal and holds a cap slot.
const Timeout = 10 * time.Minute

// Request is one consult to reserve, spawn and record.
type Request struct {
	Name     string
	ID       string
	Role     string
	Endpoint store.Endpoint
	Body     []byte

	// Round > 0 is a round consult: Argv is the resume argv the caller already
	// built and validated, and Inline says whether the question was staged
	// inline or as a file. Round 0 spawns a fresh headless process.
	Round  int
	Argv   []string
	Inline bool

	// Candidate, Spec and Tier are the resolved launch inputs of a headless
	// consult (Round == 0).
	Candidate candidate.Candidate
	Spec      harness.RoleSpec
	Tier      harness.Tier

	// Pick builds the candidate-pick log entry for a running headless consult;
	// nil when the consult writes none.
	Pick func(round int) *store.LogEntry
	// AskNote is the ask log entry's Note.
	AskNote string
}

// Running counts ConsultSpawning as well as ConsultRunning. A reservation
// occupies a slot the cap cares about, or two concurrent asks would both see
// it free.
func Running(b store.Binding) int {
	n := 0
	for _, c := range b.Consults {
		if c.State == store.ConsultSpawning || c.State == store.ConsultRunning {
			n++
		}
	}
	return n
}

// RandomID returns 8 hex characters. A failed CSPRNG read is not a reason to
// refuse a consult: the id only has to be unique within one binding, and the
// clock is sufficient for that.
func RandomID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%08x", uint32(time.Now().UnixNano()))
	}
	return hex.EncodeToString(b[:])
}

// newID returns the caller's id minter, or RandomID when it has none.
func newID(d Deps) string {
	if d.NewID != nil {
		return d.NewID()
	}
	return RandomID()
}

// strandError combines the failure that stranded a consult with any failure to
// record it. Returning only the save error hides the half that explains what
// actually went wrong.
func strandError(cause, saveErr error) error {
	if saveErr != nil {
		return fmt.Errorf("%w (also failed to record the consult: %s)", cause, saveErr.Error())
	}
	return cause
}

// brief reduces an error to one line, for a note field where a multi-line
// message would break the line's shape.
func brief(err error) string {
	if err == nil {
		return ""
	}
	s := strings.TrimSpace(err.Error())
	if idx := strings.Index(s, "\n"); idx != -1 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}
