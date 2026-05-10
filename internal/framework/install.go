package framework

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ErrBootstrapCancelled is returned by a Bootstrapper when the user
// chose not to seed config.yaml. Update treats it as "skip the seed,
// proceed with the install".
var ErrBootstrapCancelled = errors.New("config bootstrap cancelled by user")

// BootstrapValues are the user-supplied (or flag-supplied) inputs
// that drive _apex/config.yaml seeding on first install.
type BootstrapValues struct {
	ProjectName string
	Extensions  []string // canonical IDs; e.g., {"ext-adrs", "ext-features"}
}

// Bootstrapper resolves BootstrapValues for a fresh project. The
// production implementation is a Bubble Tea TUI; tests pass static
// stubs. NoopBootstrapper is used when --no-bootstrap is set.
type Bootstrapper interface {
	Bootstrap(ctx context.Context, defaultProjectName string, extensions []Extension) (BootstrapValues, error)
}

// NoopBootstrapper always cancels — used for --no-bootstrap.
type NoopBootstrapper struct{}

// Bootstrap implements Bootstrapper.
func (NoopBootstrapper) Bootstrap(_ context.Context, _ string, _ []Extension) (BootstrapValues, error) {
	return BootstrapValues{}, ErrBootstrapCancelled
}

// StaticBootstrapper returns predetermined values without prompting.
// Used when --project-name / --extensions flags are provided.
type StaticBootstrapper struct {
	Values BootstrapValues
}

// Bootstrap implements Bootstrapper.
func (s StaticBootstrapper) Bootstrap(_ context.Context, _ string, _ []Extension) (BootstrapValues, error) {
	return s.Values, nil
}

// UpdateOptions parameterize an Update call.
type UpdateOptions struct {
	// FrameworkRepo is the absolute path to a checked-out
	// apex_process_framework repo.
	FrameworkRepo string
	// ProjectRoot is the absolute path to the project the install
	// targets.
	ProjectRoot string
	// NoFetch skips the `git fetch && merge --ff-only` step.
	NoFetch bool
	// Force bypasses the framework-clean / project-skills-modified
	// safety checks. Use sparingly.
	Force bool
	// ApeVersion is the version string of the binary performing the
	// install. Recorded into framework.yaml's `ape.version`.
	ApeVersion string
	// Bootstrapper resolves config-bootstrap values when
	// _apex/config.yaml is absent. Required.
	Bootstrapper Bootstrapper
	// Now is injectable for deterministic tests; defaults to
	// time.Now().UTC().
	Now func() time.Time
}

// UpdateResult is the structured payload an Update call returns.
type UpdateResult struct {
	Metadata Metadata
	Summary  UpdateSummary
}

// UpdateSummary is a tally of what changed in the project tree.
type UpdateSummary struct {
	SkillsInstalled    int      `json:"skillsInstalled"              yaml:"skillsInstalled"`
	SkillsRemoved      int      `json:"skillsRemoved"                yaml:"skillsRemoved"`
	SkillsRemovedPaths []string `json:"skillsRemovedPaths,omitempty" yaml:"skillsRemovedPaths,omitempty"`
	PipelinesInstalled int      `json:"pipelinesInstalled"           yaml:"pipelinesInstalled"`
	ConfigSeeded       bool     `json:"configSeeded"                 yaml:"configSeeded"`
	ConfigLocalSeeded  bool     `json:"configLocalSeeded"            yaml:"configLocalSeeded"`
}

// ValidationError signals the framework repo is in a state that
// blocks the install (dirty, wrong branch, missing subtree, etc.).
// Cobra layer maps this to a structured error envelope.
type ValidationError struct {
	Code   string // e.g. "framework_dirty", "framework_not_main"
	Detail string
}

// Error implements error.
func (e *ValidationError) Error() string {
	return e.Code + ": " + e.Detail
}

