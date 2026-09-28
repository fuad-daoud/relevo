package mastermind

import (
	"regexp"
	"strconv"
)

// Ident is which harness process is calling, as far as relevo can tell
// without shelling out or reading a file.
type Ident struct {
	Kind      string
	SessionID string
	// HostPID is the harness process: $CLAUDE_PID in a Bash tool, else the
	// caller's parent pid.
	HostPID int
}

const claudeEnvMarker = "CLAUDECODE"

// agyConversationEnv names the agy conversation a command runs in.
const agyConversationEnv = "ANTIGRAVITY_CONVERSATION_ID"

// conversationIDRe is a lower-case 8-4-4-4-12 hex UUID.
var conversationIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validConversationID(id string) bool { return conversationIDRe.MatchString(id) }

// Detect reports which harness process is calling: `claude` from CLAUDECODE,
// `agy` from a valid ANTIGRAVITY_CONVERSATION_ID, `opencode` from
// RELEVO_HARNESS=opencode. Claude is checked first, so CLAUDECODE=1 wins
// when both are set. Pure: it never shells out or reads a file.
//
// ppid (os.Getppid()) is HostPID's fallback when CLAUDE_PID is unset or
// unparseable.
func Detect(env func(string) string, ppid int) (Ident, bool) {
	if env == nil {
		return Ident{}, false
	}

	if env(claudeEnvMarker) == "1" {
		ident := Ident{
			Kind:      "claude",
			SessionID: env("CLAUDE_CODE_SESSION_ID"),
		}
		if pid, err := strconv.Atoi(env("CLAUDE_PID")); err == nil && pid > 0 {
			ident.HostPID = pid
		} else {
			ident.HostPID = ppid
		}
		return ident, true
	}

	// agy names no stable host process, so HostPID stays 0.
	if conv := env(agyConversationEnv); validConversationID(conv) {
		return Ident{Kind: "agy", SessionID: conv, HostPID: 0}, true
	}

	if env("RELEVO_HARNESS") == "opencode" {
		return Ident{Kind: "opencode"}, true
	}

	return Ident{}, false
}

// RunnerEnv marks a process relevo started for a round: its value is the
// binding name the round runs for. A harness session relevo spawns for a round
// carries exactly this one identity variable, so the consent hooks can tell a
// runner from the human's own session and stay silent for it.
const RunnerEnv = "RELEVO_RUNNER"

// IsRunner reports whether env belongs to a process relevo started for a round:
// a non-empty RunnerEnv. A nil env is not a runner, so a caller with no
// environment reads the safe answer -- a session that is left alone.
func IsRunner(env func(string) string) bool {
	if env == nil {
		return false
	}
	return env(RunnerEnv) != ""
}
