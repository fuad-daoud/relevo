package setup

// docs.go holds the setup documents and the numbered TTY prompts, pure over
// the engine's facts, so a test pins their shape without a harness.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/text"
)

// SetupQuestionsDoc is the machine shape of "questions are still open".
type SetupQuestionsDoc struct {
	Inventory []SetupInventoryEntry `json:"inventory"`
	Notes     []string              `json:"notes,omitempty"`
	Questions []SetupQuestionDoc    `json:"questions"`
	Next      string                `json:"next"`
}

// SetupInventoryEntry is one probed harness row; on_path is always true, and
// login names the way in only where it is missing.
type SetupInventoryEntry struct {
	Harness  string   `json:"harness"`
	OnPath   bool     `json:"on_path"`
	LoggedIn bool     `json:"logged_in"`
	Models   []string `json:"models,omitempty"`
	Login    string   `json:"login,omitempty"`
	// Providers is opencode's model count per provider; its Models are the
	// recommended picks, the full list being hundreds long.
	Providers map[string]int `json:"providers,omitempty"`
}

// SetupQuestionDoc is one open question.
type SetupQuestionDoc struct {
	ID          string           `json:"id"`
	Prompt      string           `json:"prompt"`
	Multi       bool             `json:"multi"`
	Optional    bool             `json:"optional,omitempty"`
	Recommended []string         `json:"recommended"`
	Choices     []SetupChoiceDoc `json:"choices"`
}

// SetupChoiceDoc is one choice: the value an --answer flag takes and, for a
// model, the efforts an answer may append as value:effort.
type SetupChoiceDoc struct {
	Value   string   `json:"value"`
	Label   string   `json:"label"`
	Why     string   `json:"why"`
	Efforts []string `json:"efforts,omitempty"`
}

// SetupResultDoc is the machine shape of an applied run: one row per step.
type SetupResultDoc struct {
	Steps []SetupStepDoc `json:"steps"`
}

