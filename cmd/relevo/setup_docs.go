package main

// cmd/relevo/setup_docs.go holds the setup documents (the questions contract
// and the result contract) and the numbered TTY prompts. Both are pure over
// the engine's facts, so a test pins their shape without a harness.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/setup"
)

// SetupQuestionsDoc is the machine shape of "questions are still open": the
// probed inventory, every standing note, the questions left, and the command
// that hands the answers back.
type SetupQuestionsDoc struct {
	Inventory []SetupInventoryEntry `json:"inventory"`
	Notes     []string              `json:"notes,omitempty"`
	Questions []SetupQuestionDoc    `json:"questions"`
	Next      string                `json:"next"`
}

// SetupInventoryEntry is one probed harness row: on_path is always true (a
// binary not on PATH gets no row), and login names the way in only where it
// is missing.
type SetupInventoryEntry struct {
	Harness  string   `json:"harness"`
	OnPath   bool     `json:"on_path"`
	LoggedIn bool     `json:"logged_in"`
	Models   []string `json:"models,omitempty"`
	Login    string   `json:"login,omitempty"`
}

// SetupQuestionDoc is one open question: its prompt, whether several values
// may be picked in order, the recommended ones, and the choices to name.
type SetupQuestionDoc struct {
	ID          string           `json:"id"`
	Prompt      string           `json:"prompt"`
	Multi       bool             `json:"multi"`
	Recommended []string         `json:"recommended"`
	Choices     []SetupChoiceDoc `json:"choices"`
}

// SetupChoiceDoc is one choice: the value an --answer flag takes, a short
// label, and the one-line why.
type SetupChoiceDoc struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Why   string `json:"why"`
}

// SetupResultDoc is the machine shape of an applied run: one row per step,
// the failing ones naming their own fix.
type SetupResultDoc struct {
	Steps []SetupStepDoc `json:"steps"`
}

