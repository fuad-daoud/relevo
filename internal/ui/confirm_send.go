package ui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
	"github.com/fuad-daoud/relevo/internal/view"
)

// sendCmd is the s key: the plan picker, then the send confirm. The picker
// keeps the prompt's pre-fill (plansDir(b)), tab completion and file
// validation; it adds a list of the directory's recent plans. root is the
// binding's tree, which relative values resolve against.
func sendCmd(env widget.Env, b view.BindingStatus) tea.Cmd {
	p := sendPicker{env: env, b: b, input: newPromptInput(), pick: -1, root: b.CWD}
	if d := plansDir(b); d != "" {
		p.input.SetValue(d)
		p.input.CursorEnd()
	}
	return openOverlay(p)
}

// plansDir is the send picker's pre-fill: the binding tree's docs/plans/
// directory, made relative to that tree ("docs/plans/"), when it exists, ""
// when it does not.
func plansDir(b view.BindingStatus) string {
	if b.CWD == "" {
		return ""
	}
	dir := filepath.Join(b.CWD, "docs", "plans")
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}
	return filepath.Join("docs", "plans") + string(filepath.Separator)
}

// pathCompletion is the plan prompt's tab: the entries of the directory the
// value names, filtered by its last segment, so tab lists a directory and
// cycles what is in it. A value ending in the separator lists that
// directory's entries whole.
func pathCompletion(value string) []string {
	if value == "" {
		return nil
	}
	dir, prefix := value, ""
	if !strings.HasSuffix(value, string(filepath.Separator)) {
		dir, prefix = filepath.Dir(value), filepath.Base(value)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || (prefix != "" && !strings.HasPrefix(name, prefix)) {
			continue
		}
		full := filepath.Join(dir, name)
		if e.IsDir() {
			full += string(filepath.Separator)
		}
		out = append(out, full)
	}
	sort.Strings(out)
	return out
}

// sendConfirmTitle is the send confirm's question: the file, the binding, the
// round the send opens, and nothing else. The round is BindingStatus.Round:
// the daemon advances it when a round closes, so the title names b.Round,
// not b.Round+1.
func sendConfirmTitle(b view.BindingStatus, file string) string {
	return fmt.Sprintf("Send %s to %s as round %d?", file, b.Key(), b.Round)
}

func sendConfirmLines(b view.BindingStatus) []string {
	var lines []string
	if l := mastermindConfirmLine(b, "is waiting on this round"); l != "" {
		lines = append(lines, l)
	}
	return lines
}

// editorCmd is the E key: draft a plan in the state directory's tui-plans/,
// open $EDITOR on it with the terminal released, and, when the human left
// something new and non-empty in it, ask the same send confirm s asks.
func editorCmd(env widget.Env, b view.BindingStatus) tea.Cmd {
	key := b.Key()
	dir, ok := tuiPlansDir(env)
	if !ok {
		return notice("no state directory to draft a plan in; write one and use s")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return notice(err.Error())
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.md", key, env.Now.Unix()))
	header := []byte(editorPlanHeader(key))
	if err := os.WriteFile(path, header, 0o644); err != nil {
		return notice(err.Error())
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	return tea.ExecProcess(exec.Command(editor, path), func(err error) tea.Msg {
		if err != nil {
			return widget.NoticeMsg{Text: err.Error()}
		}
		after, rerr := os.ReadFile(path)
		if rerr != nil {
			return widget.NoticeMsg{Text: rerr.Error()}
		}
		if !planSent(header, after) {
			return widget.NoticeMsg{Text: "nothing sent"}
		}
		return openOverlayMsg{ov: confirmBox{
			kind:   "send",
			yes:    "send the plan",
			danger: false,
			title:  sendConfirmTitle(b, path),
			lines:  sendConfirmLines(b),
			onYes: runAction(env.Ctx, "send", key, func(ctx context.Context) widget.Result {
				return env.Actions.Send(ctx, key, path)
			}),
		}}
	})
}

// tuiPlansDir is <state dir>/tui-plans, where the E key drafts a plan. The
// state dir is the store's root: the database sits inside it.
func tuiPlansDir(env widget.Env) (string, bool) {
	st := env.Src.Base().Store
	if st == nil {
		return "", false
	}
	return filepath.Join(filepath.Dir(st.DBPath()), "tui-plans"), true
}

// editorPlanHeader is the one-line comment the draft file starts with, so a
// human sees what the file is for before writing in it.
func editorPlanHeader(name string) string {
	return fmt.Sprintf("<!-- relevo: plan for %s -->\n", name)
}

// planSent reports whether an edited draft is worth sending: something
// non-empty was written and the file differs from the header alone.
func planSent(before, after []byte) bool {
	return len(bytes.TrimSpace(after)) > 0 && !bytes.Equal(before, after)
}
