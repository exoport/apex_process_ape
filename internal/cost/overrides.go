package cost

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// overridesPath returns ~/.ape/prices.yaml. Used by both LoadOverrides
// and SaveOverrides. Tests inject a different home via t.Setenv("HOME").
func overridesPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "ape-prices.yaml")
	}
	return filepath.Join(home, ".ape", "prices.yaml")
}

// overridesShape is the on-disk schema for ~/.ape/prices.yaml.
//
//	prices:
//	  claude-opus-4-7:
//	    base_input: 5.00
//	    output:    25.00
//	  claude-sonnet-5:
//	    base_input: 3.00
//	    output:    15.00
//	    effective_from: 2026-09-01   # optional; override activates on/after this date
//
// effective_from is the optional dating hook (PLAN-10 D3): absent, the
// override wins unconditionally (unchanged legacy behaviour); present, it
// applies only to turns timestamped at/after it, so a dateless Lookup
// stays on the conservative built-in rate.
type overridesShape struct {
	Prices map[string]priceRow `yaml:"prices"`
}

type priceRow struct {
	BaseInput     float64 `yaml:"base_input"`
	Output        float64 `yaml:"output"`
	EffectiveFrom string  `yaml:"effective_from,omitempty"`
}

// overrideEntry is a parsed override: the price plus the optional date it
// takes effect from (zero = always).
type overrideEntry struct {
	Price ModelPrice
	From  time.Time
}

var (
	overridesMu     sync.RWMutex
	loadedOverrides map[string]overrideEntry
	overridesLoaded bool
)

// LoadOverridesFrom reads a price-override YAML file and parses it
// into a map. Used by `ape costs update --from <file>` to validate
// before persisting. PLAN-5 / C7.
func LoadOverridesFrom(path string) (map[string]ModelPrice, error) {
	bs, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cost.LoadOverridesFrom: %w", err)
	}
	var raw overridesShape
	if err := yaml.Unmarshal(bs, &raw); err != nil {
		return nil, fmt.Errorf("cost.LoadOverridesFrom: parse: %w", err)
	}
	if len(raw.Prices) == 0 {
		return nil, errors.New("cost.LoadOverridesFrom: no `prices:` map in file")
	}
	out := make(map[string]ModelPrice, len(raw.Prices))
	for k, v := range raw.Prices {
		if v.BaseInput < 0 || v.Output < 0 {
			return nil, fmt.Errorf("cost.LoadOverridesFrom: model %q has negative price", k)
		}
		if _, err := parseEffectiveFrom(v.EffectiveFrom); err != nil {
			return nil, fmt.Errorf("cost.LoadOverridesFrom: model %q: %w", k, err)
		}
		out[k] = ModelPrice{BaseInput: v.BaseInput, Output: v.Output}
	}
	return out, nil
}

// parseEffectiveFrom parses an override's optional effective_from. Empty
// means "always" (zero time). Accepts an RFC3339 timestamp or a bare
// YYYY-MM-DD date (interpreted as midnight UTC).
func parseEffectiveFrom(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid effective_from %q (want RFC3339 or YYYY-MM-DD)", s)
	}
	return t.UTC(), nil
}

// SaveOverrides writes prices to ~/.ape/prices.yaml. Subsequent Lookup
// calls see the new values until process exit. PLAN-5 / C7.
func SaveOverrides(prices map[string]ModelPrice) error {
	shape := overridesShape{Prices: make(map[string]priceRow, len(prices))}
	for k, v := range prices {
		shape.Prices[k] = priceRow{BaseInput: v.BaseInput, Output: v.Output}
	}
	bs, err := yaml.Marshal(shape)
	if err != nil {
		return err
	}
	path := overridesPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, bs, 0o644); err != nil { //nolint:gosec // user-visible config file; world-readable is intentional
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	// Drop cache so the next Lookup picks the new values up.
	overridesMu.Lock()
	loadedOverrides = nil
	overridesLoaded = false
	overridesMu.Unlock()
	return nil
}

// loadOverridesOnce reads ~/.ape/prices.yaml on first call, caches the
// result. Called transparently from LookupAt; returns an empty map on
// any error.
func loadOverridesOnce() map[string]overrideEntry {
	overridesMu.RLock()
	if overridesLoaded {
		defer overridesMu.RUnlock()
		return loadedOverrides
	}
	overridesMu.RUnlock()

	overridesMu.Lock()
	defer overridesMu.Unlock()
	if overridesLoaded {
		return loadedOverrides
	}
	overridesLoaded = true
	loadedOverrides = map[string]overrideEntry{}
	bs, err := os.ReadFile(overridesPath())
	if err != nil {
		return loadedOverrides
	}
	var raw overridesShape
	if err := yaml.Unmarshal(bs, &raw); err != nil {
		return loadedOverrides
	}
	for k, v := range raw.Prices {
		// A malformed effective_from disables that row's dating rather than
		// the whole file — the price still applies (unconditionally).
		from, _ := parseEffectiveFrom(v.EffectiveFrom)
		loadedOverrides[k] = overrideEntry{
			Price: ModelPrice{BaseInput: v.BaseInput, Output: v.Output},
			From:  from,
		}
	}
	return loadedOverrides
}
