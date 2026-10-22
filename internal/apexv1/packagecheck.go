package apexv1

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	modzip "golang.org/x/mod/zip"
)

// The release gate's package-contents check (W7, Q160): `ape project
// package-check`, which apex-release runs before it releases a library.
// It packs the library as its users get it and fails (library.package-leak)
// on any file under an APEX folder. Go: the module zip at HEAD, from the
// root module or, with no go.mod at the root, each module in a subfolder
// (the docs' option A), listed with golang.org/x/mod/zip from the files git
// holds — with no .gitattributes applied, as cmd/go turns export-ignore
// off. npm, Python and Cargo: their own tools' packed lists. A package it
// cannot pack — the tool absent or failing, no commit, no manifest — is the
// notice library.package-unchecked, never a silent pass.

// leaksShown bounds the paths a leak finding names; LeaksTotal counts all.
const leaksShown = 20

// packageToolTimeout bounds one packaging tool's run.
const packageToolTimeout = 10 * time.Minute

// The ecosystems package-check packs.
const (
	EcosystemGo     = "go"
	EcosystemNPM    = "npm"
	EcosystemPython = "python"
	EcosystemCargo  = "cargo"
)

// PackageReport is one package's check.
type PackageReport struct {
	Ecosystem string `json:"ecosystem"`
	// Dir is the package's folder, "." for the root.
	Dir string `json:"dir"`
	// Manifest is the file that declares the package (go.mod,
	// package.json, pyproject.toml or setup.py, Cargo.toml).
	Manifest string `json:"manifest"`
	// Tool is what listed the packed files.
	Tool    string `json:"tool"`
	Checked bool   `json:"checked"`
	// Files counts the packed files; Leaks are the first leaksShown under
	// an APEX folder, LeaksTotal all of them.
	Files      int      `json:"files"`
	Leaks      []string `json:"leaks"`
	LeaksTotal int      `json:"leaks_total"`
	// Unchecked says why it was not checked.
	Unchecked string `json:"unchecked,omitempty"`
	// ZipError is why the Go module zip could not be built as it is
	// (golang.org/x/mod/zip's own check); the files it would hold are
	// still checked.
	ZipError string `json:"zip_error,omitempty"`
}

// PackageCheckResult is `ape project package-check`'s report.
type PackageCheckResult struct {
	// Library is _apex/config.yaml's library: true; without it nothing is
	// packed and the check passes.
	Library bool `json:"library"`
	// Head is the commit the Go module zip and the Python build were made
	// from.
	Head     string          `json:"head,omitempty"`
	Packages []PackageReport `json:"packages"`
	Notices  []Diagnostic    `json:"notices"`
}

// PackageCheck is `ape project package-check`: every package of a
// library: true project packed and checked for APEX folders. A leak is
// library.package-leak (exit 1, a finding per package); what could not be
// packed is a notice. It writes nothing in the project.
func (p *Project) PackageCheck(ctx context.Context) (*PackageCheckResult, error) {
	res := &PackageCheckResult{Library: p.IsLibrary(), Packages: []PackageReport{}, Notices: []Diagnostic{}}
	if !res.Library {
		return res, nil
	}
	res.Head = HeadSHA(ctx, p.Root)
	res.Packages = append(res.Packages, p.goPackages(ctx, res.Head)...)
	if fileExists(filepath.Join(p.Root, "package.json")) {
		res.Packages = append(res.Packages, p.npmPackage(ctx))
	}
	if m := p.pythonManifest(); m != "" {
		res.Packages = append(res.Packages, p.pythonPackage(ctx, res.Head, m))
	}
	if fileExists(filepath.Join(p.Root, "Cargo.toml")) {
		res.Packages = append(res.Packages, p.cargoPackage(ctx))
	}
	if len(res.Packages) == 0 {
		res.Notices = append(res.Notices, notice(Diagnostic{Code: CodePackageUnchecked, File: configYAML, Message: "library: true, and no package ape knows how to pack: " +
			"no go.mod (at the root or in a subfolder), package.json, pyproject.toml, setup.py or Cargo.toml — check what the release ships by hand"}))
		return res, nil
	}
	var found []Finding
	for i := range res.Packages {
		r := &res.Packages[i]
		if !r.Checked {
			res.Notices = append(res.Notices, notice(Diagnostic{Code: CodePackageUnchecked, File: r.Manifest, Message: r.Ecosystem + " package " + r.Dir +
				" was not checked for APEX folders: " + r.Unchecked + " — " + uncheckedFix(r.Ecosystem)}))
			continue
		}
		if r.LeaksTotal > 0 {
			found = append(found, leakFinding(r))
		}
	}
	if len(found) > 0 {
		return res, &FailError{Code: CodePackageLeak, Msg: strconv.Itoa(len(found)) + " package(s) ship APEX folders — keep them out before the release (the findings say how)", Findings: found}
	}
	return res, nil
}

