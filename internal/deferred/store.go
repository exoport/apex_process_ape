package deferred

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/frontmatter"
	"gopkg.in/yaml.v3"
)

// ClosedDirName holds tombstones. Closed records stay on disk forever:
// an LLM that cannot see a closed defer re-files it, and deletion is what
// destroyed the audit trail the first time.
const ClosedDirName = "closed"

// IndexFileName is the derived listing. It is gitignored and rebuilt on
// read — never a source of truth, so a stale one cannot mislead anyone.
const IndexFileName = "index.yaml"

// Store is the record directory.
type Store struct {
	Dir string
}

// New opens a store at dir. Nothing is created until a write.
func New(dir string) *Store { return &Store{Dir: dir} }

// ClosedDir is where tombstones live.
func (s *Store) ClosedDir() string { return filepath.Join(s.Dir, ClosedDirName) }

// LoadResult is what a read returns: the records that parsed, and one
// warning per record that did not.
//
// This split IS the degradation contract. One malformed record loses
// exactly that record; the single-file predecessor returned zero records
// when handed one bad entry.
type LoadResult struct {
	Records  []Record `json:"records"            yaml:"records"`
	Warnings []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

// LoadOptions selects what to read.
type LoadOptions struct {
	// IncludeClosed reads the closed/ subdirectory too.
	IncludeClosed bool
}

// Load reads every record in the store.
func (s *Store) Load(opts LoadOptions) (*LoadResult, error) {
	res := &LoadResult{}
	if err := s.loadDir(s.Dir, res); err != nil {
		return nil, err
	}
	if opts.IncludeClosed {
		if err := s.loadDir(s.ClosedDir(), res); err != nil {
			return nil, err
		}
	}
	sort.Slice(res.Records, func(i, j int) bool { return res.Records[i].ID < res.Records[j].ID })
	sort.Strings(res.Warnings)
	return res, nil
}

func (s *Store) loadDir(dir string, res *LoadResult) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // an absent store is an empty store
		}
		return fmt.Errorf("read %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		// The two files in a store that are prose about the store rather
		// than records in it.
		if strings.HasPrefix(e.Name(), ".") ||
			strings.EqualFold(e.Name(), "README.md") ||
			strings.EqualFold(e.Name(), PreambleFileName) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		rec, err := ReadRecord(path)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		res.Records = append(res.Records, rec)
	}
	return nil
}

// ReadRecord parses one record file.
func ReadRecord(path string) (Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	fm, body, err := frontmatter.Split(data)
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if err := yaml.Unmarshal(fm, &rec); err != nil {
		return Record{}, fmt.Errorf("frontmatter is not valid YAML: %w", err)
	}
	if rec.ID == "" {
		return Record{}, errors.New("frontmatter has no id")
	}
	rec.Body = string(body)
	rec.Path = path
	if rec.Status == "" {
		rec.Status = StatusOpen
	}
	return rec, nil
}

// Render serialises a record: frontmatter, then the body byte-identical.
func Render(rec Record) ([]byte, error) {
	var buf strings.Builder
	buf.WriteString("---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(rec); err != nil {
		return nil, fmt.Errorf("encode record %s: %w", rec.ID, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode record %s: %w", rec.ID, err)
	}
	buf.WriteString("---\n")
	// The body goes down verbatim. 108 of 109 real bodies contain
	// backticks and the em-dash tail; anything that reformatted them would
	// break the losslessness assertion the migration depends on.
	buf.WriteString(rec.Body)
	return []byte(buf.String()), nil
}

// Write writes a record into the store, returning its path. An existing
// file for the same id is replaced, which is what makes ingest idempotent
// for an identical bullet on the same day.
func (s *Store) Write(rec Record) (string, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", s.Dir, err)
	}
	data, err := Render(rec)
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.Dir, rec.FileName())
	if err := writeFileAtomic(path, data); err != nil {
		return "", err
	}
	return path, nil
}

// ErrNotFound reports an id the store does not hold.
var ErrNotFound = errors.New("no such deferred record")

// Filter selects records for `list`.
type Filter struct {
	// Status is open|closed|discarded|all. Empty means open, and `closed`
	// matches discarded records too — both have left the working set. The
	// vocabulary is spelled out here because it used to say `open|closed|all`
	// while the store modelled three statuses, and a caller reading the
	// short version concluded discard was not supported and wrote the wrong
	// one.
	Status string
	Owner  string
	Story  string
	// Path matches any anchor's path prefix.
	Path  string
	Group string
}

// writeFileAtomic writes via a temp file and a rename, so a crash cannot
// leave a half-written record that the next read would report as
// malformed.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".ape-"+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create temp beside %s: %w", path, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return fmt.Errorf("chmod %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("rename onto %s: %w", path, err)
	}
	return nil
}

// RebuildIndex writes the derived index.yaml. Gitignored and rebuilt on
// read, so it is a convenience for humans and never consulted as truth.
func (s *Store) RebuildIndex() error {
	res, err := s.Load(LoadOptions{IncludeClosed: true})
	if err != nil {
		return err
	}
	type entry struct {
		ID     string `yaml:"id"`
		Title  string `yaml:"title"`
		Status string `yaml:"status"`
		Owner  string `yaml:"owner,omitempty"`
		Story  string `yaml:"source_story,omitempty"`
		File   string `yaml:"file"`
	}
	doc := struct {
		Note    string  `yaml:"note"`
		Records []entry `yaml:"records"`
	}{
		Note: "Derived from the record files. Gitignored and rebuilt on read — never a source of truth.",
	}
	for i := range res.Records {
		rec := &res.Records[i]
		rel, relErr := filepath.Rel(s.Dir, rec.Path)
		if relErr != nil {
			rel = filepath.Base(rec.Path)
		}
		doc.Records = append(doc.Records, entry{
			ID: rec.ID, Title: rec.Title, Status: rec.Status,
			Owner: rec.Owner, Story: rec.SourceStory,
			File: filepath.ToSlash(rel),
		})
	}
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encode index: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode index: %w", err)
	}
	return writeFileAtomic(filepath.Join(s.Dir, IndexFileName), []byte(buf.String()))
}
