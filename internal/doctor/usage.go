package doctor

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/usage"
)

// pricesMaxAge is how old prices.json's as_of may be before doctor warns.
const pricesMaxAge = 90 * 24 * time.Hour

func usageChecks(env Env, cfg runConfig) []Check {
	var out []Check
	if cfg.usageOpencode {
		if _, err := env.LookPath("sqlite3"); err != nil {
			out = append(out, Check{
				Name: "sqlite3", Severity: SevWarn,
				Detail: "not on PATH; relevo cannot confirm a push to an opencode mastermind, so its reports wait for the background wait",
				Fix:    "install sqlite3 (the CLI), e.g. pacman -S sqlite / apt install sqlite3",
			})
		} else {
			out = append(out, Check{Name: "sqlite3", Severity: SevOK, Detail: "on PATH; pushes to an opencode mastermind can be confirmed"})
		}
	}
	if cfg.usagePrices == nil {
		d := usage.DefaultPrices()
		out = append(out, Check{Name: "prices", Severity: SevOK,
			Detail: fmt.Sprintf("no prices configured; using the embedded default (as_of %s, %d models)", d.AsOf, len(d.Models))})
		return out
	}
	var p usage.Prices
	if err := json.Unmarshal(cfg.usagePrices, &p); err != nil {
		out = append(out, Check{Name: "prices", Severity: SevWarn,
			Detail: fmt.Sprintf("the stored prices body does not validate: %v; rounds estimate from the embedded default", err),
			Fix:    "fix the prices section"})
		return out
	}
	asOf, err := time.Parse("2006-01-02", p.AsOf)
	switch {
	case err != nil:
		out = append(out, Check{Name: "prices", Severity: SevWarn,
			Detail: fmt.Sprintf("prices: as_of %q is not YYYY-MM-DD", p.AsOf), Fix: "set as_of to the date the prices were checked"})
	case time.Since(asOf) > pricesMaxAge:
		out = append(out, Check{Name: "prices", Severity: SevWarn,
			Detail: fmt.Sprintf("prices: as_of %s is older than %d days; estimates may be stale", p.AsOf, int(pricesMaxAge.Hours()/24)),
			Fix:    "check the providers' pricing pages and update as_of"})
	default:
		out = append(out, Check{Name: "prices", Severity: SevOK, Detail: fmt.Sprintf("prices: as_of %s, %d models", p.AsOf, len(p.Models))})
	}
	return out
}