// uncheckedFix is the remedy of a package-unchecked notice.
func uncheckedFix(eco string) string {
	switch eco {
	case EcosystemNPM:
		return "install npm and run `ape project package-check` again, or check `npm pack --dry-run` by hand"
	case EcosystemPython:
		return "install python3 and its build module (python3 -m pip install build) and run `ape project package-check` again, or check the sdist and wheel by hand"
	case EcosystemCargo:
		return "install cargo and run `ape project package-check` again, or check `cargo package --list` by hand"
	}
	return "commit, then run `ape project package-check` again"
}

// leakFinding is a package's leak as a finding.
func leakFinding(r *PackageReport) Finding {
	what := map[string]string{
		EcosystemGo:     "the Go module zip (" + r.Dir + ") at HEAD",
		EcosystemNPM:    "npm pack (" + r.Manifest + ")",
		EcosystemPython: "the Python sdist or wheel (" + r.Manifest + ")",
		EcosystemCargo:  "cargo package (" + r.Manifest + ")",
	}[r.Ecosystem]
	fix := map[string]string{
		EcosystemGo: "run `ape project fence`, which writes and commits a go.mod fence in each APEX folder, so the zip leaves them out" +
			" (or move the module into a subfolder; see the publish-a-library how-to)",
		EcosystemNPM:    "leave the APEX folders out of package.json's files (or add them to .npmignore)",
		EcosystemPython: "exclude the APEX folders from the sdist and wheel (MANIFEST.in prune, or the build backend's exclude settings)",
		EcosystemCargo:  "add the APEX folders to Cargo.toml's exclude (or narrow its include)",
	}[r.Ecosystem]
	list := strings.Join(r.Leaks, ", ")
	if r.LeaksTotal > len(r.Leaks) {
		list += " and " + strconv.Itoa(r.LeaksTotal-len(r.Leaks)) + " more"
	}
	return Finding{File: r.Manifest, Message: what + " holds " + strconv.Itoa(r.LeaksTotal) + " file(s) under APEX folders: " + list + " — " + fix}
}

// judge fills a report's counts and leaks from the packed paths, each
// relative to the repository root and slash-separated.
func (p *Project) judge(r *PackageReport, paths []string) {
	sort.Strings(paths)
	r.Checked, r.Files, r.Leaks = true, len(paths), []string{}
	for _, rel := range paths {
		if p.underApexFolder(rel) == "" {
			continue
		}
		r.LeaksTotal++
		if len(r.Leaks) < leaksShown {
			r.Leaks = append(r.Leaks, rel)
		}
	}
}

// gitEntry is one file of a commit's tree (git ls-tree -l).
type gitEntry struct {
	mode, sha, path string
	size            int64
}

// headEntries are the files of rev's tree, submodules (gitlinks) left out
// as git archive leaves their content out.
func headEntries(ctx context.Context, root, rev string) ([]gitEntry, error) {
	out, err := gitRaw(ctx, root, "ls-tree", "-r", "-z", "-l", "--full-tree", rev)
	if err != nil {
		return nil, err
	}
	var es []gitEntry
	for rec := range strings.SplitSeq(out, "\x00") {
		meta, p, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) != 4 || f[1] != "blob" {
			continue
		}
		size, _ := strconv.ParseInt(f[3], 10, 64)
		es = append(es, gitEntry{mode: f[0], sha: f[2], path: p, size: size})
	}
	return es, nil
}

