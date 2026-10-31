package relevo

import (
	"os"

	"github.com/fuad-daoud/relevo/internal/text"
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
	run := text.NewestCheckRun(names, round)
	if run == 0 {
		return nil
	}
	body, missing, err := read(rt.Store.CheckLogPath(name, round, run))
	if err != nil || missing {
		return err
	}
	res.Text = text.ChainCheckRunLabel(run) + "\n" + body
	res.Missing = false
	return nil
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
