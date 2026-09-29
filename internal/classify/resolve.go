package classify

import (
	"strings"

	"github.com/fuad-daoud/relevo/internal/policy"
)

// Resolve builds the classifier from the policy block. Key lookup:
// TYPESAFE_API_KEY from getenv, trimmed, else the db key, trimmed. No key ->
// Unavailable. Never returns an error.
func Resolve(cfg *policy.Classify, key string, getenv func(string) string) (Classifier, Status) {
	if cfg == nil {
		return nil, Status{Configured: false}
	}

	st := Status{
		Configured: true,
		Provider:   cfg.Provider,
		Model:      cfg.ModelName(),
	}

	if getenv != nil {
		if envKey := strings.TrimSpace(getenv("TYPESAFE_API_KEY")); envKey != "" {
			st.KeySource = "env"
			return NewClient(envKey, cfg.ModelName()), st
		}
	}

	if dbKey := strings.TrimSpace(key); dbKey != "" {
		st.KeySource = "db"
		return NewClient(dbKey, cfg.ModelName()), st
	}

	st.KeySource = ""
	return Unavailable{Reason: "no classifier key"}, st
}
