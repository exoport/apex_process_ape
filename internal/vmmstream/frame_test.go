package vmmstream

import (
	"bytes"
	"testing"
)

func TestSessionSubject(t *testing.T) {
	got := SessionSubject("node1", "s7", ChannelStdout)
	if want := "ape.vmm.node1.exec.s7.stdout"; got != want {
		t.Fatalf("SessionSubject = %q, want %q", got, want)
	}
}

func TestControlFrameRoundTrip(t *testing.T) {
	in := ControlFrame{Type: ControlResize, Cols: 120, Rows: 40}
	b, err := in.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := DecodeControl(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != in {
		t.Fatalf("round-trip = %+v, want %+v", out, in)
	}
}

func TestChunks(t *testing.T) {
	if c := Chunks(nil); c != nil {
		t.Errorf("Chunks(nil) = %v, want nil", c)
	}
	if c := Chunks([]byte("hi")); len(c) != 1 || string(c[0]) != "hi" {
		t.Errorf("Chunks(small) = %v, want one 'hi' chunk", c)
	}
	// Exactly one frame.
	exact := bytes.Repeat([]byte("a"), MaxFrameData)
	if c := Chunks(exact); len(c) != 1 || len(c[0]) != MaxFrameData {
		t.Errorf("Chunks(exact) split into %d chunks, want 1", len(c))
	}
	// One byte over → two frames (MaxFrameData + 1).
	over := bytes.Repeat([]byte("b"), MaxFrameData+1)
	c := Chunks(over)
	if len(c) != 2 || len(c[0]) != MaxFrameData || len(c[1]) != 1 {
		t.Fatalf("Chunks(over) = %d chunks (%d,%d…), want 2 (%d,1)", len(c), len(c[0]), lenOr(c, 1), MaxFrameData)
	}
	// Reassembly preserves bytes.
	var reassembled []byte
	for _, chunk := range c {
		reassembled = append(reassembled, chunk...)
	}
	if !bytes.Equal(reassembled, over) {
		t.Error("Chunks lost bytes on reassembly")
	}
}

func lenOr(c [][]byte, i int) int {
	if i < len(c) {
		return len(c[i])
	}
	return -1
}
