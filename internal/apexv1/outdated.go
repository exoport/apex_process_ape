package apexv1

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// `ape plugins status --outdated` (v1.1 W6, Q-E7): for each marketplace
// _apex/config.yaml lists, the version tags its repository has that are
// newer than the ref it is pinned to — what a team bumping ref: across
// many repos needs to see. Read with `git ls-remote --tags` (no clone, no
// install, nothing written). A tag counts when it is a release version,
// with or without a leading v (v0.2.0, 1.4.0): pre-releases and other tags
// are left out. A ref that is not such a version (a branch, a commit,
// none) has nothing to be newer than: its row names the latest tag.

// MarketplaceTags is one marketplace's row.
type MarketplaceTags struct {
	Marketplace string `json:"marketplace"`
	Source      string `json:"source"`
	Ref         string `json:"ref,omitempty"`
	// Latest is the newest version tag, Newer every one newer than Ref,
	// newest first (always present; [] when none or Ref is no version).
	Latest string   `json:"latest,omitempty"`
	Newer  []string `json:"newer"`
	// Note says why Newer was not computed (a directory marketplace, a ref
	// that is not a version); Error why the tags could not be read.
	Note  string `json:"note,omitempty"`
	Error string `json:"error,omitempty"`
}

// lsRemoteTimeout bounds one marketplace's git ls-remote.
const lsRemoteTimeout = 60 * time.Second

// tagVersion is a tag as a semver version ("v" added), "" when it is not a
// release version.
func tagVersion(tag string) string {
	v := tag
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) || semver.Prerelease(v) != "" || semver.Build(v) != "" {
		return ""
	}
	return v
}

// remoteURL is the URL git reads a marketplace from, "" for a directory.
func (m Marketplace) remoteURL() string {
	switch m.Source {
	case "github":
		return "https://github.com/" + m.Repo + ".git"
	case "git":
		return m.URL
	}
	return ""
}

// lsRemoteTags lists a repository's tag names.
func lsRemoteTags(ctx context.Context, dir, url string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, lsRemoteTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--tags", "--refs", url)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New("git ls-remote " + url + ": " + msg)
	}
	var tags []string
	for line := range strings.SplitSeq(stdout.String(), "\n") {
		if _, ref, ok := strings.Cut(line, "\t"); ok {
			tags = append(tags, strings.TrimPrefix(ref, "refs/tags/"))
		}
	}
	return tags, nil
}

// OutdatedMarketplaces reads each listed marketplace's tags, sorted by
// marketplace name. A marketplace whose tags cannot be read says so in
// its row; nothing fails the call.
func (p *Project) OutdatedMarketplaces(ctx context.Context) ([]MarketplaceTags, error) {
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(cfg.Marketplaces))
	for n := range cfg.Marketplaces {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]MarketplaceTags, 0, len(names))
	for _, n := range names {
		m := cfg.Marketplaces[n]
		row := MarketplaceTags{Marketplace: n, Source: m.Source, Ref: m.Ref, Newer: []string{}}
		url := m.remoteURL()
		if url == "" {
			row.Note = "a directory marketplace has no tags"
			out = append(out, row)
			continue
		}
		tags, err := lsRemoteTags(ctx, p.Root, url)
		if err != nil {
			row.Error = err.Error()
			out = append(out, row)
			continue
		}
		row.Latest, row.Newer, row.Note = newerTags(tags, m.Ref)
		out = append(out, row)
	}
	return out, nil
}

// newerTags is the latest version tag and the ones newer than ref (newest
// first), or a note when ref is no version.
func newerTags(tags []string, ref string) (latest string, newer []string, note string) {
	type tv struct{ tag, v string }
	var vs []tv
	for _, t := range tags {
		if v := tagVersion(t); v != "" {
			vs = append(vs, tv{t, v})
		}
	}
	sort.SliceStable(vs, func(i, j int) bool { return semver.Compare(vs[i].v, vs[j].v) > 0 })
	newer = []string{}
	if len(vs) == 0 {
		return "", newer, "no version tags"
	}
	latest = vs[0].tag
	refV := tagVersion(ref)
	if refV == "" {
		if ref == "" {
			return latest, newer, "no ref: the marketplace follows its default branch; pin a tag (ref: " + latest + ") to compare"
		}
		return latest, newer, "ref " + ref + " is not a version tag; pin a tag to compare"
	}
	for _, x := range vs {
		if semver.Compare(x.v, refV) > 0 {
			newer = append(newer, x.tag)
		}
	}
	return latest, newer, ""
}
