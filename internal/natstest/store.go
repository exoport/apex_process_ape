package natstest

import (
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

// storeDir is a JetStream store directory removed with the test. Create it
// before starting the server that uses it, and register that server's stop
// after it: cleanups run last first, so the server has closed the store
// before the removal. The removal is tried again for a while (V8-WIN-4):
// on Windows a store file can stay in use a moment after the server
// closed it ("being used by another process"), and t.TempDir's single
// attempt failed a test whose body had passed. Each try runs the garbage
// collector first, so a file a stopped server dropped is closed by its
// finalizer. A node stopped and restarted can keep a raft file open for
// good (CI on 2228c8e..fbbb659: still held after 10 s); on Windows that is
// logged, not failed: the embedded server's handle in this process, in the
// OS temp dir, says nothing of ape. Anywhere else a removal failure fails.
func storeDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "natstest-") //nolint:usetesting // t.TempDir removes once; this one tries again (V8-WIN-4)
	if err != nil {
		t.Fatalf("natstest: store dir: %v", err)
	}
	t.Cleanup(func() {
		err := removeAll(dir, func(d string) error { runtime.GC(); return os.RemoveAll(d) }, 10*time.Second)
		switch {
		case err == nil:
		case runtime.GOOS == "windows":
			t.Logf("natstest: store dir left, a file still in use by the embedded server: %v", err)
		default:
			t.Errorf("natstest: remove store dir: %v", err)
		}
	})
	return dir
}

// removeAll runs rm on dir until it succeeds or within has passed.
func removeAll(dir string, rm func(string) error, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		err := rm(dir)
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// stop shuts s down and waits until it is done, its store closed.
func stop(s *server.Server) {
	s.Shutdown()
	s.WaitForShutdown()
}
