package natsconn

import (
	"bufio"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// connectProxyStub is a minimal HTTP CONNECT proxy: it reads the request line
// and headers, answers 200 (or refuse), and splices to target. It stands in for
// the real per-workspace proxy so the dialer is tested against the protocol
// rather than against our own implementation of it.
func connectProxyStub(t *testing.T, target string, refuse bool) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					line, rerr := br.ReadString('\n')
					if rerr != nil {
						return
					}
					if strings.TrimSpace(line) == "" {
						break
					}
				}
				if refuse {
					_, _ = io.WriteString(c, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
					return
				}
				up, derr := net.Dial("tcp", target)
				if derr != nil {
					_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
					return
				}
				defer up.Close()
				_, _ = io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
				go func() { _, _ = io.Copy(up, br) }()
				_, _ = io.Copy(c, up)
			}()
		}
	}()
	return ln.Addr().String()
}

// echoTarget is a stand-in for the front's loopback NATS listener.
func echoTarget(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return ln.Addr().String()
}

func TestProxyDialerTunnelsRawBytes(t *testing.T) {
	proxy := connectProxyStub(t, echoTarget(t), false)
	d := &ProxyDialer{ProxyAddr: proxy, Timeout: 5 * time.Second}

	conn, err := d.Dial("tcp", "aped.internal:4222")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// What NATS does next: raw protocol bytes, no HTTP framing.
	if _, err := conn.Write([]byte("CONNECT {}\r\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 12)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "CONNECT {}\r\n" {
		t.Errorf("tunnelled bytes = %q, want them delivered verbatim", buf)
	}
}

func TestProxyDialerReportsARefusal(t *testing.T) {
	proxy := connectProxyStub(t, echoTarget(t), true)
	d := &ProxyDialer{ProxyAddr: proxy}
	_, err := d.Dial("tcp", "evil.example.com:4222")
	if err == nil {
		t.Fatal("a refused CONNECT returned a usable conn")
	}
	// The message has to say the PROXY refused, not that the dial failed: those
	// have different fixes and only one of them is the allowlist.
	if !strings.Contains(err.Error(), "refused CONNECT") || !strings.Contains(err.Error(), "403") {
		t.Errorf("error does not identify a proxy refusal: %v", err)
	}
}

func TestProxyDialerNeedsAProxy(t *testing.T) {
	d := &ProxyDialer{}
	if _, err := d.Dial("tcp", "aped.internal:4222"); err == nil {
		t.Error("dialing with no proxy configured succeeded")
	}
}

func TestProxyDialerRefusesNonTCP(t *testing.T) {
	d := &ProxyDialer{ProxyAddr: "127.0.0.1:3128"}
	if _, err := d.Dial("udp", "aped.internal:4222"); err == nil {
		t.Error("a udp dial was accepted; a CONNECT tunnel is TCP by definition")
	}
}

func TestProxyDialerUnreachableProxy(t *testing.T) {
	// Port 1 on loopback: reliably refused, so this exercises the dial failure
	// path without waiting on a timeout.
	d := &ProxyDialer{ProxyAddr: "127.0.0.1:1", Timeout: time.Second}
	if _, err := d.Dial("tcp", "aped.internal:4222"); err == nil {
		t.Error("dialing an unreachable proxy succeeded")
	}
}

func TestProxyAddrFromEnvPrefersHTTPS(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://169.254.42.1:3999")
	t.Setenv("HTTPS_PROXY", "http://169.254.42.1:3128")
	if got := ProxyAddrFromEnv(); got != "169.254.42.1:3128" {
		t.Errorf("ProxyAddrFromEnv() = %q, want the HTTPS proxy", got)
	}
}

func TestProxyAddrFromEnvUnset(t *testing.T) {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		t.Setenv(k, "")
	}
	if got := ProxyAddrFromEnv(); got != "" {
		t.Errorf("ProxyAddrFromEnv() = %q with no proxy set, want empty", got)
	}
}

func TestProxyAddrNormalizes(t *testing.T) {
	for in, want := range map[string]string{
		"http://169.254.42.1:3128": "169.254.42.1:3128",
		"169.254.42.1:3128":        "169.254.42.1:3128",
		"http://proxy.local":       "proxy.local:80",
		"":                         "",
		"::::":                     "",
	} {
		if got := proxyAddr(in); got != want {
			t.Errorf("proxyAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