// SetupStepDoc is one applied step: success or failure, the failure's reason
// and the command that fixes it.
type SetupStepDoc struct {
	Step   string `json:"step"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Next   string `json:"next,omitempty"`
}

func setupQuestionsDocOf(inv setup.Inventory, questions []setup.Question) SetupQuestionsDoc {
	entries := make([]SetupInventoryEntry, 0, len(inv.Harnesses))
	for _, s := range inv.Harnesses {
		login := s.Login
		if s.LoggedIn {
			// Logged in needs no way in: an empty login omits the field's key.
			login = ""
		}
		entries = append(entries, SetupInventoryEntry{
			Harness:  s.Harness,
			OnPath:   s.OnPath,
			LoggedIn: s.LoggedIn,
			Models:   s.Models,
			Login:    login,
		})
	}
	return SetupQuestionsDoc{
		Inventory: entries,
		Notes:     inv.Notes,
		Questions: setupQuestionDocs(questions),
		Next:      setupQuestionsNext(questions),
	}
}

func setupQuestionDocs(qs []setup.Question) []SetupQuestionDoc {
	docs := make([]SetupQuestionDoc, 0, len(qs))
	for _, q := range qs {
		choices := make([]SetupChoiceDoc, 0, len(q.Choices))
		for _, c := range q.Choices {
			choices = append(choices, SetupChoiceDoc{Value: c.Value, Label: c.Label, Why: c.Why})
		}
		docs = append(docs, SetupQuestionDoc{
			ID:          q.ID,
			Prompt:      q.Prompt,
			Multi:       q.Multi,
			Recommended: q.Recommended,
			Choices:     choices,
		})
	}
	return docs
}

func setupQuestionsNext(qs []setup.Question) string {
	parts := make([]string, 0, len(qs))
	for _, q := range qs {
		parts = append(parts, "--answer "+q.ID+"="+setupQuestionPlaceholder(q))
	}
	return strings.Join(append([]string{"relevo setup"}, parts...), " ")
}

// setupQuestionPlaceholder is the value placeholder a yes/no question's
// answer spells with both picks, the ordered questions' with the general one.
func setupQuestionPlaceholder(q setup.Question) string {
	if len(q.Choices) == 2 && q.Choices[0].Value == "yes" && q.Choices[1].Value == "no" {
		return "<yes|no>"
	}
	return "<...>"
}

func setupResultDocOf(steps []SetupStepDoc) SetupResultDoc {
	return SetupResultDoc{Steps: steps}
}

// setupPrintQuestions prints the questions document and returns the exit
// report already handles silently: the document itself sits on stdout.
func setupPrintQuestions(doc SetupQuestionsDoc) error {
	if err := printDoc(doc); err != nil {
		return err
	}
	return exitCodeErr{code: setupQuestionsCode}
}

// setupStepDocOf turns a coded failure into its result row: the code's own
// message and the next it carries.
func setupStepDocOf(step string, err error) SetupStepDoc {
	doc := SetupStepDoc{Step: step, OK: false, Detail: firstLine(err.Error())}
	var ce *cliError
	if errors.As(err, &ce) {
		doc.Detail = ce.message
		doc.Next = ce.next
	}
	return doc
}

// askSetupQuestions walks the open questions in order, reading one answer
// line per question.
func askSetupQuestions(w io.Writer, r io.Reader, qs []setup.Question) (setup.Answers, error) {
	sc := bufio.NewScanner(r)
	out := make(setup.Answers, len(qs))
	for _, q := range qs {
		vals, err := askOneSetupQuestion(w, sc, q)
		if err != nil {
			return nil, err
		}
		out[q.ID] = vals
	}
	return out, nil
}

// askOneSetupQuestion renders one prompt and reads an answer, re-asking up
// to three times when a line is unusable; an empty line takes the
// recommendation.
func askOneSetupQuestion(w io.Writer, sc *bufio.Scanner, q setup.Question) ([]string, error) {
	for tries := 0; tries < 3; tries++ {
		writeSetupPrompt(w, q)
		if !sc.Scan() {
			return nil, io.ErrUnexpectedEOF
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			return append([]string{}, q.Recommended...), nil
		}
		vals, err := promptAnswerLine(q, line)
		if err == nil {
			return vals, nil
		}
		fmt.Fprintln(w, err)
	}
	return nil, fmt.Errorf("%s: no usable answer after three prompts", q.ID)
}

// writeSetupPrompt renders one question: the prompt, every choice with the
// recommended ones marked, then the answer line.
func writeSetupPrompt(w io.Writer, q setup.Question) {
	fmt.Fprintf(w, "%s\n", q.Prompt)
	for i, c := range q.Choices {
		mark := ""
		if slices.Contains(q.Recommended, c.Value) {
			mark = " (recommended)"
		}
		fmt.Fprintf(w, "  %d. %s%s -- %s\n", i+1, setupChoiceText(c), mark, c.Why)
	}
	fmt.Fprint(w, "> ")
}

// setupChoiceText carries a choice's label beside its value when the label
// differs; the wizard's yes/nos carry none.
func setupChoiceText(c setup.Choice) string {
	if c.Label != "" && c.Label != c.Value {
		return c.Label + " (" + c.Value + ")"
	}
	return c.Value
}

// promptAnswerLine turns one typed line into an answer: an empty line takes
// the recommendation, a comma list of numbers picks choices in the order
// typed, and anything else is parsed as the flag rule would.
func promptAnswerLine(q setup.Question, line string) ([]string, error) {
	tokens := strings.Split(line, ",")
	if order, ok := numericTokens(tokens); ok {
		vals := make([]string, 0, len(tokens))
		for _, n := range order {
			n--
			if n < 0 || n >= len(q.Choices) {
				return nil, fmt.Errorf("%s: pick between 1 and %d", q.ID, len(q.Choices))
			}
			vals = append(vals, q.Choices[n].Value)
		}
		return vals, nil
	}
	return setup.ParseAnswer(q, line)
}

// numericTokens reports a comma list of natural numbers, keeping the order
// typed; anything else means the line is an answer as an --answer flag
// carries it.
func numericTokens(tokens []string) ([]int, bool) {
	order := make([]int, 0, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" {
			return nil, false
		}
		n, err := strconv.Atoi(t)
		if err != nil || n < 1 {
			return nil, false
		}
		order = append(order, n)
	}
	return order, true
}
