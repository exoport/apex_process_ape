package vmmstream

import (
	"context"
	"io"

	"github.com/nats-io/nats.go"
)

// Sender publishes a byte stream on one data channel, chunked to ≤MaxFrameData
// and gated by a CreditWindow the peer refills over the credit (control) channel
// (PLAN-18 D2 flow control). It self-manages the credit subscription.
type Sender struct {
	nc        *nats.Conn
	dataSubj  string
	window    *CreditWindow
	creditSub *nats.Subscription
}

// NewSender wires a Sender publishing on dataSubj, refilling its credit from
// creditSubj, starting with initialCredit frames.
func NewSender(nc *nats.Conn, dataSubj, creditSubj string, initialCredit int) (*Sender, error) {
	s := &Sender{nc: nc, dataSubj: dataSubj, window: NewCreditWindow(initialCredit)}
	sub, err := nc.Subscribe(creditSubj, func(m *nats.Msg) {
		if f, err := DecodeControl(m.Data); err == nil && f.Type == ControlCredit {
			s.window.Grant(f.Credit)
		}
	})
	if err != nil {
		return nil, err
	}
	s.creditSub = sub
	return s, nil
}

// Write chunks p and publishes each frame after acquiring one credit. It blocks
// until every frame is sent or ctx is done, and returns the bytes sent.
func (s *Sender) Write(ctx context.Context, p []byte) (int, error) {
	sent := 0
	for _, chunk := range Chunks(p) {
		if _, err := s.window.Acquire(ctx, 1); err != nil {
			return sent, err
		}
		if err := s.nc.Publish(s.dataSubj, chunk); err != nil {
			return sent, err
		}
		sent += len(chunk)
	}
	return sent, s.nc.Flush()
}

// CloseSend signals end-of-stream with a zero-length data frame (ordered after
// all prior frames on the same subject) and stops refilling credit. It does not
// gate the EOF on credit — the sentinel is a control signal, not payload.
func (s *Sender) CloseSend() error {
	s.window.Close()
	if s.creditSub != nil {
		_ = s.creditSub.Unsubscribe()
	}
	if err := s.nc.Publish(s.dataSubj, nil); err != nil {
		return err
	}
	return s.nc.Flush()
}

// Receiver subscribes a data channel, delivers frames in order via Read, and
// grants the peer one credit per frame CONSUMED over the credit channel. Because
// credit is granted only on consumption, frames in flight never exceed the
// buffer, so the async callback's buffered hand-off never blocks the NATS
// dispatch goroutine (the slow-consumer trap). A zero-length frame is EOF.
type Receiver struct {
	nc         *nats.Conn
	creditSubj string
	frames     chan []byte
	sub        *nats.Subscription
	closed     chan struct{}
	pending    []byte
	eof        bool
}

// NewReceiver subscribes dataSubj (buffering up to credit frames) and grants
// credit back on creditSubj as frames are read.
func NewReceiver(nc *nats.Conn, dataSubj, creditSubj string, credit int) (*Receiver, error) {
	if credit < 1 {
		credit = 1
	}
	r := &Receiver{
		nc:         nc,
		creditSubj: creditSubj,
		frames:     make(chan []byte, credit),
		closed:     make(chan struct{}),
	}
	sub, err := nc.Subscribe(dataSubj, func(m *nats.Msg) {
		// Copy: NATS reuses m.Data after the callback returns.
		data := append([]byte(nil), m.Data...)
		select {
		case r.frames <- data:
		case <-r.closed:
		}
	})
	if err != nil {
		return nil, err
	}
	r.sub = sub
	return r, nil
}

// Read reassembles the frame stream. It returns io.EOF at the zero-length
// sentinel (or after Close), and grants one credit per fully-received frame.
func (r *Receiver) Read(p []byte) (int, error) {
	if len(r.pending) == 0 {
		if r.eof {
			return 0, io.EOF
		}
		select {
		case frame := <-r.frames:
			if len(frame) == 0 { // EOF sentinel
				r.eof = true
				return 0, io.EOF
			}
			r.pending = frame
			_ = r.grant(1) // one frame consumed → refill the peer
		case <-r.closed:
			return 0, io.EOF
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

// grant publishes a credit refill on the control channel.
func (r *Receiver) grant(n int) error {
	b, err := ControlFrame{Type: ControlCredit, Credit: n}.Encode()
	if err != nil {
		return err
	}
	return r.nc.Publish(r.creditSubj, b)
}

// Close stops the subscription and unblocks a pending Read.
func (r *Receiver) Close() error {
	select {
	case <-r.closed:
	default:
		close(r.closed)
	}
	if r.sub != nil {
		return r.sub.Unsubscribe()
	}
	return nil
}