// ProjectSkillsModifiedError signals the project has uncommitted edits
// to .claude/skills/apex-* — refusing to clobber them without --force.
type ProjectSkillsModifiedError struct {
	Paths []string
}

// Error implements error.
func (e *ProjectSkillsModifiedError) Error() string {
	return "uncommitted changes under .claude/skills/apex-* (pass --force to override): " + strings.Join(e.Paths, ", ")
}

// AlreadyInstalledError signals that <projectRoot>/_apex/framework.yaml
// already exists when `ape framework setup` was invoked. Pass --force
// to re-bootstrap (which resets project_name + extensions).
type AlreadyInstalledError struct {
	Path string
}

func (e *AlreadyInstalledError) Error() string {
	return fmt.Sprintf(
		"framework already installed at %s — run \"ape framework update\" to refresh, "+
			"or \"ape framework setup --force\" to re-bootstrap "+
			"(resets project_name and extensions)",
		e.Path,
	)
}

// repoInfoFromFramework collects the framework repo metadata used to
// fill the framework.yaml `framework:` block.
type repoInfo struct {
	branch  string
	headSHA string
	tag     string
	origin  string
}

// Setup runs the initial-install flow: bootstrap config.yaml (if
// absent), copy skills + pipelines, write framework.yaml. Refuses to
// run if framework.yaml already exists unless opts.Force is set.
//
// Per PLAN-1 / I3, this is the command users run on a fresh project.
// Subsequent refreshes use Update, which deliberately omits the
// bootstrap step so config.yaml stays untouched.
func Setup(ctx context.Context, opts *UpdateOptions) (*UpdateResult, error) {
	if opts.ProjectRoot == "" {
		return nil, errors.New("UpdateOptions.ProjectRoot is required")
	}
	if opts.Bootstrapper == nil {
		return nil, errors.New("UpdateOptions.Bootstrapper is required for setup")
	}
	metaPath := MetadataPath(opts.ProjectRoot)
	if _, err := os.Stat(metaPath); err == nil && !opts.Force {
		return nil, &AlreadyInstalledError{Path: metaPath}
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("stat %s: %w", metaPath, err)
	}
	return installCore(ctx, opts, true /* doBootstrap */)
}

// Update runs the refresh flow: re-copy skills + pipelines, refresh
// framework.yaml. Does NOT touch config.yaml — that's a one-time
// bootstrap recorded by Setup. Refuses to run if framework.yaml is
// absent (no install to refresh).
//
// Per PLAN-1 / I3, this is the command users run on every framework
// version bump after the initial Setup. The Bootstrapper field of
// opts is ignored.
func Update(ctx context.Context, opts *UpdateOptions) (*UpdateResult, error) {
	if opts.ProjectRoot == "" {
		return nil, errors.New("UpdateOptions.ProjectRoot is required")
	}
	// Existence check up-front so users get the actionable not-installed
	// hint rather than a downstream copy / write failure.
	if _, err := ReadMetadata(opts.ProjectRoot); err != nil {
		return nil, err
	}
	return installCore(ctx, opts, false /* doBootstrap */)
}

