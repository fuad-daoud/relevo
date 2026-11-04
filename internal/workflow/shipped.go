package workflow

import (
	"embed"
	"fmt"
)

//go:embed shipped/default.yaml shipped/direct-fix.yaml
var shippedFS embed.FS

// Default returns the shipped default workflow. It panics only when the
// embedded definition does not parse, which is a build defect.
func Default() Definition {
	data, err := shippedFS.ReadFile("shipped/default.yaml")
	if err != nil {
		panic(fmt.Sprintf("workflow: read shipped default: %v", err))
	}
	def, err := Parse(data)
	if err != nil {
		panic(fmt.Sprintf("workflow: shipped default does not parse: %v", err))
	}
	return def
}

func ShippedSeeds() []string {
	return []string{"repair", "review", "correct", "scan", "fix", "fix-direct"}
}

// Shipped returns the shipped workflow with the given name, or false when no
// shipped file carries it.
func Shipped(name string) (Definition, bool) {
	data, err := shippedFS.ReadFile("shipped/" + name + ".yaml")
	if err != nil {
		return Definition{}, false
	}
	def, err := Parse(data)
	if err != nil {
		return Definition{}, false
	}
	return def, true
}

// IsShipped reports whether def is one of the shipped workflows, by name.
func IsShipped(def Definition) bool {
	_, ok := Shipped(def.Name)
	return ok
}
