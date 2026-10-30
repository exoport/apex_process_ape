package lease

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"github.com/exoport/apex_process_ape/internal/coord/layout"
)

// Entry is the last token an ape saw for one lease it held (§4.4 step 4):
// "held" while it holds it, "released" once it released or handed it over.
// After a rebuild the ape re-claims with it; without one it pauses.
type Entry struct {
	Token  layout.Token `json:"token"`
	Holder string       `json:"holder"`
	State  string       `json:"state"` // held | released
	At     time.Time    `json:"at"`
}

// Entry states.
const (
	Held     = "held"
	Released = "released"
)

// Book is an ape's local record of its lease tokens, one JSON file in its
// own state (the checkout's _apex/state/coord/tokens.json; a supervisor's
// or coordinator's own state dir). It is the ape's, never NATS's: it is
// what a rebuild replays from.
type Book struct {
	path string
	mu   sync.Mutex
}

// OpenBook is the book at path (created on first write).
func OpenBook(path string) *Book { return &Book{path: path} }

// Record writes key's entry, replacing the previous one.
func (b *Book) Record(key string, l Lease, state string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	all, err := b.read()
	if err != nil {
		return err
	}
	all[key] = Entry{Token: l.Token, Holder: l.Holder, State: state, At: time.Now().UTC()}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(b.path), 0o755); err != nil {
		return fmt.Errorf("lease: book dir: %w", err)
	}
	return atomicfile.Write(b.path, append(data, '\n'))
}

// Last is key's entry, ok false when the ape never held it.
func (b *Book) Last(key string) (Entry, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	all, err := b.read()
	if err != nil {
		return Entry{}, false, err
	}
	e, ok := all[key]
	return e, ok, nil
}

func (b *Book) read() (map[string]Entry, error) {
	all := map[string]Entry{}
	data, err := os.ReadFile(b.path)
	if errors.Is(err, fs.ErrNotExist) {
		return all, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lease: read book: %w", err)
	}
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("lease: book %s: %w", b.path, err)
	}
	return all, nil
}
