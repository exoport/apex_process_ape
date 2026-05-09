package updatecache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const defaultCheckTTL = 1 * time.Hour

// CacheEntry holds the cached version check result.
type CacheEntry struct {
	CheckedAt     time.Time `json:"checkedAt"`
	LatestVersion string    `json:"latestVersion"`
}

func cachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "ape", "version-check.json")
}

func ttl() time.Duration {
	if v := os.Getenv("APE_UPDATE_CHECK_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultCheckTTL
}

// Load reads the cache entry if it exists and has not expired.
// Returns nil if the cache is missing or stale.
func Load() *CacheEntry {
	data, err := os.ReadFile(cachePath())
	if err != nil {
		return nil
	}

	var entry CacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil
	}

	if time.Since(entry.CheckedAt) > ttl() {
		return nil
	}

	return &entry
}

// Save writes a new cache entry with the current time.
func Save(latestVersion string) {
	entry := CacheEntry{
		CheckedAt:     time.Now(),
		LatestVersion: latestVersion,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}

	path := cachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}

	_ = os.WriteFile(path, data, 0o644) //nolint:gosec // version cache is non-sensitive public version metadata
}