// gitZipFile is a file of a commit as golang.org/x/mod/zip reads one.
type gitZipFile struct {
	ctx  context.Context //nolint:containedctx // read once, within the PackageCheck call that made it
	root string
	e    gitEntry
	name string
}

func (f gitZipFile) Path() string                { return f.name }
func (f gitZipFile) Lstat() (os.FileInfo, error) { return gitFileInfo(f), nil }
func (f gitZipFile) Open() (io.ReadCloser, error) {
	out, err := gitRaw(f.ctx, f.root, "cat-file", "blob", f.e.sha)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader(out)), nil
}

type gitFileInfo gitZipFile

func (i gitFileInfo) Name() string { return path.Base(i.name) }
func (i gitFileInfo) Size() int64  { return i.e.size }
func (i gitFileInfo) Mode() fs.FileMode {
	switch i.e.mode {
	case "120000":
		return fs.ModeSymlink | fs.ModePerm
	case "100755":
		return 0o755
	}
	return 0o644
}
func (i gitFileInfo) ModTime() time.Time { return time.Time{} }
func (i gitFileInfo) IsDir() bool        { return false }
func (i gitFileInfo) Sys() any           { return nil }

// GoModuleZipFiles are the files the module zip of the Go module in
// subdir ("" for the root) holds at rev, as paths relative to the module:
// golang.org/x/mod/zip's own selection (nested modules, vendor rules,
// symlinks and irregular files left out; a subfolder module without a
// LICENSE gets the root's, as cmd/go adds it) from the files git holds at
// rev, with no .gitattributes applied — cmd/go writes
// `* -export-subst -export-ignore` into the repository's attributes
// before it archives, so export-ignore never shortens a module zip
// (measured: the W7 experiment's v0.2.0). zipErr is the zip's own
// validity problem, if any (a file too large, a bad name).
func GoModuleZipFiles(ctx context.Context, root, rev, subdir string) (files []string, zipErr string, err error) {
	es, err := headEntries(ctx, root, rev)
	if err != nil {
		return nil, "", err
	}
	return goModuleZipFiles(ctx, root, es, subdir)
}

func goModuleZipFiles(ctx context.Context, root string, es []gitEntry, subdir string) (files []string, zipErr string, err error) {
	var in []modzip.File
	haveLicense := false
	for _, e := range es {
		name := e.path
		if subdir != "" {
			rest, ok := strings.CutPrefix(e.path, subdir+"/")
			if !ok {
				continue
			}
			name = rest
		}
		if name == "LICENSE" {
			haveLicense = true
		}
		in = append(in, gitZipFile{ctx: ctx, root: root, e: e, name: name})
	}
	if subdir != "" && !haveLicense {
		for _, e := range es {
			if e.path == "LICENSE" {
				in = append(in, gitZipFile{ctx: ctx, root: root, e: e, name: "LICENSE"})
			}
		}
	}
	cf, cfErr := modzip.CheckFiles(in)
	if cfErr != nil {
		zipErr = strings.ReplaceAll(cfErr.Error(), "\n", "; ")
	}
	return cf.Valid, zipErr, nil
}

// goModuleDirs are the Go modules package-check packs: the root's, or,
// with no go.mod at the root, each module in a subfolder (option A) that
// sits in no other one — outside the APEX folders and the folders the go
// command skips.
func (p *Project) goModuleDirs(es []gitEntry) []string {
	var dirs []string
	for _, e := range es {
		if e.path == "go.mod" {
			return []string{""}
		}
		dir, ok := strings.CutSuffix(e.path, "/go.mod")
		if !ok || p.underApexFolder(e.path) != "" {
			continue
		}
		skip := false
		for el := range strings.SplitSeq(dir, "/") {
			if strings.HasPrefix(el, ".") || strings.HasPrefix(el, "_") || el == "testdata" || el == "vendor" {
				skip = true
			}
		}
		if !skip {
			dirs = append(dirs, dir)
		}
	}
	sort.Strings(dirs)
	var out []string
	for _, d := range dirs {
		if len(out) > 0 && strings.HasPrefix(d, out[len(out)-1]+"/") {
			continue
		}
		out = append(out, d)
	}
	return out
}

