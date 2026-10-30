package resolve

import (
	"fmt"
	"time"
)

// blockedText renders a blocked duration compactly, flooring:
//
//	d < 1h   -> "<m>m"          45m -> "45m"; 0 -> "0m"
//	d < 24h  -> "<h>h<mm>m"     5h2m -> "5h02m"
//	else     -> "<d>d<hh>h"     52h -> "2d04h"; 72h -> "3d00h"
func blockedText(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", d/time.Minute)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", d/time.Hour, (d%time.Hour)/time.Minute)
	default:
		return fmt.Sprintf("%dd%02dh", d/(24*time.Hour), (d%(24*time.Hour))/time.Hour)
	}
}
