package account

import (
	"slices"
	"strings"
)

// GateKey encodes an account gate as group@account. An empty account is the
// bare group entry, which gates every account of the group.
func GateKey(group, accountName string) string {
	if accountName == "" {
		return group
	}
	return group + "@" + accountName
}

// ParseGateKey splits a gate key into its group and account; a bare group
// decodes with an empty account. ok is false for an empty key or a key with an
// empty side, which GateKey never produces.
func ParseGateKey(key string) (group, accountName string, ok bool) {
	if key == "" {
		return "", "", false
	}
	group, accountName, found := strings.Cut(key, "@")
	if !found {
		return key, "", true
	}
	if group == "" || accountName == "" || strings.Contains(accountName, "@") {
		return "", "", false
	}
	return group, accountName, true
}

// Gated reports whether a live gate covers a: a bare entry for a group a
// serves, or group@a.Name.
func Gated(a Account, gates []string) bool {
	for _, g := range gates {
		group, accountName, ok := ParseGateKey(g)
		if !ok || !slices.Contains(a.Groups, group) {
			continue
		}
		if accountName == "" || accountName == a.Name {
			return true
		}
	}
	return false
}
