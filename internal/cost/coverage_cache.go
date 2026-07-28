package cost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// defaultCoverageCacheTTL bounds how stale a cached coverage verdict may be.
//
// One hour matches the update-check cache (internal/updatecache) and is safe
// for what this answers: the price table only changes when ape itself is
// replaced — which busts the cache by stamp, not by time — and a newly
// released Claude Code model becoming visible up to an hour late is a
// diagnostic delay, not a wrong cost. Every gate that decides something
// (`ape costs coverage`, `--strict`, `make check-prices`) bypasses the cache.
const defaultCoverageCacheTTL = time.Hour

// coverageCacheEnv overrides the TTL, in seconds. Zero disables the cache.
const coverageCacheEnv = "APE_COVERAGE_CACHE_TTL"

// cachedCoverage is the on-disk shape of ~/.ape/coverage-cache.json.
//
//nolint:tagliatelle // snake_case matches the on-disk contract
type cachedCoverage struct {
	CheckedAt time.Time `json:"checked_at"`
	// TableStamp is the price table's `updated:` value at the time of the
	// sweep. An ape upgrade that ships a different table invalidates the
	// entry immediately rather than waiting out the TTL — otherwise the fix
	// you just installed would appear not to have worked.
	TableStamp string         `json:"table_stamp"`
	Report     CoverageReport `json:"report"`
}

func coverageCachePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "ape-coverage-cache.json")
	}
	return filepath.Join(home, ".ape", "coverage-cache.json")
}

func coverageCacheTTL() time.Duration {
	if v := os.Getenv(coverageCacheEnv); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return defaultCoverageCacheTTL
}

// ObserveModelsCached is ObserveModels with a short-lived on-disk cache, for
// callers that report rather than decide.
//
// A full sweep reads every transcript modified in the window — on an active
// machine that is hundreds of megabytes, and it made `ape doctor` a
// multi-second command in which one check accounted for 99% of the runtime.
// The verdict changes rarely, so re-deriving it on every invocation buys
// nothing.
//
// fromCache reports whether the verdict was reused rather than derived, and
// age how old it is. fromCache is a separate signal on purpose: a cache hit
// written moments ago has an age near zero, so age alone cannot distinguish
// "reused" from "just computed" — and a caller that guessed from age would
// silently present a cached verdict as a fresh one. Callers must disclose
// fromCache. Anything that gates on the result calls ObserveModels directly.
func ObserveModelsCached(home string, since time.Time) (rep CoverageReport, age time.Duration, fromCache bool, err error) {
	ttl := coverageCacheTTL()
	if cached, a, ok := freshCoverageCache(ttl); ok {
		return cached, a, true, nil
	}
	rep, err = ObserveModels(home, since)
	if err != nil {
		return rep, 0, false, err
	}
	if ttl > 0 {
		saveCoverageCache(rep)
	}
	return rep, 0, false, nil
}

// freshCoverageCache returns a cached verdict and its age when one is usable:
// the cache is enabled, readable, was written by a binary carrying the same
// price table, and is younger than the TTL. A clock that has moved backwards
// (negative age) is treated as unusable rather than as infinitely fresh.
func freshCoverageCache(ttl time.Duration) (CoverageReport, time.Duration, bool) {
	if ttl <= 0 {
		return CoverageReport{}, 0, false
	}
	entry, ok := loadCoverageCache()
	if !ok || entry.TableStamp != PriceTableUpdated {
		return CoverageReport{}, 0, false
	}
	age := time.Since(entry.CheckedAt)
	if age < 0 || age >= ttl {
		return CoverageReport{}, 0, false
	}
	return entry.Report, age, true
}

func loadCoverageCache() (cachedCoverage, bool) {
	var entry cachedCoverage
	bs, err := os.ReadFile(coverageCachePath())
	if err != nil {
		return entry, false
	}
	if err := json.Unmarshal(bs, &entry); err != nil {
		return entry, false
	}
	if entry.CheckedAt.IsZero() {
		return entry, false
	}
	return entry, true
}

// saveCoverageCache persists a verdict. Best-effort: a cache that cannot be
// written costs a slower next run, never a wrong answer.
func saveCoverageCache(rep CoverageReport) {
	bs, err := json.Marshal(cachedCoverage{
		CheckedAt:  time.Now().UTC(),
		TableStamp: PriceTableUpdated,
		Report:     rep,
	})
	if err != nil {
		return
	}
	path := coverageCachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, bs, 0o644); err != nil { //nolint:gosec // non-secret diagnostic cache
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}
