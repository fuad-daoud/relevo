package doctor

import (
	"fmt"
	"strings"
)

// SwapCheck is the swap row: whether the host has swap or zram to spill a
// burst that exceeds memory into. It reads /proc/swaps through the caller's
// bytes so tests pin the parse without touching the host.
func SwapCheck(raw []byte, readErr error) Check {
	c := Check{Name: "swap"}
	if readErr != nil {
		c.Severity = SevWarn
		c.Detail = fmt.Sprintf("cannot read /proc/swaps (%v); a burst beyond memory may have nowhere to spill", readErr)
		c.ProbeFailed = true
		return c
	}
	var swapDev, zramDev string
	for _, line := range strings.Split(string(raw), "\n") {
		// Entries name a device path; the header ("Filename ...") and
		// blank or short lines do not, so they fall out here.
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "/") {
			continue
		}
		if strings.Contains(fields[0], "zram") {
			if zramDev == "" {
				zramDev = fields[0]
			}
			continue
		}
		if swapDev == "" {
			swapDev = fields[0]
		}
	}
	switch {
	case zramDev != "":
		c.Severity = SevOK
		c.Detail = fmt.Sprintf("zram swap active on %s; a burst that exceeds memory has somewhere to spill", zramDev)
	case swapDev != "":
		c.Severity = SevOK
		c.Detail = fmt.Sprintf("swap active on %s; a burst that exceeds memory has somewhere to spill", swapDev)
	default:
		c.Severity = SevWarn
		c.Detail = "no swap/zram: a burst that exceeds memory has nowhere to spill"
		c.Fix = "as root: fallocate -l 4G /swapfile && chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile"
	}
	return c
}
