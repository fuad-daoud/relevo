package workflow

import (
	"embed"
	"fmt"
)

//go:embed shipped/default.yaml
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

// ShippedSeeds returns the seed names the shipped workflows resolve.
func ShippedSeeds() []string {
	return []string{"repair", "review", "correct", "scan", "fix"}
}
