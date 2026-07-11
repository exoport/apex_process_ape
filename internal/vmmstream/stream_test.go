package vmmstream

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/natstest"
	"github.com/nats-io/nats.go"
)

// TestStreamFramingAndFlowControl drives the Sender→Receiver pair over a loopback
// NATS server with a credit window far smaller than the payload's frame count,
// so the sender MUST block on credit and resume as the receiver drains — proving
// chunking, in-order reassembly, and end-to-end credit flow control (PLAN-18 D2).
func TestStreamFramingAndFlowControl(t *testing.T) {
	url := natstest.RunServer(t)
	ncSend, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect sender: %v", err)
	}
	defer ncSend.Close()
	ncRecv, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect receiver: %v", err)
	}
	defer ncRecv.Close()

	const node, sid = "node1", "s1"
	dataSubj := SessionSubject(node, sid, ChannelStdout)
	ctrlSubj := SessionSubject(node, sid, ChannelControl)

	// Credit=2 frames; payload spans 5+ frames → the sender blocks repeatedly.
	const credit = 2
	recv, err := NewReceiver(ncRecv, dataSubj, ctrlSubj, ChannelStdout, credit)
	if err != nil {
		t.Fatalf("receiver: %v", err)
	}
	defer func() { _ = recv.Close() }()
	send, err := NewSender(ncSend, dataSubj, ctrlSubj, ChannelStdout, credit)
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	_ = ncRecv.Flush()
	_ = ncSend.Flush()

	payload := bytes.Repeat([]byte("abcdefgh"), (MaxFrameData*5+123)/8) // >5 frames

	done := make(chan error, 1)
	go func() {
		if _, werr := send.Write(context.Background(), payload); werr != nil {
			done <- werr
			return
		}
		done <- send.CloseSend()
	}()

	got, err := io.ReadAll(recv)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: got %d bytes, want %d", len(got), len(payload))
	}
	select {
	case werr := <-done:
		if werr != nil {
			t.Fatalf("send: %v", werr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sender did not finish (flow-control deadlock?)")
	}
}
