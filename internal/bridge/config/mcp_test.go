package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildMCPConfig_HappyPath(t *testing.T) {
	raw, err := BuildMCPConfig(MCPOptions{APEBin: "/usr/local/bin/ape", IPCPort: 47291})
	if err != nil {
		t.Fatalf("BuildMCPConfig: %v", err)
	}

	var got struct {
		MCPServers map[string]MCPServer `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.MCPServers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(got.MCPServers))
	}
	srv, ok := got.MCPServers["mcp-bridge"]
	if !ok {
		t.Fatalf("missing mcp-bridge server: %s", string(raw))
	}
	if srv.Command != "/usr/local/bin/ape" {
		t.Errorf("command = %q, want /usr/local/bin/ape", srv.Command)
	}
	if len(srv.Args) != 1 || srv.Args[0] != "mcp-bridge" {
		t.Errorf("args = %v, want [mcp-bridge]", srv.Args)
	}
	if srv.Env["APE_IPC_PORT"] != "47291" {
		t.Errorf("APE_IPC_PORT = %q, want 47291", srv.Env["APE_IPC_PORT"])
	}
}

func TestBuildMCPConfig_BlobSizeUnderArgLimit(t *testing.T) {
	// MAX_ARG_STRLEN on Linux is 128 KB. PLAN-5 / C2 asserts inline
	// blobs are <1 KB. Lock that expectation; if it ever fails, the
	// hidden-MCP-server policy needs a different delivery path.
	raw, err := BuildMCPConfig(MCPOptions{APEBin: "/usr/local/bin/ape", IPCPort: 47291})
	if err != nil {
		t.Fatalf("BuildMCPConfig: %v", err)
	}
	if len(raw) > 1024 {
		t.Errorf("MCP config blob is %d bytes, expected <1024", len(raw))
	}
}

func TestBuildMCPConfig_Errors(t *testing.T) {
	cases := []struct {
		name    string
		opts    MCPOptions
		wantSub string
	}{
		{"empty APEBin", MCPOptions{IPCPort: 47291}, "APEBin is empty"},
		{"zero port", MCPOptions{APEBin: "/x/ape", IPCPort: 0}, "IPCPort"},
		{"negative port", MCPOptions{APEBin: "/x/ape", IPCPort: -1}, "IPCPort"},
		{"over port range", MCPOptions{APEBin: "/x/ape", IPCPort: 70000}, "IPCPort"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildMCPConfig(tc.opts)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestBuildMCPConfig_ExtraServersCannotOverrideBridge(t *testing.T) {
	_, err := BuildMCPConfig(MCPOptions{
		APEBin:       "/x/ape",
		IPCPort:      1234,
		ExtraServers: map[string]MCPServer{"mcp-bridge": {Command: "evil"}},
	})
	if err == nil {
		t.Fatal("expected override-protection error, got nil")
	}
	if !strings.Contains(err.Error(), "cannot override") {
		t.Errorf("error = %q, want substring 'cannot override'", err.Error())
	}
}

func TestBuildMCPConfig_ExtraServersMerged(t *testing.T) {
	raw, err := BuildMCPConfig(MCPOptions{
		APEBin:  "/x/ape",
		IPCPort: 1234,
		ExtraServers: map[string]MCPServer{
			"playwright": {Command: "/usr/bin/npx", Args: []string{"@playwright/mcp"}},
		},
	})
	if err != nil {
		t.Fatalf("BuildMCPConfig: %v", err)
	}
	var got struct {
		MCPServers map[string]MCPServer `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got.MCPServers["mcp-bridge"]; !ok {
		t.Error("mcp-bridge dropped after merge")
	}
	if _, ok := got.MCPServers["playwright"]; !ok {
		t.Error("playwright not merged")
	}
}
