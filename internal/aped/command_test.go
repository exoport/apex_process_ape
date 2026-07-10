package aped

import (
	"errors"
	"testing"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
)

func TestCommandCodecRoundTrip(t *testing.T) {
	cmd := Command{
		Op: OpCreate,
		Create: &CreateCommand{
			Caller: "alice",
			Spec: sandbox.WorkspaceSpec{
				Name:        testWS,
				Image:       testImage,
				VMM:         sandbox.VMMQemu,
				Mount:       sandbox.MountHostFS,
				ProjectRoot: "/home/alice/proj",
				Comp:        &sandbox.Composition{StagingDir: "/staging", GuestHome: "/home/ape", Env: []string{"HOME=/home/ape"}},
			},
		},
	}
	b, err := encodeCommand(cmd)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := decodeCommand(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Op != OpCreate || got.Create == nil {
		t.Fatalf("round-trip lost the create payload: %+v", got)
	}
	if got.Create.Spec.Name != testWS || got.Create.Spec.Image != testImage || got.Create.Caller != "alice" {
		t.Errorf("spec round-trip mismatch: %+v", got.Create)
	}
	if got.Create.Spec.Comp == nil || got.Create.Spec.Comp.GuestHome != "/home/ape" {
		t.Errorf("composition lost in round-trip: %+v", got.Create.Spec.Comp)
	}
}

func TestResponseCodeMapping(t *testing.T) {
	// A sentinel error → its wire code, and asError re-derives a sentinel that
	// classifies with errors.Is (so the front returns errors identical to a
	// local Backend).
	cases := []struct {
		err  error
		code string
	}{
		{workspace.ErrNotFound, workspace.CodeNotFound},
		{workspace.ErrUnsupported, workspace.CodeUnsupported},
		{workspace.ErrBusy, workspace.CodeBusy},
		{workspace.ErrPolicyDenied, workspace.CodeDenied},
		{workspace.ErrDeviceUnavailable, workspace.CodeDeviceUnavailable},
		{workspace.ErrValidation, workspace.CodeValidation},
	}
	for _, tc := range cases {
		resp := errorResponse(tc.err)
		if resp.Code != tc.code {
			t.Errorf("errorResponse(%v).Code = %q, want %q", tc.err, resp.Code, tc.code)
		}
		back := resp.asError()
		if !errors.Is(back, tc.err) {
			t.Errorf("asError for %q did not classify as %v (got %v)", tc.code, tc.err, back)
		}
	}

	// An unclassified error maps to VALIDATION (the catch-all).
	if resp := errorResponse(errors.New("nerdctl exploded")); resp.Code != workspace.CodeValidation {
		t.Errorf("unclassified error code = %q, want VALIDATION", resp.Code)
	}

	// A nil-code response is a success (no error).
	if (Response{}).asError() != nil {
		t.Error("empty response should not be an error")
	}
}