// SetupStepDoc is one applied step.
type SetupStepDoc struct {
	Step   string `json:"step"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Next   string `json:"next,omitempty"`
}

func setupQuestionsDocOf(inv Inventory, questions []Question) SetupQuestionsDoc {
	entries := make([]SetupInventoryEntry, 0, len(inv.Harnesses))
	for _, s := range inv.Harnesses {
		login := s.Login
		if s.LoggedIn {
			login = ""
		}
		entry := SetupInventoryEntry{
			Harness:  s.Harness,
			OnPath:   s.OnPath,
			LoggedIn: s.LoggedIn,
			Models:   s.Models,
			Login:    login,
		}
		for _, m := range s.Catalog {
			if entry.Providers == nil {
				entry.Providers = map[string]int{}
			}
			p, _, _ := strings.Cut(m, "/")
			entry.Providers[p]++
		}
		entries = append(entries, entry)
	}
	return SetupQuestionsDoc{
		Inventory: entries,
		Notes:     inv.Notes,
		Questions: setupQuestionDocs(questions),
		Next:      setupQuestionsNext(questions),
	}
}

func setupQuestionDocs(qs []Question) []SetupQuestionDoc {
	docs := make([]SetupQuestionDoc, 0, len(qs))
	for _, q := range qs {
		choices := make([]SetupChoiceDoc, 0, len(q.Choices))
		for _, c := range q.Choices {
			choices = append(choices, SetupChoiceDoc{Value: c.Value, Label: c.Label, Why: c.Why, Efforts: c.Efforts})
		}
		docs = append(docs, SetupQuestionDoc{
			ID:          q.ID,
			Prompt:      q.Prompt,
			Multi:       q.Multi,
			Optional:    q.Optional,
			Recommended: q.Recommended,
			Choices:     choices,
		})
	}
	return docs
}

// setupQuestionsNext names the answers still owed; a role question is not named.
func setupQuestionsNext(qs []Question) string {
	parts := make([]string, 0, len(qs))
	for _, q := range qs {
		if q.Optional {
			continue
		}
		parts = append(parts, "--answer "+q.ID+"="+setupQuestionPlaceholder(q))
	}
	return strings.Join(append([]string{"relevo setup"}, parts...), " ")
}

func setupQuestionPlaceholder(q Question) string {
	if len(q.Choices) == 2 && q.Choices[0].Value == "yes" && q.Choices[1].Value == "no" {
		return "<yes|no>"
	}
	return "<...>"
}

func setupResultDocOf(steps []SetupStepDoc) SetupResultDoc {
	return SetupResultDoc{Steps: steps}
}

// setupPrintQuestions prints the questions document and returns the exit
// the caller takes silently.
func setupPrintQuestions(printDoc func(v any) error, doc SetupQuestionsDoc) error {
	if err := printDoc(doc); err != nil {
		return err
	}
	return ErrQuestionsOpen
}

// setupStepDocOf turns a shaped failure into its result row.
func setupStepDocOf(step string, err error) SetupStepDoc {
	var se *StepError
	if errors.As(err, &se) {
		return SetupStepDoc{Step: step, OK: false, Detail: se.Message, Next: se.Next}
	}
	var ce *CodedStepError
	if errors.As(err, &ce) {
		return SetupStepDoc{Step: step, OK: false, Detail: ce.Message, Next: ce.Next}
	}
	var le *LimitError
	if errors.As(err, &le) {
		return SetupStepDoc{Step: step, OK: false, Detail: le.Message, Next: le.Next}
	}
	var na *NotAvailableError
	if errors.As(err, &na) {
		return SetupStepDoc{Step: step, OK: false, Detail: na.Message, Next: "relevo setup"}
	}
	return SetupStepDoc{Step: step, OK: false, Detail: text.FirstLine(err.Error())}
}

// askSetupQuestions walks the open questions in order, reading one answer
// line per question plus one per picked model that runs at several efforts.
// rebuild, when set, re-asks the questions after opencode's once its answer
// narrows the models they offer.
func askSetupQuestions(w io.Writer, r io.Reader, qs []Question, given Answers, rebuild func(Answers) []Question) (Answers, error) {
	sc := bufio.NewScanner(r)
	out := make(Answers, len(qs))
	known := func() Answers {
		all := make(Answers, len(given)+len(out))
		for id, v := range given {
			all[id] = v
		}
		for id, v := range out {
			all[id] = v
		}
		return all
	}
	fmt.Fprintln(w, "Press Enter on any question to take the recommended answer.")
	rolesAsked, customize := false, false
	for i := 0; i < len(qs); i++ {
		q := qs[i]
		if q.ID == "opencode" {
			vals, err := askOpencodeModels(w, sc, q)
			if err != nil {
				return nil, err
			}
			out[q.ID] = vals
			if rebuild != nil {
				qs = append(qs[:i+1:i+1], refreshedQuestions(qs[i+1:], rebuild(known()))...)
			}
			continue
		}
		if q.Optional {
			if !rolesAsked {
				rolesAsked = true
				var err error
				if customize, err = askSetupRoles(w, sc, qs, known()); err != nil {
					return nil, err
				}
			}
			if !customize {
				continue
			}
			q.Recommended = DerivedRoles(qs, known())[q.ID]
		}
		vals, err := askSetupAnswer(w, sc, q)
		if err != nil {
			return nil, err
		}
		out[q.ID] = vals
	}
	return out, nil
}

func refreshedQuestions(rest, rebuilt []Question) []Question {
	out := make([]Question, len(rest))
	for i, q := range rest {
		out[i] = q
		for _, nq := range rebuilt {
			if nq.ID == q.ID {
				out[i] = nq
			}
		}
	}
	return out
}

func askSetupAnswer(w io.Writer, sc *bufio.Scanner, q Question) ([]string, error) {
	vals, spelled, err := askOneSetupQuestion(w, sc, q)
	if err != nil {
		return nil, err
	}
	if !spelled {
		if vals, err = askSetupEfforts(w, sc, q, vals); err != nil {
			return nil, err
		}
	}
	fmt.Fprintf(w, "  -> %s\n", describeSetupAnswer(q, vals))
	return vals, nil
}

func describeSetupAnswer(q Question, vals []string) string {
	parts := make([]string, 0, len(vals))
	for _, v := range vals {
		parts = append(parts, Describe(q, v))
	}
	return strings.Join(parts, ", ")
}

// askSetupRoles shows what the other roles get from the answers so far and
// asks whether to keep that or pick each role; true means pick each role.
func askSetupRoles(w io.Writer, sc *bufio.Scanner, qs []Question, answers Answers) (bool, error) {
	derived := DerivedRoles(qs, answers)
	var lines []string
	for _, q := range qs {
		if q.Optional && len(derived[q.ID]) > 0 {
			lines = append(lines, fmt.Sprintf("  %s (%s): %s", q.ID, RoleWhat[q.ID], describeSetupAnswer(q, derived[q.ID])))
		}
	}
	gate := Question{
		ID:          "roles",
		Prompt:      "The other roles, from your picks:\n" + strings.Join(lines, "\n") + "\nKeep these?",
		Recommended: []string{"keep"},
		Choices: []Choice{
			{Value: "keep", Why: "use them as shown"},
			{Value: "customize", Why: "pick a model for each role"},
		},
	}
	vals, _, err := askOneSetupQuestion(w, sc, gate)
	if err != nil {
		return false, err
	}
	fmt.Fprintf(w, "  -> %s\n", vals[0])
	return vals[0] == "customize", nil
}

// askSetupEfforts asks each picked model's effort, in pick order.
func askSetupEfforts(w io.Writer, sc *bufio.Scanner, q Question, vals []string) ([]string, error) {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		eq, ok := EffortQuestion(q, v)
		if !ok {
			out = append(out, v)
			continue
		}
		effort, _, err := askOneSetupQuestion(w, sc, eq)
		if err != nil {
			return nil, err
		}
		out = append(out, WithEffort(q, v, effort[0]))
	}
	return out, nil
}

// askOneSetupQuestion renders one prompt and reads an answer, re-asking up
// to three times; an empty line takes the recommendation. spelled reports an
// answer typed out as values: it already says its effort, so none is asked.
func askOneSetupQuestion(w io.Writer, sc *bufio.Scanner, q Question) (vals []string, spelled bool, err error) {
	for tries := 0; tries < 3; tries++ {
		writeSetupPrompt(w, q)
		if !sc.Scan() {
			return nil, false, io.ErrUnexpectedEOF
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			return append([]string{}, q.Recommended...), false, nil
		}
		vals, spelled, err := promptAnswerLine(q, line)
		if err == nil {
			return vals, spelled, nil
		}
		fmt.Fprintln(w, err)
	}
	return nil, false, fmt.Errorf("%s: no usable answer after three prompts", q.ID)
}

// writeSetupPrompt renders one question: the prompt, every choice with the
// recommended ones marked, then the answer line.
func writeSetupPrompt(w io.Writer, q Question) {
	fmt.Fprintf(w, "\n%s\n", q.Prompt)
	for i, c := range q.Choices {
		why := ""
		if c.Why != "" {
			why = " -- " + c.Why
		}
		fmt.Fprintf(w, "  %d. %s%s%s\n", i+1, setupChoiceText(c), recommendedMark(q, c), why)
	}
	if q.Multi {
		fmt.Fprintln(w, "  e.g. 1,3 picks two, 1 first")
	}
	fmt.Fprint(w, "> ")
}

func recommendedMark(q Question, c Choice) string {
	for i, r := range q.Recommended {
		if picked, ok := ChoiceFor(q, r); !ok || picked.Value != c.Value {
			continue
		}
		if q.Multi && len(q.Recommended) > 1 {
			return fmt.Sprintf(" (recommended #%d)", i+1)
		}
		return " (recommended)"
	}
	return ""
}

// setupChoiceText carries a choice's label beside its value when the label differs.
func setupChoiceText(c Choice) string {
	if c.Label != "" && c.Label != c.Value {
		return c.Label + " (" + c.Value + ")"
	}
	return c.Value
}

// promptAnswerLine turns one typed line into an answer: a comma list of
// numbers picks choices in the order typed, and anything else parses as an
// --answer flag would and reports itself spelled.
func promptAnswerLine(q Question, line string) ([]string, bool, error) {
	tokens := strings.Split(line, ",")
	if order, ok := numericTokens(tokens); ok {
		vals := make([]string, 0, len(tokens))
		for _, n := range order {
			n--
			if n < 0 || n >= len(q.Choices) {
				return nil, false, fmt.Errorf("%s: pick between 1 and %d", q.ID, len(q.Choices))
			}
			vals = append(vals, WithEffort(q, q.Choices[n].Value, ""))
		}
		return vals, false, nil
	}
	vals, err := ParseAnswer(q, line)
	return vals, true, err
}

// numericTokens reports a comma list of natural numbers, keeping the order typed.
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