// installCore is the shared core of Setup and Update. doBootstrap
// controls whether config.yaml seeding runs; everything else is the
// same.
func installCore(ctx context.Context, opts *UpdateOptions, doBootstrap bool) (*UpdateResult, error) {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if err := EnsureGitAvailable(); err != nil {
		return nil, err
	}
	if opts.FrameworkRepo == "" {
		return nil, &ValidationError{Code: "framework_repo_unset", Detail: "framework repo path is empty (set --repo or $APEX_FRAMEWORK_REPO)"}
	}
	if err := validateFrameworkRepo(ctx, opts); err != nil {
		return nil, err
	}
	info, err := readFrameworkInfo(ctx, opts.FrameworkRepo)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(opts.ProjectRoot, "_apex"), 0o755); err != nil {
		return nil, fmt.Errorf("create _apex/: %w", err)
	}
	if err := checkProjectSkills(ctx, opts); err != nil {
		return nil, err
	}
	var (
		bootstrap         BootstrapValues
		configSeeded      bool
		configLocalSeeded bool
	)
	if doBootstrap {
		bootstrap, configSeeded, configLocalSeeded, err = bootstrapConfig(ctx, opts)
		if err != nil {
			return nil, err
		}
	}
	skillsDir := filepath.Join(opts.ProjectRoot, ProjectSkillsDir)
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", skillsDir, err)
	}
	removed, err := wipeStaleSkills(skillsDir)
	if err != nil {
		return nil, err
	}
	installedSkills, err := copySkills(opts.FrameworkRepo, skillsDir)
	if err != nil {
		return nil, err
	}
	pipelinesDir := filepath.Join(opts.ProjectRoot, ProjectPipelinesDir)
	if err := os.MkdirAll(pipelinesDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", pipelinesDir, err)
	}
	installedPipelines, err := copyPipelines(opts.FrameworkRepo, pipelinesDir)
	if err != nil {
		return nil, err
	}
	// For Update (doBootstrap=false), preserve the existing ConfigSource
	// values rather than overwriting with zeros. This keeps the
	// project_name + extensions recorded by the original Setup.
	cfgSource := ConfigSource{
		Seeded:      configSeeded,
		ProjectName: bootstrap.ProjectName,
		Extensions:  bootstrap.Extensions,
	}
	cfgLocalSource := ConfigLocalExampleSource{Seeded: configLocalSeeded}
	if !doBootstrap {
		if prior, prErr := ReadMetadata(opts.ProjectRoot); prErr == nil {
			cfgSource = prior.Sources.Config
			cfgLocalSource = prior.Sources.ConfigLocalExample
		}
	}
	meta := Metadata{
		ConfigSchemaVersion: MetadataSchemaVersion,
		InstalledAt:         now(),
		Framework: RepoInfo{
			RepoOrigin: info.origin,
			VersionTag: info.tag,
			GitHash:    info.headSHA,
			GitBranch:  info.branch,
		},
		Ape: ApeInfo{Version: opts.ApeVersion},
		Sources: Sources{
			Skills:             SkillsSource{Count: len(installedSkills), Paths: installedSkills},
			Pipelines:          PipelinesSource{Count: len(installedPipelines), Paths: installedPipelines},
			Config:             cfgSource,
			ConfigLocalExample: cfgLocalSource,
		},
	}
	if err := WriteMetadata(opts.ProjectRoot, &meta); err != nil {
		return nil, err
	}
	return &UpdateResult{
		Metadata: meta,
		Summary: UpdateSummary{
			SkillsInstalled:    len(installedSkills),
			SkillsRemoved:      len(removed),
			SkillsRemovedPaths: removed,
			PipelinesInstalled: len(installedPipelines),
			ConfigSeeded:       configSeeded,
			ConfigLocalSeeded:  configLocalSeeded,
		},
	}, nil
}

// validateFrameworkRepo runs the framework-side preconditions: subtree
// layout, git repo, branch + clean check, optional fetch.
func validateFrameworkRepo(ctx context.Context, opts *UpdateOptions) error {
	if err := validateFrameworkLayout(opts.FrameworkRepo); err != nil {
		return err
	}
	if !IsGitRepo(ctx, opts.FrameworkRepo) {
		return &ValidationError{Code: "framework_not_git_repo", Detail: opts.FrameworkRepo + " is not a git repository"}
	}
	branch, err := CurrentBranch(ctx, opts.FrameworkRepo)
	if err != nil {
		return err
	}
	if !opts.Force && branch != "main" {
		return &ValidationError{Code: "framework_not_main", Detail: fmt.Sprintf("framework repo is on branch %q (expected main; pass --force to bypass)", branch)}
	}
	clean, err := IsClean(ctx, opts.FrameworkRepo)
	if err != nil {
		return err
	}
	if !opts.Force && !clean {
		return &ValidationError{Code: "framework_dirty", Detail: "framework repo has uncommitted changes (pass --force to bypass)"}
	}
	if !opts.NoFetch {
		if err := FetchAndFastForward(ctx, opts.FrameworkRepo, "main"); err != nil {
			return &ValidationError{Code: "framework_fetch_diverged", Detail: err.Error()}
		}
	}
	return nil
}

