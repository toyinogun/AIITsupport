// Package eval holds what the agent and, from feature #20, the eval harness
// share about model cost: the per model prices in pricing.yaml (spec 0003,
// spec 0008). Prices are never hardcoded in Go; they live in that file.
package eval

import (
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed pricing.yaml
var pricingYAML []byte

// ErrUnknownModel means pricing.yaml has no entry for the model.
var ErrUnknownModel = errors.New("eval: no price for this model in eval/pricing.yaml")

// priceScale is how prices are held: USD per million tokens times 10^4, so a
// price with up to 4 decimals is a whole number.
const priceScale = 10_000

// ModelPrice is one model's prices in USD per million tokens times 10^4.
type ModelPrice struct {
	Model       string
	PricingDate time.Time
	Input       int64
	Output      int64
	CacheRead   int64
	CacheWrite  int64 // the 5 minute cache write price
}

// Tokens are the four token counts of one model call's usage.
type Tokens struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

// CostMicroUSD is the call's cost in micro US dollars, rounded half up. One
// USD per million tokens is one micro USD per token.
func (p ModelPrice) CostMicroUSD(t Tokens) int64 {
	sum := t.Input*p.Input + t.Output*p.Output + t.CacheRead*p.CacheRead + t.CacheWrite*p.CacheWrite
	return (sum + priceScale/2) / priceScale
}

// Price returns the prices for model from the embedded pricing.yaml. A
// malformed file is an error for every model.
func Price(model string) (ModelPrice, error) {
	table, err := loadTable()
	if err != nil {
		return ModelPrice{}, err
	}
	p, ok := table[model]
	if !ok {
		return ModelPrice{}, fmt.Errorf("%w: %q", ErrUnknownModel, model)
	}
	return p, nil
}

var loadTable = sync.OnceValues(func() (map[string]ModelPrice, error) {
	return parseTable(pricingYAML)
})

type rawEntry struct {
	PricingDate string `yaml:"pricing_date"`
	Input       string `yaml:"input"`
	Output      string `yaml:"output"`
	CacheRead   string `yaml:"cache_read"`
	CacheWrite  string `yaml:"cache_write"`
}

func parseTable(data []byte) (map[string]ModelPrice, error) {
	var doc struct {
		Models map[string]rawEntry `yaml:"models"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("eval: parse pricing.yaml: %w", err)
	}
	if len(doc.Models) == 0 {
		return nil, errors.New("eval: pricing.yaml lists no models")
	}
	table := make(map[string]ModelPrice, len(doc.Models))
	var errs []error
	for model, raw := range doc.Models {
		p, err := parseEntry(model, raw)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		table[model] = p
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return table, nil
}

func parseEntry(model string, raw rawEntry) (ModelPrice, error) {
	p := ModelPrice{Model: model}
	date, err := time.Parse(time.DateOnly, raw.PricingDate)
	if err != nil {
		return p, fmt.Errorf("eval: %s: pricing_date must be YYYY-MM-DD", model)
	}
	p.PricingDate = date
	var errs []error
	for _, f := range []struct {
		name string
		raw  string
		dst  *int64
	}{
		{"input", raw.Input, &p.Input},
		{"output", raw.Output, &p.Output},
		{"cache_read", raw.CacheRead, &p.CacheRead},
		{"cache_write", raw.CacheWrite, &p.CacheWrite},
	} {
		v, err := parsePrice(f.raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("eval: %s: %s: %w", model, f.name, err))
			continue
		}
		*f.dst = v
	}
	return p, errors.Join(errs...)
}

var priceFormat = regexp.MustCompile(`^\d+(\.\d{1,4})?$`)

// parsePrice turns "2.5" into 25000: a non negative decimal with at most 4
// decimals, scaled by 10^4.
func parsePrice(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("missing")
	}
	if !priceFormat.MatchString(s) {
		return 0, fmt.Errorf("%q is not a non negative price with at most 4 decimals", s)
	}
	whole, frac, _ := strings.Cut(s, ".")
	frac += strings.Repeat("0", 4-len(frac))
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is out of range", s)
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is out of range", s)
	}
	return w*priceScale + f, nil
}
