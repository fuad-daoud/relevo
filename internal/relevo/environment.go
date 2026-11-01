package relevo

import (
	"fmt"
	"log/slog"
)

// EnvironmentError is a preflight refusal: the machine cannot start another
// round right now. Callers find it with errors.As, so a round can be stopped
// on the error's identity without matching its text.
type EnvironmentError struct {
	Reason string
}

// Error prefixes the reason with "environment: ": a preflight refusal reads
// as an environment problem, not a code one.
func (e *EnvironmentError) Error() string {
	return "environment: " + e.Reason
}

// gib renders n bytes as GB to one decimal place: more precision than statfs
// granularity earns and less than a full byte count breathes.
func gib(n uint64) string {
	return fmt.Sprintf("%.1f", float64(n)/(1<<30))
}

// CheckDiskFree is a round's disk preflight: the first path with less than
// minBytes free refuses the round with an *EnvironmentError naming it.
// minBytes 0 turns the preflight off. A path whose free space cannot be read
// is skipped and never refuses the round: a broken statfs is a probe problem,
// and failing every round on it would defeat the check.
func CheckDiskFree(paths []string, minBytes uint64, free func(string) (uint64, error)) error {
	if minBytes == 0 {
		return nil
	}
	for _, path := range paths {
		avail, err := free(path)
		if err != nil {
			slog.Warn("disk preflight: free space unreadable; skipping path", "path", path, "err", err)
			continue
		}
		if avail < minBytes {
			return &EnvironmentError{
				Reason: fmt.Sprintf("disk full: %s has %s GB free (< %d GB)", path, gib(avail), minBytes/(1<<30)),
			}
		}
	}
	return nil
}
