package broker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// startBroker spins up a broker on 127.0.0.1:0 and returns the base
// URL, the broker handle, and a cancel func that shuts it down.
func startBroker(t *testing.T, opts Options) (string, *Broker, context.CancelFunc) {
	t.Helper()
	b := New(opts)
	addr, err := b.Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := b.Serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
			// Test goroutine — log via t.Log only if test is still running.
			_ = err
		}
	}()
	base := "http://" + addr
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/")
		if err == nil {
			resp.Body.Close()
			return base, b, cancel
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatal("broker never became reachable")
	return "", nil, cancel
}

func TestBroker_RejectsNon127001Bind(t *testing.T) {
	b := New(Options{ListenAddr: "0.0.0.0:0"})
	if _, err := b.Listen(); err == nil {
		t.Fatal("expected refuse-to-bind error")
	}
}

func TestBroker_SSEDelivers(t *testing.T) {
	base, b, cancel := startBroker(t, Options{})
	defer cancel()

	resp, body := openSSE(t, base+"/api/events")
	defer resp.Body.Close()
	waitFor(t, "subscribe", func() bool { return b.ClientCount() == 1 })

	b.Publish(Event{Name: "stage-start", Data: `<div>hello</div>`})
	got := readSSEEvent(t, body)
	if got.event != "stage-start" || got.data != `<div>hello</div>` {
		t.Errorf("got %+v", got)
	}
}

// TestBroker_SSEFlushOnEveryEvent locks the load-bearing PoC fix.
// Without flusher.Flush() after every Fprintf, slow producers leave
// events buffered until the OS chunk fills, freezing the UI for >30 s
// gaps. The regression guard: write one event, demand it appears at
// the client within 500 ms. If a future refactor drops the flush, the
// HTTP write buffer holds it until close and the test times out.
func TestBroker_SSEFlushOnEveryEvent(t *testing.T) {
	base, b, cancel := startBroker(t, Options{})
	defer cancel()

	resp, body := openSSE(t, base+"/api/events")
	defer resp.Body.Close()
	waitFor(t, "subscribe", func() bool { return b.ClientCount() == 1 })

	start := time.Now()
	b.Publish(Event{Name: "ping", Data: "ok"})
	got := readSSEEvent(t, body)
	elapsed := time.Since(start)
	if got.event != "ping" {
		t.Fatalf("event = %q, want ping", got.event)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("SSE delivery took %s — flusher.Flush() is likely missing from the publish path", elapsed)
	}
}

func TestBroker_ReplayOnConnect(t *testing.T) {
	calls := atomic.Int32{}
	base, _, cancel := startBroker(t, Options{
		ReplayEvents: func() []Event {
			calls.Add(1)
			return []Event{
				{Name: "pipeline-init", Data: "<scaffold/>"},
				{Name: "await-pending", Data: "<input enabled/>"},
			}
		},
	})
	defer cancel()
	resp, body := openSSE(t, base+"/api/events")
	defer resp.Body.Close()

	first := readSSEEvent(t, body)
	if first.event != "pipeline-init" {
		t.Errorf("first event = %q, want pipeline-init", first.event)
	}
	second := readSSEEvent(t, body)
	if second.event != "await-pending" {
		t.Errorf("second event = %q, want await-pending", second.event)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("ReplayEvents called %d times, want 1", got)
	}
}

func TestBroker_PostSend(t *testing.T) {
	got := make(chan string, 1)
	base, _, cancel := startBroker(t, Options{
		OnSend: func(content string) { got <- content },
	})
	defer cancel()

	body := strings.NewReader(`{"content":"hi"}`)
	resp, err := http.Post(base+"/api/send", "application/json", body)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
	select {
	case content := <-got:
		if content != "hi" {
			t.Errorf("OnSend got %q, want hi", content)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("OnSend never called")
	}
}

func TestBroker_PostSend_503WhenNoCallback(t *testing.T) {
	base, _, cancel := startBroker(t, Options{})
	defer cancel()
	resp, err := http.Post(base+"/api/send", "application/json", strings.NewReader(`{"content":"x"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

func TestBroker_PostStop(t *testing.T) {
	called := atomic.Int32{}
	base, _, cancel := startBroker(t, Options{
		OnStop: func() { called.Add(1) },
	})
	defer cancel()
	resp, err := http.Post(base+"/api/stop", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want 202", resp.StatusCode)
	}
	if called.Load() != 1 {
		t.Errorf("OnStop called %d times, want 1", called.Load())
	}
}

func TestBroker_PostStop_409WhenNoCallback(t *testing.T) {
	base, _, cancel := startBroker(t, Options{})
	defer cancel()
	resp, err := http.Post(base+"/api/stop", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", resp.StatusCode)
	}
}

func TestBroker_PostSend_64KBLimit(t *testing.T) {
	base, _, cancel := startBroker(t, Options{OnSend: func(string) {}})
	defer cancel()
	big := make([]byte, 80*1024)
	for i := range big {
		big[i] = 'a'
	}
	body, _ := json.Marshal(map[string]string{"content": string(big)})
	resp, err := http.Post(base+"/api/send", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		t.Error("80 KB body should have been rejected by MaxBytesReader")
	}
}

// --- SSE helpers ---

type sseEvent struct {
	event string
	data  string
}

func openSSE(t *testing.T, url string) (*http.Response, *bufio.Reader) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE status = %d", resp.StatusCode)
	}
	return resp, bufio.NewReader(resp.Body)
}

func readSSEEvent(t *testing.T, r *bufio.Reader) sseEvent {
	t.Helper()
	var ev sseEvent
	var dataLines []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				t.Fatal("SSE stream closed before event delivered")
			}
			t.Fatalf("read sse: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			ev.data = strings.Join(dataLines, "\n")
			return ev
		}
		if strings.HasPrefix(line, "event: ") {
			ev.event = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
		}
	}
}

func waitFor(t *testing.T, name string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waitFor %s timed out", name)
}
