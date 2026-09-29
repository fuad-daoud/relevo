package usage

import "context"

// Exec runs a binary and returns its stdout. The opencode deliverer's delivery
// confirmation is its remaining user: nil means the binary is not available.
type Exec interface {
	Run(ctx context.Context, bin string, args ...string) ([]byte, error)
}
