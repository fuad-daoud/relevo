package relevo

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// showChainCheckGate fills res with the round's chain check log when the
// round has no gate log of its own: a chain runs its checks against its
// writer's newest closed round, so the check run's sealed log is the gate
// output that round holds. The log shown is the one with the highest run
// number, under a label naming the run. Runs are numbered across the whole
// chain, so a round may hold runs 3, 4 and 5 and none below 3 -- the round's
// files are listed rather than probed run by run. list yields the binding's
// round-file names, a live store's disk files and sealed rows or an archive
// record's sealed names, and read reads one back by the key CheckLogPath
// builds. With no check log for the round, res is left as it is.
func showChainCheckGate(rt Runtime, name string, round int, read func(path string) (text string, missing bool, err error), list func() ([]string, error), res *ShowResult) error {
	names, err := list()
	if err != nil {
		return err
	}
	run := newestCheckRun(names, round)
	if run == 0 {
		return nil
	}
	text, missing, err := read(rt.Store.CheckLogPath(name, round, run))
	if err != nil || missing {
		return err
	}
	res.Text = chainCheckRunLabel(run) + "\n" + text
	res.Missing = false
	return nil
}

// chainCheckRunLabel is the line put above a check log the gate section
// shows, so a reader can tell it from the round's own gate log.
func chainCheckRunLabel(run int) string {
	return fmt.Sprintf("chain check run %d", run)
}

// newestCheckRun is the highest run number among names that are round's
// chain check logs, or 0 when the round holds none. The highest run is the
// attempt the round was left at.
func newestCheckRun(names []string, round int) int {
	newest := 0
	for _, n := range names {
		if run, ok := checkLogRun(n, round); ok && run > newest {
			newest = run
		}
	}
	return newest
}

// checkLogRun parses a round-file name shaped NNN-check-RRR.log into its run
// number; ok is false for every other name. The store writes that shape for
// nothing else: a served check log is NNN-served-check.log, and a consult's
// files do not end .log.
func checkLogRun(n string, round int) (int, bool) {
	base, ok := strings.CutSuffix(n, ".log")
	if !ok {
		return 0, false
	}
	parts := strings.Split(base, "-")
	if len(parts) != 3 || parts[1] != "check" {
		return 0, false
	}
	r, err := strconv.Atoi(parts[0])
	if err != nil || r != round {
		return 0, false
	}
	run, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0, false
	}
	return run, true
}

// readFileOrMissing reads path through read, reporting Missing rather than an
// error when it does not exist. read is rt.Store.ReadFile, so a sealed
// round's file is found in the database exactly as a present one is.
func readFileOrMissing(read func(string) ([]byte, error), path string) (text string, missing bool, err error) {
	data, err := read(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", true, nil
		}
		return "", false, err
	}
	return string(data), false, nil
}
