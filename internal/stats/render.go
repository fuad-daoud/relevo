package stats

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FitKey fits s into width runes, clipping with a leading "…" when clipLeft so
// a repo key's tail stays visible.
func FitKey(s string, width int, clipLeft bool) string {
	r := []rune(s)
	if len(r) <= width {
		return s + strings.Repeat(" ", width-len(r))
	}
	if clipLeft {
		return "…" + string(r[len(r)-(width-1):])
	}
	return string(r[:width-1]) + "…"
}

// PctText is a percentage of the closed rounds, or "-" when none is closed.
func PctText(pct float64, closed int) string {
	if closed == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", pct)
}

// TTFTText is the time to first token, or "-" when there is none.
func TTFTText(s ScoreRow) string {
	if !s.HasTTFT {
		return "-"
	}
	return fmt.Sprintf("%.1fs", float64(s.TTFTMS)/1000)
}

// ShortTokens is the report's token total: plain under 1,000, else k/M/B with a
// trailing ".0" stripped. It stays local because usage.ShortTokens stops at M.
func ShortTokens(n int64) string {
	if n < 1_000 {
		return strconv.FormatInt(n, 10)
	}
	var value float64
	var unit string
	switch {
	case n < 1_000_000:
		value, unit = float64(n)/1_000, "k"
	case n < 1_000_000_000:
		value, unit = float64(n)/1_000_000, "M"
	default:
		value, unit = float64(n)/1_000_000_000, "B"
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", value), ".0") + unit
}

func Duration(ms int64) string {
	hour := int64(time.Hour / time.Millisecond)
	minute := int64(time.Minute / time.Millisecond)
	if ms >= hour {
		return fmt.Sprintf("%dh%02dm", ms/hour, (ms%hour)/minute)
	}
	return fmt.Sprintf("%dm", ms/minute)
}

func MonthDay(day string) string {
	if len(day) >= len("2006-01-02") {
		return day[5:]
	}
	return day
}
