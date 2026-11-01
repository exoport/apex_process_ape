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

// FINDING-V15-1A-7 (c): main's queue configuration needs Merge queues:
// read. When the token lacks it — the App service says so (Missing), or an
// older service's token is refused by GitHub — project check does not
// judge the settings, and says so loudly with the route. The control: a
// token with it judges them (a MERGE queue, no finding, no warning).
func TestQueueFindings_BlindReadIsLoud(t *testing.T) {
	for _, tc := range []struct {
		name    string
		missing []string
		refuse  bool // GitHub refuses the query, as it did on the mirror
		loud    bool
	}{
		{"service-says-missing", []string{"merge_queues: read"}, false, true},
		{"github-refuses", nil, true, true},
		{"control", nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.refuse {
					_, _ = w.Write([]byte(`{"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by integration"}]}`))
					return
				}
				_, _ = w.Write([]byte(`{"data":{"repository":{"mergeQueue":{"configuration":{"mergeMethod":"MERGE"}}}}}`))
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
			cfg := strings.Replace(block, "nats://127.0.0.1:4222", url, 1) + "  github:\n    repo: shop/notes\n    api: " + gh.URL + "\n    app: true\n"
			p := project(t, cfg, false)
			creds, _ := natstest.MintStandaloneCreds(t, "dana")
			data, err := os.ReadFile(creds)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Join(p.Root, "hot"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(p.Root, "hot", "ape-nats.creds"), data, 0o600))

			var note bytes.Buffer
			fs := QueueFindings(context.Background(), p, &note)
			require.Empty(t, fs)
			if !tc.loud {
				require.Empty(t, note.String(), "judged: nothing to say")
				return
			}
			require.Contains(t, note.String(), "warning: main's merge-queue settings on shop/notes are NOT judged: the App's token lacks Merge queues: read")
			require.Contains(t, note.String(), QueueBlindRoute)
		})
	}
}
