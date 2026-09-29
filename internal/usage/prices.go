package usage

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

//go:embed prices_default.json
var defaultPricesJSON []byte

// ErrBadPrices reports a prices.json that does not validate. The caller prints it
// and continues with the default: a bad price file must never stop a round from
// closing.
var ErrBadPrices = errors.New("prices.json does not validate")

type ModelPrice struct {
	In         float64 `json:"in"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
	Out        float64 `json:"out"`
}

// Prices is the table. Models is keyed "provider/model", as candidate refs use
// minus the harness.
type Prices struct {
	AsOf   string                `json:"as_of"`
	Source string                `json:"source"`
	Models map[string]ModelPrice `json:"models"`
}

func DefaultPrices() Prices {
	var p Prices
	if err := json.Unmarshal(defaultPricesJSON, &p); err != nil {
		panic("prices_default.json: " + err.Error()) // a build artefact, caught by TestDefaultPricesParses
	}
	if p.Models == nil {
		p.Models = map[string]ModelPrice{}
	}
	return p
}

// LoadPrices reads path over the embedded default: a missing file is the default,
// a present file's rows replace default rows of the same key, and a malformed file
// is ErrBadPrices.
func LoadPrices(path string) (Prices, error) {
	base := DefaultPrices()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return base, nil
	}
	if err != nil {
		return base, fmt.Errorf("%s: %w", path, err)
	}
	p, err := ParsePrices(raw)
	if err != nil {
		return base, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// ParsePrices returns the embedded default overlaid by the rows in data. Errors
// wrap ErrBadPrices and carry no path: LoadPrices adds the file's.
func ParsePrices(data []byte) (Prices, error) {
	base := DefaultPrices()
	var file Prices
	if err := json.Unmarshal(data, &file); err != nil {
		return base, fmt.Errorf("%w: %w", err, ErrBadPrices)
	}
	for k, m := range file.Models {
		if m.In < 0 || m.CacheRead < 0 || m.CacheWrite < 0 || m.Out < 0 {
			return base, fmt.Errorf("%s: negative price: %w", k, ErrBadPrices)
		}
		base.Models[k] = m
	}
	if file.AsOf != "" {
		base.AsOf = file.AsOf
	}
	if file.Source != "" {
		base.Source = file.Source
	}
	return base, nil
}

// Estimate prices t for provider/model; ok is false when there is no row.
func (p Prices) Estimate(provider, model string, t Tokens) (float64, bool) {
	m, ok := p.Models[provider+"/"+model]
	if !ok {
		return 0, false
	}
	const million = 1_000_000
	usd := float64(t.In)*m.In/million +
		float64(t.CacheRead)*m.CacheRead/million +
		float64(t.CacheWrite)*m.CacheWrite/million +
		float64(t.Out)*m.Out/million
	return usd, true
}
