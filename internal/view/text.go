package view

import "fmt"

// HumanBytes renders n as a binary (1024-based) human-readable size, e.g.
// "512 B", "1.5 KiB", "3.0 MiB". It is the one implementation the CLI and the
// dry run share, so `relevo db stats` and `relevo send --dry-run` never disagree.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
