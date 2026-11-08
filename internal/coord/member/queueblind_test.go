package member

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/ghapp"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// FINDING-V15-1B-PRE-2 (correcting FINDING-V15-1A-7 c): on the mirror the
// App's read token was minted WITH Merge queues: read and GitHub's GraphQL
// still refused mergeQueue.configuration; REST rules/branches/main, which
// needs only Metadata: read, answered with the merge_queue rule. So
// project check reads main's rulesets first:
//
//   - the mirror's shape (a ruleset carries the queue, GraphQL refuses):
//     judged, with or without the service's Missing — no warning;
//   - a ruleset whose queue squashes: judged, a finding;
//   - the control, a queue no ruleset carries and GraphQL refusing: NOT
//     judged, loudly, with a route that names no permission (the one it
//     named is already granted and does not help).
func TestQueueFindings_RulesetsFirst(t *testing.T) {
	for _, tc := range []struct {
		name    string
		missing []string
		rule    string // the merge_queue rule's method on main; "" none
		finding bool
		loud    bool
	}{
		{"mirror-shape", nil, "MERGE", false, false},
		{"mirror-shape-service-says-missing", []string{"merge_queues: read"}, "MERGE", false, false},
		{"ruleset-squashes", nil, "SQUASH", true, false},
		{"control-no-rule-graphql-refuses", nil, "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/repos/shop/notes/rules/branches/main" {
					rules := []map[string]any{{"type": "non_fast_forward"}}
					if tc.rule != "" {
						rules = append(rules, map[string]any{"type": "merge_queue", "parameters": map[string]any{"merge_method": tc.rule}})
					}
					_ = json.NewEncoder(w).Encode(rules)
					return
				}
				_, _ = w.Write([]byte(`{"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by integration"}]}`))
			}))
			t.Cleanup(gh.Close)
			url := natstest.RunServer(t)
			nc, err := nats.Connect(url)
			require.NoError(t, err)
			t.Cleanup(nc.Close)
			_, err = nc.Subscribe(ghapp.TokenSubject+".*", func(m *nats.Msg) {
				data, _ := json.Marshal(ghapp.Reply{OK: true, Token: "ghs_read", Missing: tc.missing})
				_ = m.Respond(data)
			})
			require.NoError(t, err)
			require.NoError(t, nc.Flush())
			cfg := strings.Replace(queuedBlock, "nats://127.0.0.1:4222", url, 1) + "  github:\n    repo: shop/notes\n    api: " + gh.URL + "\n    app: true\n"
			p := project(t, cfg, false)
			creds, _ := natstest.MintStandaloneCreds(t, "dana")
			data, err := os.ReadFile(creds)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Join(p.Root, "hot"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(p.Root, "hot", "ape-nats.creds"), data, 0o600))

			var note bytes.Buffer
			fs := QueueFindings(context.Background(), p, &note)
			if tc.finding {
				require.Len(t, fs, 1)
				require.Contains(t, fs[0].Message, "merges by squash")
			} else {
				require.Empty(t, fs)
			}
			if !tc.loud {
				require.Empty(t, note.String(), "judged: nothing to say")
				return
			}
			require.Contains(t, note.String(), "warning: main's merge-queue settings on shop/notes are NOT judged")
			require.Contains(t, note.String(), "Resource not accessible by integration")
			require.Contains(t, note.String(), QueueBlindRoute)
			require.NotContains(t, note.String(), "Merge queues: Read-only", "it does not send a person to grant a permission already granted")
		})
	}
}
