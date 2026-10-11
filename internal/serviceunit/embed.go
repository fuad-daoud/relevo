// Package serviceunit owns the daemon's service unit files shipped inside the
// binary and the pure decision of what to do with an installed copy.
package serviceunit

import _ "embed"

// The units live here, not in dist/: go:embed cannot reach a parent directory.
// dist/ holds generated copies for people without the binary.
var (
	//go:embed units/relevo.service
	shippedService []byte

	//go:embed units/com.github.fuad-daoud.relevo.plist.in
	shippedPlistTemplate []byte
)

// Names of the shipped units, as the index records them.
const (
	ServiceName = "relevo.service"
	PlistName   = "com.github.fuad-daoud.relevo.plist.in"
)

func ShippedService() []byte {
	return append([]byte(nil), shippedService...)
}

func ShippedPlistTemplate() []byte {
	return append([]byte(nil), shippedPlistTemplate...)
}
