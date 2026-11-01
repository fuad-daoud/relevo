package setup

import (
	"slices"
	"strings"
)

// opencodeShortlist is the OpenRouter lineup, recommended nowhere else.
var opencodeShortlist = []string{
	"openrouter/z-ai/glm-5.3-flash",
	"openrouter/deepseek/deepseek-v4.1-flash",
	"openrouter/xiaomi/mimo-v2.6-flash",
}

// opencodeBuilder and opencodePlanner are the lineup's builder and planner.
const (
	opencodeBuilder        = "openrouter/z-ai/glm-5.3-flash"
	opencodePlanner        = "openrouter/deepseek/deepseek-v4.1-flash"
	opencodePlannerVariant = "max"
)

// opencodeFree is the provider whose models need no login.
const opencodeFree = "opencode"

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

// opencodeShortlistIn are the lineup models a catalog lists, never a recommendation.
func opencodeShortlistIn(catalog []string) []string {
	var out []string
	for _, m := range opencodeShortlist {
		if slices.Contains(catalog, m) {
			out = append(out, m)
		}
	}
	return out
}

func opencodeProvider(model string) string {
	provider, _, _ := strings.Cut(model, "/")
	return provider
}

// opencodeQuestion asks which opencode models relevo may use. It recommends
// none: the whole catalog is accepted, most of it unlisted, because it runs
// to hundreds of models.
func opencodeQuestion(s HarnessStatus) Question {
	q := Question{
		ID:       "opencode",
		Prompt:   "Which opencode models may relevo use? Pick one or more, comma-separated; any model `opencode models --standalone` lists is accepted as opencode/<provider>/<model>, and none uses no opencode model",
		Multi:    true,
		Optional: true,
	}
	for _, m := range opencodeShortlistIn(s.Catalog) {
		q.Choices = append(q.Choices, opencodeChoice(m))
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

// OpencodeCatalog is every model the opencode question accepts, in opencode's own order.
func OpencodeCatalog(q Question) []Choice {
	return q.more
}

// withOpencodePicks narrows opencode's offer to the models the opencode
// answer picked; no answer leaves opencode offering nothing.
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

func opencodeChoices(s HarnessStatus) []Choice {
	out := make([]Choice, 0, len(s.Models))
	for _, m := range s.Models {
		out = append(out, opencodeChoice(m))
	}
	return out
}
