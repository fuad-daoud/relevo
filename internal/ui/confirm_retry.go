package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
	"github.com/fuad-daoud/relevo/internal/view"
)

// bindCmd is the b key: name, candidate and feature, in that order, then the
// bind confirm. It takes no binding: the key creates one.
func bindCmd(env widget.Env) tea.Cmd {
	name := promptBox{kind: "bind", title: "name:", input: newPromptInput()}
	name.validate = func(value string) error {
		v := strings.TrimSpace(value)
		if v == "" {
			return errors.New("name is required")
		}
		return store.ValidName(v)
	}
	name.onEnter = func(value string) tea.Cmd {
		binding := strings.TrimSpace(value)
		cand := promptBox{
			kind:    "bind",
			title:   "candidate (tab: …; empty = policy pick):",
			input:   newPromptInput(),
			choices: env.Actions.Candidates("builder"),
		}
		cand.onEnter = func(c string) tea.Cmd {
			feature := promptBox{kind: "bind", title: "feature (optional):", input: newPromptInput()}
			feature.onEnter = func(f string) tea.Cmd {
				in := widget.BindInput{Name: binding, Candidate: strings.TrimSpace(c), Feature: strings.TrimSpace(f)}
				return openOverlay(confirmBox{
					kind:   "bind",
					yes:    "bind",
					danger: false,
					title:  bindConfirmTitle(in),
					onYes: runAction(env.Ctx, "bind", binding, func(ctx context.Context) widget.Result {
						return env.Actions.Bind(ctx, in)
					}),
				})
			}
			return openOverlay(feature)
		}
		return openOverlay(cand)
	}
	return openOverlay(name)
}

// bindConfirmTitle is the bind confirm's question: the name, the repo the
// worktree is cut from, and the candidate or the policy pick.
func bindConfirmTitle(in widget.BindInput) string {
	on := in.Candidate
	if on == "" {
		on = "the policy pick"
	}
	return fmt.Sprintf("Bind %s on a new worktree of %s as builder on %s?", in.Name, getwd(), on)
}

// retryCmd is the r key: a candidate list, then the confirm. The list
// is the role's candidates in their order, including the current one; the
// current candidate and any gated one are disabled.
func retryCmd(env widget.Env, b view.BindingStatus) tea.Cmd {
	key := b.Key()
	current := candidateText(b)

	var items []listItem
	for _, name := range env.Actions.Candidates(actorCell(b)) {
		switch {
		case name == current:
			note := "last used"
			if roundOpen(b) {
				note = fmt.Sprintf("running round %d now", b.Round)
			}
			items = append(items, listItem{
				name: name, status: "current", note: note,
				statusStyle: widget.MutedStyle, disabled: true,
			})
		case gatedBy(env, name) != nil:
			g := gatedBy(env, name)
			status := "gated"
			if !g.Until.IsZero() {
				status = "gated " + ago(env.Now, g.Until)
			}
			items = append(items, listItem{
				name: name, status: status, note: gateProvider(g.Token),
				statusStyle: widget.FaintStyle, disabled: true,
			})
		default:
			items = append(items, listItem{name: name, status: "ready", statusStyle: widget.GreenStyle})
		}
	}

	sel := -1
	for i, item := range items {
		if !item.disabled {
			sel = i
			break
		}
	}

	header := widget.AccentStyle.Bold(true).Render("Retry "+key) +
		widget.TextStyle.Render(fmt.Sprintf(" round %d on another candidate", b.Round))

	var note []string
	if roundOpen(b) {
		note = append(note, fmt.Sprintf("Stops round %d, then sends its plan again on the chosen candidate.", b.Round))
	} else {
		note = append(note, "Sends the last plan again as a new round on the chosen candidate.")
	}
	note = append(note, "The binding stays on it for later rounds.")

	return openOverlay(listBox{
		kind:   "retry on…",
		submit: "retry",
		header: []string{header},
		note:   note,
		items:  items,
		sel:    sel,
		onPick: func(name string) tea.Cmd {
			return openOverlay(confirmBox{
				kind:   "retry",
				yes:    "stop and retry",
				danger: true,
				title:  retryConfirmTitle(b, name),
				lines:  retryConfirmLines(b, name),
				onYes: runAction(env.Ctx, "retry", key, func(ctx context.Context) widget.Result {
					return env.Actions.Retry(ctx, key, name)
				}),
			})
		},
	})
}

// gatedBy is the live gate on candidate name, matched by the gate's short
// name, or nil.
func gatedBy(env widget.Env, name string) *availability.Gate {
	for i := range env.Report.Gated {
		if env.Report.Gated[i].Name == name {
			return &env.Report.Gated[i]
		}
	}
	return nil
}

// retryConfirmTitle is the retry confirm's question: an open round is
// stopped first and the question names it; with nothing open the last plan is
// simply resent as a new round.
func retryConfirmTitle(b view.BindingStatus, candidate string) string {
	if roundOpen(b) {
		return fmt.Sprintf("Stop %s round %d and resend its plan on %s?", b.Key(), b.Round, candidate)
	}
	return fmt.Sprintf("Resend %s's last plan as a new round on %s?", b.Key(), candidate)
}

// retryConfirmLines names who else is affected, that the candidate persists as
// the binding's builder, then the keys.
func retryConfirmLines(b view.BindingStatus, candidate string) []string {
	var lines []string
	if l := mastermindConfirmLine(b, "is waiting on this round"); l != "" {
		lines = append(lines, l)
	}
	lines = append(lines, "the binding keeps "+candidate+" for later rounds")
	return lines
}

// roundOpen reports whether the row has a round in flight, the fact that
// decides which retry confirm is shown. ACTIVE is the one display word
// that means a round is open.
func roundOpen(b view.BindingStatus) bool { return b.Display == "ACTIVE" }