// readFrameworkInfo gathers the framework repo's HEAD-relative metadata
// after the repo has passed validation.
func readFrameworkInfo(ctx context.Context, repo string) (repoInfo, error) {
	info := repoInfo{}
	branch, err := CurrentBranch(ctx, repo)
	if err != nil {
		return info, err
	}
	info.branch = branch
	headSHA, err := HeadSHA(ctx, repo)
	if err != nil {
		return info, err
	}
	info.headSHA = headSHA
	tag, err := ExactTag(ctx, repo)
	if err != nil {
		return info, err
	}
	info.tag = tag
	// origin missing is non-fatal — record empty.
	if origin, err := RemoteOrigin(ctx, repo); err == nil {
		info.origin = origin
	}
	return info, nil
}

// checkProjectSkills runs the project-side skill-deletion safety
// check: when the project is a git repo, refuse without --force if
// any tracked apex-* skill has uncommitted edits. Untracked apex-*
// paths are treated as safe-to-clobber leftovers.
func checkProjectSkills(ctx context.Context, opts *UpdateOptions) error {
	if opts.Force || !IsGitRepo(ctx, opts.ProjectRoot) {
		return nil
	}
	entries, err := SkillsPorcelain(ctx, opts.ProjectRoot)
	if err != nil {
		return err
	}
	modified := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsUntracked() {
			modified = append(modified, e.Path)
		}
	}
	if len(modified) > 0 {
		sort.Strings(modified)
		return &ProjectSkillsModifiedError{Paths: modified}
	}
	return nil
}

// bootstrapConfig runs the config bootstrap when _apex/config.yaml is
// absent (or when opts.Force is set — Setup --force re-bootstraps,
// overwriting the existing config.yaml so project_name + extensions
// can be reset). Returns the values used (empty when skipped), seed
// flags for both config files, and any non-cancellation error.
func bootstrapConfig(ctx context.Context, opts *UpdateOptions) (values BootstrapValues, configSeeded, configLocalSeeded bool, err error) {
	configPath := filepath.Join(opts.ProjectRoot, ProjectConfig)
	if _, err := os.Stat(configPath); err == nil {
		if !opts.Force {
			return BootstrapValues{}, false, false, nil
		}
		// Force=true with config present: fall through to re-bootstrap.
	} else if !errors.Is(err, fs.ErrNotExist) {
		return BootstrapValues{}, false, false, fmt.Errorf("stat %s: %w", configPath, err)
	}
	defaultName := DefaultProjectName(opts.ProjectRoot)
	bv, bErr := opts.Bootstrapper.Bootstrap(ctx, defaultName, Extensions)
	if errors.Is(bErr, ErrBootstrapCancelled) {
		return BootstrapValues{}, false, false, nil
	}
	if bErr != nil {
		return BootstrapValues{}, false, false, fmt.Errorf("bootstrap: %w", bErr)
	}
	if err := writeConfigFromTemplate(opts.FrameworkRepo, configPath, bv); err != nil {
		return BootstrapValues{}, false, false, fmt.Errorf("seed config.yaml: %w", err)
	}
	localPath := filepath.Join(opts.ProjectRoot, ProjectConfigLocalExample)
	if _, err := os.Stat(localPath); errors.Is(err, fs.ErrNotExist) {
		src := filepath.Join(opts.FrameworkRepo, SubtreeConfigLocalExample)
		if _, err := os.Stat(src); err == nil {
			if err := CopyFile(src, localPath); err != nil {
				return BootstrapValues{}, false, false, fmt.Errorf("seed config.local.example.yaml: %w", err)
			}
			configLocalSeeded = true
		}
	}
	return bv, true, configLocalSeeded, nil
}

