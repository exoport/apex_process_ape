package apexv1

import "slices"

// rc3-f STUB — the integrator deletes this file: rc3-e owns the
// config.changed guard list (guardedConfigKeys) and adds human_review to
// it. Idempotent, so it is harmless if left in after that merge.
func init() {
	if !slices.Contains(guardedConfigKeys, "human_review") {
		guardedConfigKeys = append(guardedConfigKeys, "human_review")
	}
}
