package relevo

import (
	"errors"
	"fmt"
	"io/fs"
)

// RetryPlan returns the bytes of name's plan for round: the plan the cockpit's
// r key resends on another candidate (cockpit B2 §4.5). It reads through
// Store.ReadFile, so a plan the seal pass already moved into the database
// still reads exactly as it did on disk.
//
// A round with no plan recorded is not a retry: it is an error naming the
// round, so the caller can say so without stopping anything.
func RetryPlan(rt Runtime, name string, round int) ([]byte, error) {
	plan, err := rt.Store.ReadFile(rt.Store.PromptPath(name, round))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no plan recorded for %s round %d", name, round)
		}
		return nil, err
	}
	return plan, nil
}