func validateFrameworkLayout(repoPath string) error {
	for _, sub := range []string{SubtreeSkills, SubtreePipelines} {
		full := filepath.Join(repoPath, sub)
		info, err := os.Stat(full)
		if err != nil || !info.IsDir() {
			return &ValidationError{Code: "framework_layout_invalid", Detail: "missing or non-directory: " + full}
		}
	}
	return nil
}

// wipeStaleSkills removes every apex-* entry under skillsDir,
// returning the relative paths (under the project root) of what was
// removed. Used so that skills deleted upstream disappear locally too.
func wipeStaleSkills(skillsDir string) ([]string, error) {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", skillsDir, err)
	}
	removed := make([]string, 0, len(entries))
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), SkillPrefix) {
			continue
		}
		full := filepath.Join(skillsDir, e.Name())
		if err := os.RemoveAll(full); err != nil {
			return nil, fmt.Errorf("remove %s: %w", full, err)
		}
		removed = append(removed, filepath.Join(ProjectSkillsDir, e.Name()))
	}
	sort.Strings(removed)
	return removed, nil
}

// copySkills copies every apex-* directory under
// <repo>/framework/_claude/skills/ into skillsDir. Returns the
// relative paths (under the project root) of the installed entries.
func copySkills(frameworkRepo, skillsDir string) ([]string, error) {
	srcRoot := filepath.Join(frameworkRepo, SubtreeSkills)
	entries, err := os.ReadDir(srcRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", srcRoot, err)
	}
	installed := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), SkillPrefix) {
			continue
		}
		src := filepath.Join(srcRoot, e.Name())
		dst := filepath.Join(skillsDir, e.Name())
		if _, err := CopyTree(src, dst); err != nil {
			return nil, fmt.Errorf("copy skill %s: %w", e.Name(), err)
		}
		installed = append(installed, filepath.Join(ProjectSkillsDir, e.Name()))
	}
	sort.Strings(installed)
	return installed, nil
}

// copyPipelines copies every *.yaml under
// <repo>/framework/_apex/pipelines/ into pipelinesDir. Returns the
// relative paths of the installed pipelines.
func copyPipelines(frameworkRepo, pipelinesDir string) ([]string, error) {
	srcRoot := filepath.Join(frameworkRepo, SubtreePipelines)
	entries, err := os.ReadDir(srcRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", srcRoot, err)
	}
	installed := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		src := filepath.Join(srcRoot, e.Name())
		dst := filepath.Join(pipelinesDir, e.Name())
		if err := CopyFile(src, dst); err != nil {
			return nil, fmt.Errorf("copy pipeline %s: %w", e.Name(), err)
		}
		installed = append(installed, filepath.Join(ProjectPipelinesDir, e.Name()))
	}
	sort.Strings(installed)
	return installed, nil
}

// writeConfigFromTemplate reads the framework's config.yaml template,
// overrides project_name + extensions with the bootstrap values, and
// writes the result to dst (atomic). Other fields are left at their
// template values; the user can edit them by hand.
func writeConfigFromTemplate(frameworkRepo, dst string, bv BootstrapValues) error {
	src := filepath.Join(frameworkRepo, SubtreeConfig)
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read template %s: %w", src, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse template %s: %w", src, err)
	}
	if err := overrideConfigFields(&doc, bv); err != nil {
		return err
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return AtomicWriteFile(dst, out, 0o644)
}