// goPackages checks every Go module's zip at HEAD. A go.mod in the
// working tree with no commit to pack is unchecked.
func (p *Project) goPackages(ctx context.Context, head string) []PackageReport {
	const tool = "golang.org/x/mod/zip at HEAD"
	if head == "" {
		if fileExists(filepath.Join(p.Root, "go.mod")) {
			return []PackageReport{{Ecosystem: EcosystemGo, Dir: ".", Manifest: "go.mod", Tool: tool, Leaks: []string{}, Unchecked: "HEAD has no commit, and the module zip is built from git"}}
		}
		return nil
	}
	es, err := headEntries(ctx, p.Root, head)
	if err != nil {
		return []PackageReport{{Ecosystem: EcosystemGo, Dir: ".", Manifest: "go.mod", Tool: tool, Leaks: []string{}, Unchecked: "git ls-tree failed: " + err.Error()}}
	}
	var out []PackageReport
	for _, dir := range p.goModuleDirs(es) {
		r := PackageReport{Ecosystem: EcosystemGo, Dir: ".", Manifest: "go.mod", Tool: tool, Leaks: []string{}}
		if dir != "" {
			r.Dir, r.Manifest = dir, dir+"/go.mod"
		}
		files, zipErr, err := goModuleZipFiles(ctx, p.Root, es, dir)
		if err != nil {
			r.Unchecked = err.Error()
			out = append(out, r)
			continue
		}
		r.ZipError = zipErr
		rels := make([]string, len(files))
		for i, f := range files {
			rels[i] = path.Join(dir, f)
		}
		p.judge(&r, rels)
		out = append(out, r)
	}
	return out
}

// runTool runs a packaging tool in dir with packageToolTimeout: its
// stdout, or an error carrying the last line of its stderr.
func runTool(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, packageToolTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		last := ""
		for l := range strings.SplitSeq(strings.TrimSpace(errb.String()), "\n") {
			if strings.TrimSpace(l) != "" {
				last = strings.TrimSpace(l)
			}
		}
		if last != "" {
			return nil, errors.New(name + " " + strings.Join(args, " ") + " failed: " + last)
		}
		return nil, errors.New(name + " " + strings.Join(args, " ") + " failed: " + err.Error())
	}
	return out.Bytes(), nil
}

// npmPackage is npm pack's file list, from the working tree. Scripts are
// not run (--ignore-scripts): a check builds nothing.
func (p *Project) npmPackage(ctx context.Context) PackageReport {
	args := []string{"pack", "--dry-run", "--json", "--ignore-scripts"}
	r := PackageReport{Ecosystem: EcosystemNPM, Dir: ".", Manifest: "package.json", Tool: "npm " + strings.Join(args, " "), Leaks: []string{}}
	if _, err := exec.LookPath("npm"); err != nil {
		r.Unchecked = "npm is not on PATH"
		return r
	}
	out, err := runTool(ctx, p.Root, "npm", args...)
	if err != nil {
		r.Unchecked = err.Error()
		return r
	}
	var packs []struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if i := bytes.IndexByte(out, '['); i >= 0 {
		out = out[i:]
	}
	if err := json.Unmarshal(out, &packs); err != nil || len(packs) == 0 {
		r.Unchecked = "npm pack --json printed no file list"
		return r
	}
	var paths []string
	for _, f := range packs[0].Files {
		paths = append(paths, path.Clean(filepath.ToSlash(f.Path)))
	}
	p.judge(&r, paths)
	return r
}

// cargoPackage is cargo package --list, from the working tree.
func (p *Project) cargoPackage(ctx context.Context) PackageReport {
	args := []string{"package", "--list", "--allow-dirty"}
	r := PackageReport{Ecosystem: EcosystemCargo, Dir: ".", Manifest: "Cargo.toml", Tool: "cargo " + strings.Join(args, " "), Leaks: []string{}}
	if _, err := exec.LookPath("cargo"); err != nil {
		r.Unchecked = "cargo is not on PATH"
		return r
	}
	out, err := runTool(ctx, p.Root, "cargo", args...)
	if err != nil {
		r.Unchecked = err.Error()
		return r
	}
	var paths []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			paths = append(paths, path.Clean(filepath.ToSlash(l)))
		}
	}
	p.judge(&r, paths)
	return r
}

