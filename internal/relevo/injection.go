package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/classify"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/store"
)

// injectionScan is what both scan sites consume.
type injectionScan struct {
	Flagged   int
	FlaggedBy string                // "", "regex", "jev", "both"
	Record    *store.ClassifyRecord // nil when no classifier configured
	Note      string                // "" or the Record.Note, for joinNotes on the entry
}

// scanForInjection runs the regex floor and, when both rt.Policy.Classify
// and rt.Classify are non-nil, the classifier, and composes the result.
// It never returns an error: every classifier failure is a Note. The
// classifier call is bounded by context.WithTimeout(ctx, cfg.Timeout()).
// source is "report" or "dialog"; the harness comes from b.Builder.Kind.
func scanForInjection(ctx context.Context, rt Runtime, source string, b store.Binding, text []byte) injectionScan {
	regexLines := housekeep.ScanLines(text, housekeep.CompileScanPatterns(rt.Policy))
	regexCount := len(regexLines)
	cfg := rt.Policy.Classify
	if cfg == nil || rt.Classify == nil {
		by := ""
		if regexCount > 0 {
			by = "regex"
		}
		return injectionScan{
			Flagged:   regexCount,
			FlaggedBy: by,
			Record:    nil,
			Note:      "",
		}
	}

	provider := ""
	if cfg != nil {
		provider = cfg.Provider
	}
	rec := &store.ClassifyRecord{
		Provider:  provider,
		Model:     cfg.ModelName(),
		Threshold: cfg.Threshold(),
	}
	paras := classify.Split(text)
	kept, partial := classify.Trim(paras)
	rec.Partial = partial
	rec.Paragraphs = len(kept)
	if len(kept) == 0 {
		by := ""
		if regexCount > 0 {
			by = "regex"
		}
		return injectionScan{
			Flagged:   regexCount,
			FlaggedBy: by,
			Record:    rec,
			Note:      "",
		}
	}

	cctx, cancel := context.WithTimeout(ctx, cfg.Timeout())
	defer cancel()

	ans, err := rt.Classify.Judge(cctx, classify.Request{
		Source:     source,
		Harness:    b.Builder.Kind,
		Paragraphs: kept,
	})
	if err != nil {
		rec.Note = "classify: " + shortReason(err, cfg.Timeout())
		rec.Paragraphs = 0
		rec.Partial = false
		slog.Warn("classify failed", "binding", b.Name, "round", b.Round, "source", source, "err", err)
		by := ""
		if regexCount > 0 {
			by = "regex"
		}
		return injectionScan{
			Flagged:   regexCount,
			FlaggedBy: by,
			Record:    rec,
			Note:      rec.Note,
		}
	}

	flagged, by, above, max := composeFlagged(regexLines, kept, ans.Probabilities, cfg.Threshold())
	rec.Model = ans.Model
	rec.Above = above
	rec.Max = max
	rec.InputTokens = ans.InputTokens
	return injectionScan{
		Flagged:   flagged,
		FlaggedBy: by,
		Record:    rec,
		Note:      "",
	}
}

func shortReason(err error, timeout time.Duration) string {
	if errors.Is(err, classify.ErrUnavailable) {
		reason := strings.TrimPrefix(err.Error(), "classify: unavailable: ")
		return "unavailable: " + reason
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("timeout after %v", timeout)
	}
	if errors.Is(err, classify.ErrUnauthorized) {
		return "unauthorized (401)"
	}
	var se *classify.StatusError
	if errors.As(err, &se) {
		return fmt.Sprintf("http %d", se.Code)
	}
	return strings.TrimPrefix(err.Error(), "classify: ")
}

// composeFlagged is the pure union. A paragraph counts toward `above` when
// probs[i] >= threshold. It adds to `flagged` only when no regex-hit line
// falls within [kept[i].Line, kept[i].Line+kept[i].Lines-1], so a paragraph
// both judges caught is counted once. max is the largest probs[i] (0 when
// probs is empty). by is "" when flagged == 0, "regex" when only regex
// contributed, "jev" when only the classifier did, "both" otherwise --
// "both" includes the case where every jev paragraph overlapped a regex line.
func composeFlagged(regexLines []int, kept []classify.Paragraph, probs []float64, threshold float64) (flagged int, by string, above int, max float64) {
	for _, p := range probs {
		if p > max {
			max = p
		}
	}

	extraJev := 0
	for i, p := range kept {
		prob := 0.0
		if i < len(probs) {
			prob = probs[i]
		}
		if prob >= threshold {
			above++
			overlap := false
			pStart := p.Line
			pEnd := p.Line + p.Lines - 1
			for _, rLine := range regexLines {
				if rLine >= pStart && rLine <= pEnd {
					overlap = true
					break
				}
			}
			if !overlap {
				extraJev++
			}
		}
	}

	regexCount := len(regexLines)
	flagged = regexCount + extraJev
	if flagged == 0 {
		by = ""
	} else if above > 0 && regexCount > 0 {
		by = "both"
	} else if above > 0 {
		by = "jev"
	} else {
		by = "regex"
	}
	return flagged, by, above, max
}

// flaggedParenthetical is the caveat the two scan sites append to the text
// they record for the round. With rec == nil or rec.Note != "" it is:
//
//	" (N instruction-shaped line[s] flagged; see relevo show --log)"
//
// With a successful rec it is
//
//	" (N instruction-shaped line[s] flagged; jev p=0.94; see relevo show --log)"
//
// where p is rec.Max formatted %.2f. Empty when flagged <= 0, always.
func flaggedParenthetical(flagged int, rec *store.ClassifyRecord) string {
	if flagged <= 0 {
		return ""
	}
	unit := "lines"
	if flagged == 1 {
		unit = "line"
	}
	if rec == nil || rec.Note != "" {
		return fmt.Sprintf(" (%d instruction-shaped %s flagged; see relevo show --log)", flagged, unit)
	}
	return fmt.Sprintf(" (%d instruction-shaped %s flagged; jev p=%.2f; see relevo show --log)", flagged, unit, rec.Max)
}
