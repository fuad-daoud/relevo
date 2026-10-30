package setup

// opencode_cli.go asks the opencode question on a terminal: a catalog runs
// to hundreds of models, so the reader picks providers first, then models
// -- a short provider's whole list, a long one's recommended shortlist and
// a name search.

import (
	"bufio"
	"fmt"
	"io"
	"slices"
	"strings"
)

// opencodeListMax is the longest model list shown whole; a longer provider
// gets its shortlist and a search instead.
const opencodeListMax = 30

// opencodeProviderModels is one provider's models, in catalog order, by the
// model's id under the provider.
type opencodeProviderModels struct {
	name   string
	models []string
}

func opencodeProviders(q Question) []opencodeProviderModels {
	var out []opencodeProviderModels
	at := map[string]int{}
	for _, c := range OpencodeCatalog(q) {
		name, model, _ := strings.Cut(c.Label, "/")
		i, ok := at[name]
		if !ok {
			i = len(out)
			at[name] = i
			out = append(out, opencodeProviderModels{name: name})
		}
		out[i].models = append(out[i].models, model)
	}
	return out
}

// opencodeValue is the opencode answer value for one provider's model.
func opencodeValue(provider, model string) string {
	return "opencode/" + provider + "/" + model
}

// askOpencodeModels walks the providers and returns the picked models as
// opencode answer values, or none.
func askOpencodeModels(w io.Writer, sc *bufio.Scanner, q Question) ([]string, error) {
	providers := opencodeProviders(q)
	pq := Question{
		ID:     "opencode",
		Prompt: "opencode can run models from these providers. Which should relevo use models from? Pick one or more, comma-separated; Enter skips opencode",
		Multi:  true,
	}
	for _, p := range providers {
		why := fmt.Sprintf("%d models", len(p.models))
		if p.name == "opencode" {
			why += ", free"
		}
		pq.Choices = append(pq.Choices, Choice{Value: p.name, Why: why})
	}
	pq.Choices = append(pq.Choices, Choice{Value: OpencodeNone, Why: "use no opencode model; relevo config set candidates adds one later"})
	names, _, err := askOneSetupQuestion(w, sc, pq)
	if err != nil {
		return nil, err
	}
	var picks []string
	for _, p := range providers {
		if !slices.Contains(names, p.name) {
			continue
		}
		got, err := askProviderModels(w, sc, p, shortlistValues(q))
		if err != nil {
			return nil, err
		}
		picks = append(picks, got...)
	}
	if len(picks) == 0 {
		fmt.Fprintln(w, "  -> no opencode model")
		return []string{OpencodeNone}, nil
	}
	fmt.Fprintf(w, "  -> %s\n", strings.Join(trimOpencode(picks), ", "))
	return picks, nil
}

// shortlistValues are the lineup models the opencode question lists: the
// starting list a long provider offers once the reader picked it.
func shortlistValues(q Question) []string {
	var out []string
	for _, c := range q.Choices {
		if c.Value != OpencodeNone {
			out = append(out, c.Value)
		}
	}
	return out
}

func trimOpencode(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = strings.TrimPrefix(v, "opencode/")
	}
	return out
}

// askProviderModels picks one provider's models: the whole list when it is
// short, else a few lineup models to start from, then a search. Nothing is
// pre-picked: Enter adds none.
func askProviderModels(w io.Writer, sc *bufio.Scanner, p opencodeProviderModels, lineup []string) ([]string, error) {
	var shortlist []string
	for _, m := range p.models {
		if slices.Contains(lineup, opencodeValue(p.name, m)) {
			shortlist = append(shortlist, m)
		}
	}
	list := p.models
	if len(list) > opencodeListMax {
		list = shortlist
	}
	var picked []string
	if len(list) > 0 {
		mq := Question{ID: p.name, Multi: true,
			Prompt: fmt.Sprintf("Which %s models? Pick one or more, comma-separated; Enter adds none", p.name)}
		if len(p.models) > opencodeListMax {
			mq.Prompt = fmt.Sprintf("%s has %d models; here are a few to start from. Which of them? Enter adds none", p.name, len(p.models))
		}
		for _, m := range list {
			mq.Choices = append(mq.Choices, Choice{Value: m})
		}
		got, _, err := askOneSetupQuestion(w, sc, mq)
		if err != nil {
			return nil, err
		}
		picked = got
	}
	if len(p.models) > opencodeListMax {
		more, err := searchProviderModels(w, sc, p, picked)
		if err != nil {
			return nil, err
		}
		picked = append(picked, more...)
	}
	out := make([]string, 0, len(picked))
	for _, m := range picked {
		out = append(out, opencodeValue(p.name, m))
	}
	return out, nil
}

// searchProviderModels adds models from a long provider by name: a typed
// word lists the models holding it to pick from, a typed full id is taken
// as it is, and an empty line ends the search.
func searchProviderModels(w io.Writer, sc *bufio.Scanner, p opencodeProviderModels, have []string) ([]string, error) {
	var added []string
	for {
		fmt.Fprintf(w, "\nAdd more %s models: type part of a name (e.g. qwen) to search, or press Enter to finish\n> ", p.name)
		if !sc.Scan() {
			return nil, io.ErrUnexpectedEOF
		}
		word := strings.TrimSpace(sc.Text())
		switch {
		case word == "":
			return added, nil
		case slices.Contains(p.models, word):
			if !slices.Contains(have, word) && !slices.Contains(added, word) {
				added = append(added, word)
			}
			fmt.Fprintf(w, "  + %s\n", word)
			continue
		}
		matches := matchingModels(p.models, word)
		if len(matches) == 0 {
			fmt.Fprintf(w, "  no %s model holds %q\n", p.name, word)
			continue
		}
		mq := Question{ID: p.name + "-search", Multi: true,
			Prompt: fmt.Sprintf("%d %s models hold %q. Which? Enter adds none", len(matches), p.name, word)}
		if len(matches) > opencodeListMax {
			mq.Prompt = fmt.Sprintf("%d %s models hold %q; the first %d are listed, type more of the name to narrow. Which? Enter adds none", len(matches), p.name, word, opencodeListMax)
			matches = matches[:opencodeListMax]
		}
		for _, m := range matches {
			mq.Choices = append(mq.Choices, Choice{Value: m})
		}
		got, _, err := askOneSetupQuestion(w, sc, mq)
		if err != nil {
			return nil, err
		}
		for _, m := range got {
			if !slices.Contains(have, m) && !slices.Contains(added, m) {
				added = append(added, m)
				fmt.Fprintf(w, "  + %s\n", m)
			}
		}
	}
}

func matchingModels(models []string, word string) []string {
	word = strings.ToLower(word)
	var out []string
	for _, m := range models {
		if strings.Contains(strings.ToLower(m), word) {
			out = append(out, m)
		}
	}
	return out
}

// opencodeInventoryText is opencode's part of the probe line: each provider
// with its model count, since opencode is never asked for a login and the
// list is what it can run.
func opencodeInventoryText(s HarnessStatus) string {
	counts := map[string]int{}
	var order []string
	for _, m := range s.Catalog {
		p, _, _ := strings.Cut(m, "/")
		if counts[p] == 0 {
			order = append(order, p)
		}
		counts[p]++
	}
	parts := make([]string, 0, len(order))
	for _, p := range order {
		free := ""
		if p == "opencode" {
			free = " free"
		}
		parts = append(parts, fmt.Sprintf("%s (%d%s models)", p, counts[p], free))
	}
	if len(parts) == 0 {
		return "opencode: on PATH"
	}
	return "opencode: " + strings.Join(parts, ", ")
}
