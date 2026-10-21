package eval

import (
	"errors"
	"strings"
	"testing"
)

// covers: AC-15. Both shipped models have a dated price; an unknown model is refused.
func TestPriceForShippedModels(t *testing.T) {
	for _, model := range []string{"claude-sonnet-5-5", "claude-haiku-5-5"} {
		p, err := Price(model)
		if err != nil {
			t.Fatalf("Price(%q): %v", model, err)
		}
		if p.PricingDate.IsZero() || p.Input <= 0 || p.Output <= 0 || p.CacheRead <= 0 || p.CacheWrite <= 0 {
			t.Errorf("Price(%q) = %+v, want every price set", model, p)
		}
	}
	if _, err := Price("claude-unknown-1"); !errors.Is(err, ErrUnknownModel) {
		t.Errorf("Price(unknown) = %v, want ErrUnknownModel", err)
	}
}

func TestParsePrice(t *testing.T) {
	for in, want := range map[string]int64{"2": 20000, "2.5": 25000, "0.0100": 100, "10.0000": 100000, "0": 0} {
		if got, err := parsePrice(in); err != nil || got != want {
			t.Errorf("parsePrice(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "-1", "1.23456", "abc", "1.", ".5", "1e3"} {
		if _, err := parsePrice(bad); err == nil {
			t.Errorf("parsePrice(%q) succeeded", bad)
		}
	}
}

func TestParseTableRejectsBadEntries(t *testing.T) {
	cases := map[string]string{
		"missing key":   "models:\n  m:\n    pricing_date: 2026-10-06\n    input: \"1\"\n    output: \"1\"\n    cache_read: \"1\"\n",
		"negative":      "models:\n  m:\n    pricing_date: 2026-10-06\n    input: \"-1\"\n    output: \"1\"\n    cache_read: \"1\"\n    cache_write: \"1\"\n",
		"five decimals": "models:\n  m:\n    pricing_date: 2026-10-06\n    input: \"1.00001\"\n    output: \"1\"\n    cache_read: \"1\"\n    cache_write: \"1\"\n",
		"bad date":      "models:\n  m:\n    pricing_date: soon\n    input: \"1\"\n    output: \"1\"\n    cache_read: \"1\"\n    cache_write: \"1\"\n",
		"no models":     "models: {}\n",
	}
	for name, doc := range cases {
		if _, err := parseTable([]byte(doc)); err == nil {
			t.Errorf("%s: parseTable succeeded", name)
		}
	}
}

// covers: AC-15. cost = (Σ tokens × price + 5000) / 10000 micro USD.
func TestCostMicroUSD(t *testing.T) {
	p := ModelPrice{Input: 20000, Output: 100000, CacheRead: 2000, CacheWrite: 25000}
	// 1000 in × 2 + 200 out × 10 + 5000 read × 0.2 + 100 write × 2.5 = 2000 + 2000 + 1000 + 250
	got := p.CostMicroUSD(Tokens{Input: 1000, Output: 200, CacheRead: 5000, CacheWrite: 100})
	if got != 5250 {
		t.Errorf("cost = %d, want 5250", got)
	}
	// Rounds half up: one cache read token at 0.0100 is 0.01 micro USD, rounds to 0;
	// 50 of them are 0.5, which rounds to 1.
	h := ModelPrice{CacheRead: 100}
	if c := h.CostMicroUSD(Tokens{CacheRead: 1}); c != 0 {
		t.Errorf("tiny cost = %d, want 0", c)
	}
	if c := h.CostMicroUSD(Tokens{CacheRead: 50}); c != 1 {
		t.Errorf("half cost = %d, want 1", c)
	}
	if !strings.Contains(ErrUnknownModel.Error(), "pricing.yaml") {
		t.Error("ErrUnknownModel should name the file")
	}
}

// covers: spec 0014 AC-18. Cache reads count at their price over the input
// price, rounded down once; equal prices, a free input, or a cache read
// dearer than input count them in full.
func TestBudgetTokens(t *testing.T) {
	sonnet, err := Price("claude-sonnet-5-5")
	if err != nil {
		t.Fatal(err)
	}
	rows := Tokens{Input: 1000, Output: 100, CacheRead: 2000, CacheWrite: 500}
	if got := sonnet.BudgetTokens(rows); got != 1800 {
		t.Errorf("sonnet = %d, want 1800", got)
	}
	if got := sonnet.BudgetTokens(Tokens{CacheRead: 19}); got != 1 {
		t.Errorf("19 cache reads = %d, want 1 (rounded down)", got)
	}
	for name, p := range map[string]ModelPrice{
		"equal prices":     {Input: 20000, CacheRead: 20000},
		"free input":       {Input: 0, CacheRead: 2000},
		"dearer than read": {Input: 1000, CacheRead: 2000},
	} {
		if got := p.BudgetTokens(rows); got != 3600 {
			t.Errorf("%s = %d, want 3600 (cache reads in full)", name, got)
		}
	}
}
