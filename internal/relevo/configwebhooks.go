package relevo

import (
	"encoding/json"

	"github.com/fuad-daoud/relevo/internal/policy"
)

// SetWebhooks replaces the stored webhook list with hooks, validated like any
// policy edit. An empty list clears notify whole, so the row returns to its
// default rather than keeping an empty array behind it. Each hook goes through
// a JSON round trip, so policy.Webhook's omitempty rules decide the stored
// shape: a json format and an empty event list both vanish.
func SetWebhooks(d ConfigDoc, hooks []policy.Webhook, message string) (ConfigEdit, error) {
	if len(hooks) == 0 {
		return EditPolicy(d, []PolicySet{{Path: "notify", Value: nil}}, message)
	}

	list := make([]any, 0, len(hooks))
	for _, hook := range hooks {
		// json is the default the policy schema reads from an absent key, so
		// an explicit "json" is stored as the omitted "".
		if hook.Format == "json" {
			hook.Format = ""
		}
		raw, err := json.Marshal(hook)
		if err != nil {
			return ConfigEdit{}, &FieldError{"", err.Error()}
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return ConfigEdit{}, &FieldError{"", err.Error()}
		}
		list = append(list, m)
	}
	return EditPolicy(d, []PolicySet{{Path: "notify.webhooks", Value: list}}, message)
}
