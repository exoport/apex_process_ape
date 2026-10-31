package rebuild

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/runlog"
)

// Collect reads what this checkout alone wrote (§4.4 step 2): its
// events.jsonl lines, its run manifests (keyed <kind>.<run id>) and its
// attempt counts (keyed by change), under user's and the project's tokens.
func Collect(p *apexv1.Project, user, project string) (Records, error) {
	checkout, err := events.CheckoutID(p.StateDir())
	if err != nil {
		return Records{}, err
	}
	r := Records{
		User: user, Project: project, Checkout: checkout, Events: p.EventsSince(0),
		Runs: map[string][]byte{}, Attempts: map[string][]byte{},
	}
	for _, root := range runlog.RunRoots(p.Root) {
		pattern := filepath.Join(root.Path, "*", "manifest.yaml")
		if root.Grouped {
			pattern = filepath.Join(root.Path, "*", "*", "manifest.yaml")
		}
		paths, _ := filepath.Glob(pattern)
		for _, m := range paths {
			data, err := os.ReadFile(m)
			if err != nil {
				return Records{}, err
			}
			r.Runs[kvToken(root.Kind+"."+filepath.Base(filepath.Dir(m)))] = data
		}
	}
	data, err := os.ReadFile(p.AttemptsJSON())
	if err != nil && !os.IsNotExist(err) {
		return Records{}, err
	}
	if len(data) > 0 {
		var all map[string]json.RawMessage
		if err := json.Unmarshal(data, &all); err != nil {
			return Records{}, err
		}
		for id, v := range all {
			r.Attempts[kvToken(id)] = v
		}
	}
	return r, nil
}

// kvToken keeps a key to the characters a KV key allows.
func kvToken(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.', r == '=':
			return r
		}
		return '_'
	}, s)
}