// overrideConfigFields walks the YAML document and mutates the
// project_name and extensions values in place. Preserves declaration
// order and any other fields the template carries.
func overrideConfigFields(doc *yaml.Node, bv BootstrapValues) error {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return errors.New("config template: not a YAML document")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return errors.New("config template: root is not a mapping")
	}
	for i := 0; i < len(root.Content); i += 2 {
		key := root.Content[i]
		val := root.Content[i+1]
		switch key.Value {
		case "project_name":
			val.Tag = "!!str"
			val.Value = bv.ProjectName
			val.Kind = yaml.ScalarNode
			val.Style = 0
		case "extensions":
			val.Kind = yaml.SequenceNode
			val.Tag = "!!seq"
			val.Style = yaml.FlowStyle
			val.Content = nil
			for _, ext := range bv.Extensions {
				val.Content = append(val.Content, &yaml.Node{
					Kind:  yaml.ScalarNode,
					Tag:   "!!str",
					Value: ext,
				})
			}
		}
	}
	return nil
}

// StatusOptions parameterize a Status call.
type StatusOptions struct {
	ProjectRoot   string
	FrameworkRepo string // optional; when set, drift fields are populated
	NoFetch       bool   // skip fetch when reading framework HEAD
}

// StatusResult is the payload `ape framework status` returns.
type StatusResult struct {
	Installed Metadata  `json:"installed"         yaml:"installed"`
	Current   *RepoInfo `json:"current,omitempty" yaml:"current,omitempty"`
	Drift     *Drift    `json:"drift,omitempty"   yaml:"drift,omitempty"`
}

// Drift summarizes how the project's installed framework state
// compares to the framework repo's current HEAD. Only populated when
// FrameworkRepo was provided.
type Drift struct {
	HashDrift bool     `json:"hashDrift"       yaml:"hashDrift"`
	TagDrift  bool     `json:"tagDrift"        yaml:"tagDrift"`
	Notes     []string `json:"notes,omitempty" yaml:"notes,omitempty"`
}

// Status reads a project's framework.yaml, optionally compares it
// against the framework repo's current HEAD, and returns a structured
// drift report.
func Status(ctx context.Context, opts StatusOptions) (*StatusResult, error) {
	if opts.ProjectRoot == "" {
		return nil, errors.New("StatusOptions.ProjectRoot is required")
	}
	meta, err := ReadMetadata(opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	res := &StatusResult{Installed: *meta}
	if opts.FrameworkRepo == "" {
		return res, nil
	}
	if err := EnsureGitAvailable(); err != nil {
		return nil, err
	}
	if err := validateFrameworkLayout(opts.FrameworkRepo); err != nil {
		return nil, err
	}
	if !IsGitRepo(ctx, opts.FrameworkRepo) {
		return nil, &ValidationError{Code: "framework_not_git_repo", Detail: opts.FrameworkRepo + " is not a git repository"}
	}
	if !opts.NoFetch {
		// Best-effort fetch — don't ff-merge for a status read.
		_, _ = runGit(ctx, opts.FrameworkRepo, "fetch", "origin", "main")
	}
	info, err := readFrameworkInfo(ctx, opts.FrameworkRepo)
	if err != nil {
		return nil, err
	}
	current := &RepoInfo{
		RepoOrigin: info.origin,
		VersionTag: info.tag,
		GitHash:    info.headSHA,
		GitBranch:  info.branch,
	}
	res.Current = current
	drift := &Drift{
		HashDrift: meta.Framework.GitHash != current.GitHash,
		TagDrift:  meta.Framework.VersionTag != current.VersionTag,
	}
	if drift.HashDrift {
		drift.Notes = append(drift.Notes,
			fmt.Sprintf("installed git_hash %s differs from framework HEAD %s", short(meta.Framework.GitHash), short(current.GitHash)))
	}
	if drift.TagDrift {
		drift.Notes = append(drift.Notes,
			fmt.Sprintf("installed version_tag %q differs from framework HEAD tag %q — run \"ape framework update\"", meta.Framework.VersionTag, current.VersionTag))
	}
	res.Drift = drift
	return res, nil
}

func short(sha string) string {
	const w = 7
	if len(sha) <= w {
		return sha
	}
	return sha[:w]
}
