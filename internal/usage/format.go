package usage

import (
	"fmt"
	"math"
	"strings"
)

// Money is the cost word. Plan wins over basis: a subscription lane never shows
// dollars. Under a cent prints "<$0.01", not "$0.00".
func Money(c Cost) string {
	switch {
	case c.Plan:
		return "plan"
	case c.Basis == Unknown:
		return "unknown"
	}
	prefix := "$"
	if c.Basis == Estimated {
		prefix = "~$"
	}
	if c.USD > 0 && c.USD < 0.005 {
		return "<" + prefix + "0.01"
	}
	return fmt.Sprintf("%s%.2f", prefix, c.USD)
}

// ShortTokens: < 1000 as-is, < 1M "182k", else "2.3M".
func ShortTokens(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 999_500:
		return fmt.Sprintf("%dk", int64(math.Round(float64(n)/1000)))
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

// ShortDuration: "" for 0, "<1m" under a minute, "14m", "1h05m".
func ShortDuration(ms int64) string {
	switch {
	case ms <= 0:
		return ""
	case ms < 60_000:
		return "<1m"
	}
	minutes := ms / 60_000
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dh%02dm", minutes/60, minutes%60)
}

// msText renders "640ms" below a second, "3.1s" from there up.
func msText(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// tokenCells is the four token cells in order, or nil when there are no samples.
// `write` is printed even when 0, so a reader learns the field exists.
func tokenCells(t Tokens, samples int) []string {
	if samples == 0 {
		return nil
	}
	prompt := t.In + t.CacheRead + t.CacheWrite
	cache := "cache " + ShortTokens(t.CacheRead)
	if prompt > 0 {
		cache += fmt.Sprintf(" (%.0f%%)", t.CacheRatio()*100)
	}
	return []string{
		"in " + ShortTokens(t.In), cache,
		"write " + ShortTokens(t.CacheWrite), "out " + ShortTokens(t.Out),
	}
}

func Line(u Usage) string {
	var parts []string
	var id []string
	for _, s := range []string{u.Harness, u.Provider, u.Model} {
		if s != "" {
			id = append(id, s)
		}
	}
	if len(id) > 0 {
		parts = append(parts, strings.Join(id, "/"))
	}
	if d := ShortDuration(u.DurationMS); d != "" {
		parts = append(parts, d)
	}
	parts = append(parts, tokenCells(u.Tokens, u.Samples)...)
	if u.Steps > 0 {
		parts = append(parts, fmt.Sprintf("%d steps", u.Steps))
	}
	if u.Steps > 0 && u.ToolCalls > 0 {
		parts = append(parts, fmt.Sprintf("%.2f calls/step", float64(u.ToolCalls)/float64(u.Steps)))
	}
	if u.Steps == 0 && u.ToolCalls > 0 {
		parts = append(parts, fmt.Sprintf("%d tool calls", u.ToolCalls))
	}
	if u.StepP50MS > 0 {
		parts = append(parts, "step p50 "+msText(u.StepP50MS))
	}
	if u.FirstOutputP50MS > 0 {
		parts = append(parts, "first out p50 "+msText(u.FirstOutputP50MS))
	}
	money := Money(u.Cost)
	if u.Cost.Basis == Unknown && !u.Cost.Plan && u.Note != "" {
		money += " (" + u.Note + ")"
	}
	parts = append(parts, money)
	return strings.Join(parts, "  ")
}

// Parts is the round as separable parts for a surface that joins them itself and
// already names the harness. An unknown note ending in " for <provider>/<model>"
// loses that suffix.
func Parts(u Usage) []string {
	var parts []string
	switch {
	case u.Model != "":
		parts = append(parts, u.Model)
	case u.Harness != "":
		parts = append(parts, u.Harness)
	}
	if d := ShortDuration(u.DurationMS); d != "" {
		parts = append(parts, d)
	}
	parts = append(parts, tokenCells(u.Tokens, u.Samples)...)
	money := Money(u.Cost)
	if u.Cost.Basis == Unknown && !u.Cost.Plan && u.Note != "" {
		note := strings.TrimSuffix(u.Note, " for "+u.Provider+"/"+u.Model)
		money += ": " + note
	}
	return append(parts, money)
}

// LiveParts is Parts with the word "live" prepended: a figure for a running round.
func LiveParts(u Usage) []string {
	return append([]string{"live"}, Parts(u)...)
}

// LiveShort is the card/statusline form of a live figure; "" with no samples, so a
// surface shows nothing rather than "live unknown".
func LiveShort(u Usage) string {
	if u.Samples == 0 {
		return ""
	}
	return "live " + Money(u.Cost) + " · " + ShortTokens(u.Tokens.Total()) + " tok"
}
