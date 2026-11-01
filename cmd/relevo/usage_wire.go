package main

import (
	// usagepkg: package main already names a const "usage", so internal/usage
	// is imported under a name that does not shadow it.
	usagepkg "github.com/fuad-daoud/relevo/internal/usage"
)

// newUsageReader wires the usage reader and the price table from the loaded
// config. Neither can fail, so wiring it never stops a round from closing.
func newUsageReader(prices usagepkg.Prices) (usagepkg.Reader, usagepkg.Prices) {
	return usagepkg.New(), prices
}
