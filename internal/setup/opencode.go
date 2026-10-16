package setup

import (
	"slices"
	"strings"
)

// opencodeShortlist is the OpenRouter lineup a new machine starts from: the
// builder, the lite-planner and the security reviewer the owner runs. They
// are the opencode question's recommendation wherever the catalog has them.
var opencodeShortlist = []string{
	"openrouter/z-ai/glm-5.3-flash",
	"openrouter/deepseek/deepseek-v4.1-flash",
	"openrouter/xiaomi/mimo-v2.6-flash",
}

// opencodeBuilder and opencodePlanner are the lineup's builder and planner;
// the planner runs at the lineup's own variant.
const (
	opencodeBuilder        = "openrouter/z-ai/glm-5.3-flash"
	opencodePlanner        = "openrouter/deepseek/deepseek-v4.1-flash"
	opencodePlannerVariant = "max"
)

// opencodeFree is the provider whose models need no login.
const opencodeFree = "opencode"

// OpencodeNone is the opencode answer that picks no models at all.
const OpencodeNone = "none"

// probeOpencode lists the models opencode can run: `opencode models
// --standalone` prints only logged-in providers' models plus the free ones,
// so the list is the login probe too. A failure falls back to the lineup,
// because a missing key is a doctor finding, not a reason to drop it.
func probeOpencode(env SetupEnv) []string {
	out, exit, err := runProbe(env, "opencode", "models", "--standalone")
	if err != nil || exit != 0 {
		return append([]string{}, opencodeShortlist...)
	}
	var models []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.Count(line, "/") >= 1 && !strings.ContainsAny(line, " \t") {
			models = append(models, line)
		}
	}
	if len(models) == 0 {
		return append([]string{}, opencodeShortlist...)
	}
	return models
}

// defaultOpencodePicks are the models opencode contributes when the opencode
// question goes unanswered: the lineup models the catalog lists, else the
// first model of the first provider that needs a login, else the first.
func defaultOpencodePicks(catalog []string) []string {
	var picks []string
	for _, m := range opencodeShortlist {
		if slices.Contains(catalog, m) {
			picks = append(picks, m)
		}
	}
	if len(picks) > 0 {
		return picks
	}
	for _, m := range catalog {
		if opencodeProvider(m) != opencodeFree {
			return []string{m}
		}
	}
	if len(catalog) > 0 {
		return catalog[:1]
	}
	return nil
}

func opencodeProvider(model string) string {
	provider, _, _ := strings.Cut(model, "/")
	return provider
}

// opencodeQuestion asks which opencode models relevo may use. Its choices are
// the recommended picks plus none; the whole catalog is accepted, most of it
// unlisted, because it runs to hundreds of models.
func opencodeQuestion(s HarnessStatus) Question {
	q := Question{
		ID:       "opencode",
		Prompt:   "Which opencode models may relevo use? Pick one or more, comma-separated; any model `opencode models --standalone` lists is accepted as opencode/<provider>/<model>, and none uses no opencode model",
		Multi:    true,
		Optional: true,
	}
	for _, m := range defaultOpencodePicks(s.Catalog) {
		q.Choices = append(q.Choices, opencodeChoice(m))
		q.Recommended = append(q.Recommended, "opencode/"+m)
	}
	q.Choices = append(q.Choices, Choice{Value: OpencodeNone, Why: "use no opencode model"})
	for _, m := range s.Catalog {
		q.more = append(q.more, opencodeChoice(m))
	}
	return q
}

func opencodeChoice(model string) Choice {
	return Choice{
		Value: "opencode/" + model,
		Label: model,
		Why:   "on opencode (" + opencodeProvider(model) + ")",
		sep:   "#",
		bare:  true,
		open:  true,
	}
}

// OpencodeCatalog is every model the opencode question accepts, listed and
// not, in opencode's own order: what a front end browses by provider.
func OpencodeCatalog(q Question) []Choice {
	return q.more
}

// withOpencodePicks narrows opencode's offer to the models the opencode
// answer picked, so every other question lists those alone; no answer keeps
// the default picks the probe made.
func withOpencodePicks(inv Inventory, answers map[string][]string) Inventory {
	picked := answers["opencode"]
	if len(picked) == 0 {
		return inv
	}
	out := inv
	out.Harnesses = append([]HarnessStatus{}, inv.Harnesses...)
	for i, s := range out.Harnesses {
		if s.Harness != "opencode" {
			continue
		}
		var models []string
		for _, v := range picked {
			if v == OpencodeNone {
				continue
			}
			m := strings.TrimPrefix(v, "opencode/")
			if i := strings.LastIndexByte(m, '#'); i > 0 {
				m = m[:i]
			}
			models = append(models, m)
		}
		out.Harnesses[i].Models = models
	}
	return out
}

// opencodeChoices are opencode's picked models as candidate choices.
func opencodeChoices(s HarnessStatus) []Choice {
	out := make([]Choice, 0, len(s.Models))
	for _, m := range s.Models {
		out = append(out, opencodeChoice(m))
	}
	return out
}
