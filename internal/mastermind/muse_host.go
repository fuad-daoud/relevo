package mastermind

// museHostDepth caps how far up the process tree the muse ancestor walk
// climbs: a hook, a shell and a command nest only a few levels deep, and the
// bound also terminates the walk on a pid cycle.
const museHostDepth = 8

// MuseHostSeam builds a ResolveInput.MuseHost from process callbacks: the
// bounded walk starting at the caller's parent pid. Production passes /proc
// readers; tests pass fakes.
func MuseHostSeam(commOf func(pid int) (string, bool), ppidOf func(pid int) (int, bool)) func(ppid int) (int, bool) {
	return func(ppid int) (int, bool) {
		return findMuseHost(ppid, commOf, ppidOf)
	}
}

// findMuseHost is the nearest ancestor of ppid whose command name is "muse":
// the hook's and the MCP server's parent, or the shell's parent's parent when
// a command runs under `bash -c`. commOf reports a pid's command name and
// ppidOf its parent; a missing process ends the walk. Injected callbacks keep
// /proc out, so tests need no real process tree.
func findMuseHost(ppid int, commOf func(pid int) (string, bool), ppidOf func(pid int) (int, bool)) (int, bool) {
	pid := ppid
	for range museHostDepth {
		comm, ok := commOf(pid)
		if !ok {
			return 0, false
		}
		if comm == "muse" {
			return pid, true
		}
		parent, ok := ppidOf(pid)
		if !ok || parent <= 0 {
			return 0, false
		}
		pid = parent
	}
	return 0, false
}
