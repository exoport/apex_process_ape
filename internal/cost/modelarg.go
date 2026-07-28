package cost

import (
	"maps"
	"strings"
)

// stripDateSuffix removes a trailing `-YYYYMMDD` snapshot stamp so a dated
// model id attributes to its base rate.
//
// Claude Code writes the dated form for some models
// (`claude-haiku-4-5-20251001`) and the bare alias for others. Both bill
// identically, so pricing must not care which one the transcript happened
// to record. Handling it here rather than by enumerating every dated
// variant means a snapshot id that appears in future needs no table change.
func stripDateSuffix(s string) string {
	i := strings.LastIndexByte(s, '-')
	if i < 0 || len(s)-i-1 != 8 {
		return s
	}
	for _, c := range s[i+1:] {
		if c < '0' || c > '9' {
			return s
		}
	}
	return s[:i]
}

// foldModelPunctuation lowercases a model identifier and folds the
// separators users actually type — `Claude_Sonnet_5`, `claude sonnet 5`,
// and `claude-sonnet-4.6` all mean the same model as their canonical
// dashed-lowercase form. Runs of separators collapse and leading/trailing
// ones are trimmed, so a stray dash can't produce an empty segment.
//
// Transcript-recorded ids are already canonical, so this is a no-op on the
// pricing hot path; it exists for the identifiers humans write by hand
// (a spec's `model:` field, a `--model` flag).
func foldModelPunctuation(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("_", "-", " ", "-", ".", "-").Replace(s)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

// splitContextSuffix separates a `[...]` context-window suffix from the
// model id. `opus[1m]` → ("opus", "[1m]"). The suffix is opaque to ape and
// is reattached verbatim after canonicalization.
func splitContextSuffix(s string) (base, suffix string) {
	if i := strings.IndexByte(s, '['); i > 0 {
		return strings.TrimSpace(s[:i]), s[i:]
	}
	return strings.TrimSpace(s), ""
}

// CanonicalModelArg normalizes a user-supplied model argument — a
// `--model` flag or a pipeline spec's `model:` field — into a form Claude
// Code accepts, and reports whether ape recognized it.
//
// Accepted spellings, all resolving to a concrete model id:
//
//	sonnet · Sonnet · claude-sonnet          → "claude-sonnet-5"
//	sonnet-5 · claude-sonnet-5               → "claude-sonnet-5"
//	claude-sonnet-4.6 · claude_sonnet_4_6    → "claude-sonnet-4-6"
//	opus · Opus · claude-opus                → "claude-opus-5"
//	opus[1m] · Opus[1m]                      → "claude-opus-5[1m]" (suffix kept)
//
// A bare family word RESOLVES to the current generation of that family from
// the `aliases:` block of prices.yaml, rather than being passed through for
// Claude Code to resolve. Writing `sonnet` in a spec therefore spawns a
// concrete, recorded model id, and the manifest's per-model attribution
// matches the transcript's without an alias hop.
//
// The cost of pinning is that a stale alias table silently selects an older
// generation — pass-through could not do that, because Claude Code always
// knows its own current model. That risk is covered rather than accepted:
// ObserveModels reports an alias as DRIFTED when the local Claude Code is
// emitting a different generation of the same family than the table points
// at, `ape doctor` surfaces it, and `make check-prices` fails the release
// gate on it. Keep the alias block current and the pin is exact; let it rot
// and the tooling says so out loud.
//
// recognized=false means ape could not attribute the string to a known
// family. The canonical form is still returned and callers should still
// pass it to Claude Code — a model that shipped after this binary must not
// be blocked by ape. Callers surface a warning so a genuine typo
// ("claude-sonet-5") is visible rather than silently spawning a session
// that Claude Code will reject.
//
// Empty input returns ("", true): nothing supplied, nothing to validate.
func CanonicalModelArg(raw string) (canonical string, recognized bool) {
	base, suffix := splitContextSuffix(raw)
	norm := foldModelPunctuation(base)
	if norm == "" {
		return "", true
	}
	// A bare alias resolves to the concrete current generation.
	if full, ok := modelAliases[norm]; ok {
		return full + suffix, true
	}
	// An exact table id is already canonical.
	if _, ok := Prices[norm]; ok {
		return norm + suffix, true
	}
	stem := strings.TrimPrefix(norm, "claude-")
	for _, f := range familyTiers {
		if stem == f.Family {
			// `claude-sonnet` — the family word wearing a `claude-` prefix.
			// Same meaning as the bare alias, so same resolution.
			if full, ok := modelAliases[f.Family]; ok {
				return full + suffix, true
			}
			return f.Family + suffix, true
		}
		if strings.HasPrefix(stem, f.Family+"-") {
			return "claude-" + stem + suffix, true
		}
	}
	return norm + suffix, false
}

// ResolveFamilyAlias returns the concrete model id a bare family word maps
// to ("opus" → "claude-opus-5"), or "" when the word is not a known alias.
func ResolveFamilyAlias(family string) string { return modelAliases[family] }

// FamilyAliases returns the alias → concrete-id map, for reporting.
func FamilyAliases() map[string]string {
	out := make(map[string]string, len(modelAliases))
	maps.Copy(out, modelAliases)
	return out
}

// ModelFamily returns the family word a model id belongs to ("opus",
// "sonnet", …), or "" when ape cannot attribute it. Used for reporting and
// for the family-tier price fallback.
func ModelFamily(model string) string {
	norm := NormalizeModel(model)
	for _, f := range familyTiers {
		if f.matches(norm) {
			return f.Family
		}
	}
	return ""
}