// pythonManifest is the Python package's manifest at the root, "" for
// none.
func (p *Project) pythonManifest() string {
	for _, m := range []string{"pyproject.toml", "setup.py"} {
		if fileExists(filepath.Join(p.Root, m)) {
			return m
		}
	}
	return ""
}

// pythonPackage builds the sdist and the wheel (python3 -m build
// --no-isolation, so nothing is downloaded) from a copy of HEAD, as a
// build writes into the tree it builds (egg-info, build/), and lists both.
func (p *Project) pythonPackage(ctx context.Context, head, manifest string) PackageReport {
	r := PackageReport{Ecosystem: EcosystemPython, Dir: ".", Manifest: manifest, Leaks: []string{}}
	py := "python3"
	if _, err := exec.LookPath(py); err != nil {
		py = "python"
	}
	r.Tool = py + " -m build --no-isolation (a copy of HEAD)"
	if _, err := exec.LookPath(py); err != nil {
		r.Unchecked = "python3 is not on PATH"
		return r
	}
	if head == "" {
		r.Unchecked = "HEAD has no commit, and the build runs on a copy of it"
		return r
	}
	tmp, err := os.MkdirTemp("", "ape-package-check-*")
	if err != nil {
		r.Unchecked = err.Error()
		return r
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	src, dist := filepath.Join(tmp, "src"), filepath.Join(tmp, "dist")
	if err := exportHead(ctx, p.Root, head, src); err != nil {
		r.Unchecked = "copying HEAD: " + err.Error()
		return r
	}
	if _, err := runTool(ctx, src, py, "-m", "build", "--no-isolation", "--outdir", dist, src); err != nil {
		r.Unchecked = err.Error()
		return r
	}
	paths, err := distFiles(dist)
	if err != nil {
		r.Unchecked = err.Error()
		return r
	}
	if len(paths) == 0 {
		r.Unchecked = py + " -m build wrote no sdist or wheel"
		return r
	}
	p.judge(&r, paths)
	return r
}

// exportHead writes rev's tree into dst (git archive, as tar).
func exportHead(ctx context.Context, root, rev, dst string) error {
	c := exec.CommandContext(ctx, "git", "archive", "--format=tar", rev)
	c.Dir = root
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return errors.New(strings.TrimSpace(errb.String()))
	}
	tr := tar.NewReader(&out)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.FromSlash(path.Clean(h.Name))
		if !filepath.IsLocal(name) {
			continue
		}
		abs := filepath.Join(dst, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(abs, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return err
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				return err
			}
			mode := fs.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			if err := os.WriteFile(abs, data, mode); err != nil {
				return err
			}
		}
	}
}

// distFiles lists the files of every sdist (.tar.gz, its top folder
// stripped) and wheel (.whl) in dist, relative to the package, deduped.
func distFiles(dist string) ([]string, error) {
	seen := map[string]bool{}
	ents, err := os.ReadDir(dist)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		abs := filepath.Join(dist, e.Name())
		switch {
		case strings.HasSuffix(e.Name(), ".tar.gz"):
			f, err := os.Open(abs)
			if err != nil {
				return nil, err
			}
			gz, err := gzip.NewReader(f)
			if err != nil {
				_ = f.Close()
				return nil, err
			}
			tr := tar.NewReader(gz)
			for {
				h, err := tr.Next()
				if err != nil {
					break
				}
				if h.Typeflag == tar.TypeReg {
					if _, rest, ok := strings.Cut(path.Clean(h.Name), "/"); ok {
						seen[rest] = true
					}
				}
			}
			_ = f.Close()
		case strings.HasSuffix(e.Name(), ".whl"):
			zr, err := zip.OpenReader(abs)
			if err != nil {
				return nil, err
			}
			for _, zf := range zr.File {
				if !strings.HasSuffix(zf.Name, "/") {
					seen[path.Clean(zf.Name)] = true
				}
			}
			_ = zr.Close()
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out, nil
}
