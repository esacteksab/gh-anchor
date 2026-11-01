// SPDX-License-Identifier: MIT

package updater_test

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/charmbracelet/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/esacteksab/gh-anchor/config"
	"github.com/esacteksab/gh-anchor/datasource"
	"github.com/esacteksab/gh-anchor/registry"
	"github.com/esacteksab/gh-anchor/updater"
)

// fakeSources serves canned data keyed by the real names used in the devcontainer Dockerfile.
type fakeSources struct {
	tags     map[string][]string
	npm      map[string]datasource.Package
	node     []datasource.NodeRelease
	ghTags   map[string][]datasource.Tag
	ghRels   map[string][]string
	branches map[string]string // owner/repo@branch -> sha ("" branch = default)
	calls    []string
}

func digest(repo, tag string) string {
	sum := sha256.Sum256([]byte(repo + ":" + tag))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// hash is the fake sha512 npm publishes for a package version, in corepack's format.
func hash(name, version string) string {
	sum := sha512.Sum512([]byte(name + "@" + version))
	return "sha512." + hex.EncodeToString(sum[:])
}

// withHashes fills in the hash of every version.
func withHashes(name string, p datasource.Package) datasource.Package {
	p.Hashes = map[string]string{}
	for _, v := range p.Versions {
		p.Hashes[v] = hash(name, v)
	}
	return p
}

func parse(t *testing.T, toml string) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(toml))
	require.NoError(t, err)
	return cfg
}

func (f *fakeSources) ImageTags(_ context.Context, ref registry.Reference) ([]string, error) {
	f.calls = append(f.calls, "tags "+ref.Repository())
	t, ok := f.tags[ref.Repository()]
	if !ok {
		return nil, fmt.Errorf("unknown repository %s", ref.Repository())
	}
	return t, nil
}

func (f *fakeSources) ImageDigest(
	_ context.Context,
	ref registry.Reference,
	tag string,
) (string, error) {
	f.calls = append(f.calls, "digest "+ref.Repository()+":"+tag)
	return digest(ref.Repository(), tag), nil
}

func (f *fakeSources) NPMPackage(_ context.Context, name string) (datasource.Package, error) {
	f.calls = append(f.calls, "npm "+name)
	p, ok := f.npm[name]
	if !ok {
		return datasource.Package{}, fmt.Errorf("unknown package %s", name)
	}
	return p, nil
}

func (f *fakeSources) NodeReleases(context.Context) ([]datasource.NodeRelease, error) {
	return f.node, nil
}

func (f *fakeSources) GitHubTags(_ context.Context, owner, repo string) ([]datasource.Tag, error) {
	return f.ghTags[owner+"/"+repo], nil
}

func (f *fakeSources) GitHubReleases(_ context.Context, owner, repo string) ([]string, error) {
	return f.ghRels[owner+"/"+repo], nil
}

func (f *fakeSources) GitHubBranchHead(
	_ context.Context,
	owner, repo, branch string,
) (string, string, error) {
	sha, ok := f.branches[owner+"/"+repo+"@"+branch]
	if !ok {
		return "", "", errors.New("no such branch")
	}
	if branch == "" {
		branch = "master"
	}
	return sha, branch, nil
}

func (f *fakeSources) GitHubResolveRef(_ context.Context, owner, repo, ref string) (string, error) {
	for _, t := range f.ghTags[owner+"/"+repo] {
		if t.Name == ref {
			return t.SHA, nil
		}
	}
	if sha, ok := f.branches[owner+"/"+repo+"@"+ref]; ok {
		return sha, nil
	}
	return "", fmt.Errorf("ref %s not found", ref)
}

const (
	zshHead = "4444444444444444444444444444444444444444"
	zshV071 = "a3e8a2a5b2c1d3e4f5a6b7c8d9e0f1a2b3c4d5e6"
	zshV080 = "b3e8a2a5b2c1d3e4f5a6b7c8d9e0f1a2b3c4d5e6"
	zshCur  = "85919cd1ffa7d2d5412f6d3fe437ebdbeeec4fc5"
)

func newFakes() *fakeSources {
	return &fakeSources{
		tags: map[string][]string{
			"docker.io/library/ubuntu": {
				"22.04",
				"24.04",
				"24.10",
				"25.04",
				"25.10",
				"26.04",
				"26.10",
				"latest",
				"noble",
			},
			"docker.io/library/golang": {
				"1.26.10",
				"1.26.11",
				"1.27.3",
				"1.27.3-alpine",
				"1.27",
				"1.28rc1",
				"latest",
			},
			"ghcr.io/zizmorcore/zizmor": {"1.31.1", "1.32.0", "2.0.0"},
			"ghcr.io/astral-sh/uv": {
				"0.13.6",
				"0.13.7",
				"0.14.0",
				"0.14.0-alpine",
				"python3.12-bookworm",
			},
			"ghcr.io/aquasecurity/trivy": {"0.76.1", "0.77.0", "canary", "latest"},
			"ghcr.io/hadolint/hadolint": {
				"v2.15.1",
				"v2.15.2",
				"v2.15.2-alpine",
				"v3.0.0",
				"latest",
			},
		},
		npm: map[string]datasource.Package{
			"npm": withHashes("npm", datasource.Package{
				Versions: []string{"12.2.1", "12.3.0", "12.3.1-beta.0", "13.0.0"},
				DistTags: map[string]string{"latest": "13.0.0"},
			}),
			"corepack": withHashes("corepack", datasource.Package{
				Versions: []string{"0.36.0", "0.37.0", "1.0.0"},
				DistTags: map[string]string{"latest": "1.0.0"},
			}),
			"pnpm": withHashes("pnpm", datasource.Package{
				Versions: []string{"12.16.1", "12.17.2", "13.1.0"},
				DistTags: map[string]string{"latest": "13.1.0"},
			}),
		},
		node: []datasource.NodeRelease{
			{Version: "v27.0.0", LTS: false},
			{Version: "v26.2.0", LTS: false},
			{Version: "v24.12.0", LTS: true},
			{Version: "v22.24.0", LTS: true},
			{Version: "v22.23.4", LTS: true},
		},
		ghTags: map[string][]datasource.Tag{
			"zsh-users/zsh-autosuggestions": {
				{Name: "v0.8.0", SHA: zshV080},
				{Name: "v0.7.1", SHA: zshV071},
			},
		},
		branches: map[string]string{"zsh-users/zsh-autosuggestions@": zshHead},
	}
}

func loadDevcontainer(t *testing.T) (string, *config.Config) {
	t.Helper()
	src, err := os.ReadFile("../testdata/Dockerfile.devcontainer")
	require.NoError(t, err)
	cfg, err := config.Load("../testdata/gh-anchor.devcontainer.toml", true)
	require.NoError(t, err)
	return string(src), cfg
}

// changed returns "line: old -> new" for every line that differs.
func changed(t *testing.T, before, after string) []string {
	t.Helper()
	a, b := strings.Split(before, "\n"), strings.Split(after, "\n")
	require.Len(t, b, len(a), "line count must not change")
	var out []string
	for i := range a {
		if a[i] != b[i] {
			out = append(out, fmt.Sprintf("%d: %s", i+1, strings.TrimSpace(b[i])))
		}
	}
	return out
}

// imageCalls returns the registry lookups among calls.
func imageCalls(calls []string) []string {
	var out []string
	for _, c := range calls {
		if !strings.HasPrefix(c, "npm ") {
			out = append(out, c)
		}
	}
	return out
}

// records renders records as "table field=value" with shortened values.
func records(rs []updater.Record) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Table()+" "+r.Field+"="+updater.Shorten(r.Value))
	}
	return out
}

func TestPinModeOnPinnedDockerfile(t *testing.T) {
	src, cfg := loadDevcontainer(t)
	f := newFakes()
	u := &updater.Updater{Mode: updater.ModePin, Config: cfg, Sources: f}

	res := u.Process(context.Background(), src)
	require.Empty(t, res.Errors)
	require.Empty(t, res.Mismatches)
	assert.Empty(
		t,
		imageCalls(f.calls),
		"pin mode must not query registries for already-pinned images",
	)
	// The only change: pnpm, passed to corepack, is pinned with its hash.
	assert.Equal(t, []string{
		"164: && corepack prepare pnpm@12.16.1+" + hash("pnpm", "12.16.1") + " --activate",
	}, changed(t, src, res.Output))
	assert.Equal(t, []string{
		"packages.corepack hash=" + updater.Shorten(hash("corepack", "0.36.0")),
		"packages.pnpm hash=" + updater.Shorten(hash("pnpm", "12.16.1")),
	}, records(res.Records), "image records already match; package hashes are filled in")
}

func TestUpdateModeStaysWithinMajor(t *testing.T) {
	src, cfg := loadDevcontainer(t)
	u := &updater.Updater{Mode: updater.ModeUpdate, Config: cfg, Sources: newFakes()}

	res := u.Process(context.Background(), src)
	require.Empty(t, res.Errors)

	ubuntu := "ubuntu:24.04@" + digest("docker.io/library/ubuntu", "24.04")
	assert.Equal(t, []string{
		"2: FROM " + ubuntu + " AS python",
		"14: FROM " + ubuntu + " AS gh",
		"37: FROM " + ubuntu + " AS node",
		"43: ARG NODE_VERSION=22.24.0",
		"71: FROM " + ubuntu,
		"78: ARG ZSH_AUTOSUGGESTIONS_SHA=" + zshHead,
		"79: ARG NPM_VERSION=12.3.0",
		"143: COPY --from=ghcr.io/zizmorcore/zizmor:1.32.0@" + digest(
			"ghcr.io/zizmorcore/zizmor",
			"1.32.0",
		) +
			" /usr/bin/zizmor /usr/local/bin/zizmor",
		"144: COPY --link --from=golang:1.27.3@" + digest("docker.io/library/golang", "1.27.3") +
			" /usr/local/go /usr/local/go",
		"147: COPY --from=ghcr.io/astral-sh/uv:0.14.0@" + digest(
			"ghcr.io/astral-sh/uv",
			"0.14.0",
		) + " /uv /uvx /bin/",
		"148: COPY --from=ghcr.io/aquasecurity/trivy:0.77.0@" + digest(
			"ghcr.io/aquasecurity/trivy",
			"0.77.0",
		) +
			" /usr/local/bin/trivy /usr/bin/trivy",
		"149: COPY --from=ghcr.io/hadolint/hadolint:v2.15.2@" + digest(
			"ghcr.io/hadolint/hadolint",
			"v2.15.2",
		) +
			" /bin/hadolint /bin/",
		"162: && npm install --global --prefix /usr/local corepack@0.37.0 \\",
		"164: && corepack prepare pnpm@12.17.2+" + hash("pnpm", "12.17.2") + " --activate",
	}, changed(t, src, res.Output))
	assert.Len(t, res.Changes, 14)
	assert.Equal(
		t,
		"17 checked, 14 updated, 0 up to date, 3 skipped (not in gh-anchor.devcontainer.toml: 3), 0 failed",
		res.Summary.String(),
	)

	ubuntuDigest := updater.Shorten(digest("docker.io/library/ubuntu", "24.04"))
	assert.Equal(t, []string{
		"images.python digest=" + ubuntuDigest,
		"images.gh digest=" + ubuntuDigest,
		"images.node digest=" + ubuntuDigest,
		"images.final digest=" + ubuntuDigest,
		`images."ghcr.io/zizmorcore/zizmor" tag=1.32.0`,
		`images."ghcr.io/zizmorcore/zizmor" digest=` + updater.Shorten(
			digest("ghcr.io/zizmorcore/zizmor", "1.32.0"),
		),
		"images.golang tag=1.27.3",
		"images.golang digest=" + updater.Shorten(digest("docker.io/library/golang", "1.27.3")),
		`images."ghcr.io/astral-sh/uv" tag=0.14.0`,
		`images."ghcr.io/astral-sh/uv" digest=` + updater.Shorten(
			digest("ghcr.io/astral-sh/uv", "0.14.0"),
		),
		`images."ghcr.io/aquasecurity/trivy" tag=0.77.0`,
		`images."ghcr.io/aquasecurity/trivy" digest=` + updater.Shorten(
			digest("ghcr.io/aquasecurity/trivy", "0.77.0"),
		),
		`images."ghcr.io/hadolint/hadolint" tag=v2.15.2`,
		`images."ghcr.io/hadolint/hadolint" digest=` + updater.Shorten(
			digest("ghcr.io/hadolint/hadolint", "v2.15.2"),
		),
		"packages.corepack version=0.37.0",
		"packages.corepack hash=" + updater.Shorten(hash("corepack", "0.37.0")),
		"packages.pnpm version=12.17.2",
		"packages.pnpm hash=" + updater.Shorten(hash("pnpm", "12.17.2")),
	}, records(res.Records))
}

// Without a config file nothing is updated or looked up.
func TestNoConfigUpdatesNothing(t *testing.T) {
	src, _ := loadDevcontainer(t)
	f := newFakes()
	u := &updater.Updater{Mode: updater.ModeBump, Config: parse(t, ""), Sources: f}

	res := u.Process(context.Background(), src)
	require.Empty(t, res.Errors)
	assert.Equal(t, src, res.Output)
	assert.Empty(t, f.calls)
	assert.Empty(t, res.Records)
	assert.Equal(t,
		"17 checked, 0 updated, 0 up to date, 17 skipped (no .gh-anchor.toml found: 17), 0 failed",
		res.Summary.String())
}

// Only what the config lists is updated; a config without tables updates nothing.
func TestOnlyListedReferencesAreUpdated(t *testing.T) {
	src := "FROM ubuntu:24.04 AS base\nCOPY --from=golang:1.26.10 /go /go\nRUN npx pnpm@12.16.1 -v\nFROM golang:1.26.10\n"
	cfg := parse(
		t,
		"[images.base]\n  image = \"ubuntu\"\n\n[packages.pnpm]\n  version = \"12.16.1\"\n",
	)
	cfg.Found = true
	f := newFakes()
	u := &updater.Updater{Mode: updater.ModeUpdate, Config: cfg, Sources: f}

	res := u.Process(context.Background(), src)
	require.Empty(t, res.Errors)
	assert.Equal(t, "FROM ubuntu:24.04@"+digest("docker.io/library/ubuntu", "24.04")+" AS base\n"+
		"COPY --from=golang:1.26.10 /go /go\nRUN npx pnpm@12.17.2 -v\nFROM golang:1.26.10\n", res.Output)
	assert.Equal(
		t,
		"4 checked, 2 updated, 0 up to date, 2 skipped (not in .gh-anchor.toml: 2), 0 failed",
		res.Summary.String(),
	)
	assert.NotContains(
		t,
		f.calls,
		"tags docker.io/library/golang",
		"unlisted images are not looked up",
	)
}

func TestUpdateReportsUpToDate(t *testing.T) {
	src := "FROM ubuntu:24.04@" + digest("docker.io/library/ubuntu", "24.04") + "\n" +
		"COPY --from=ghcr.io/zizmorcore/zizmor:2.0.0@" + digest("ghcr.io/zizmorcore/zizmor", "2.0.0") + " /z /z\n" +
		"COPY --from=${IMAGE} /x /x\n" +
		"RUN corepack prepare pnpm@13.1.0+" + hash("pnpm", "13.1.0") + " --activate\n"
	cfg := parse(t, `
[images.final]
  image = "ubuntu"
[images."ghcr.io/zizmorcore/zizmor"]
  image = "ghcr.io/zizmorcore/zizmor"
[packages.pnpm]
`)
	u := &updater.Updater{Mode: updater.ModeUpdate, Config: cfg, Sources: newFakes()}

	res := u.Process(context.Background(), src)
	require.Empty(t, res.Errors)
	assert.Empty(t, res.Changes)
	assert.Equal(
		t,
		"4 checked, 0 updated, 3 up to date, 1 skipped (uses a variable: 1), 0 failed",
		res.Summary.String(),
	)
	assert.Len(t, res.Records, 6, "empty records are filled in even when nothing changed")
}

func TestBumpModeCrossesMajors(t *testing.T) {
	src, cfg := loadDevcontainer(t)
	u := &updater.Updater{Mode: updater.ModeBump, Config: cfg, Sources: newFakes()}

	res := u.Process(context.Background(), src)
	require.Empty(t, res.Errors)

	got := map[string]string{}
	for _, c := range res.Changes {
		got[fmt.Sprintf("%d %s", c.Line, c.Label)] = c.To
	}
	assert.Equal(t, "24.12.0", got["43 ARG NODE_VERSION"], "lts: true skips Current releases")
	assert.Equal(t, "13.0.0", got["79 ARG NPM_VERSION"])
	assert.True(t, strings.HasPrefix(got["143 COPY"], "ghcr.io/zizmorcore/zizmor:2.0.0@"))
	assert.True(
		t,
		strings.HasPrefix(got["144 COPY"], "golang:1.27.3@"),
		"rc and suffixed tags are never picked",
	)
	assert.True(t, strings.HasPrefix(got["149 COPY"], "ghcr.io/hadolint/hadolint:v3.0.0@"))
	assert.Equal(t, "ubuntu:26.04@"+digest("docker.io/library/ubuntu", "26.04"), got["2 FROM"],
		"lts = true moves to the newest LTS tag, skipping 24.10, 25.04, 25.10 and 26.10")
	assert.Equal(
		t,
		"1.0.0",
		got["162 RUN corepack"],
		"installed with npm: no hash in the Dockerfile",
	)
	assert.Equal(t, "13.1.0+"+hash("pnpm", "13.1.0"), got["164 RUN pnpm"])
	assert.Contains(t, records(res.Records), "images.python tag=26.04")
}

func TestBumpMovesBaseImageToNewestTag(t *testing.T) {
	src := "FROM ubuntu:24.04\nFROM golang:1.26.10 AS build\n"
	tables := "[images.stage-0]\n  image = \"ubuntu\"\n[images.build]\n  image = \"golang\"\n"
	u := &updater.Updater{Mode: updater.ModeBump, Config: parse(t, tables), Sources: newFakes()}

	res := u.Process(context.Background(), src)
	require.Empty(t, res.Errors)
	assert.Equal(t, "FROM ubuntu:26.10@"+digest("docker.io/library/ubuntu", "26.10")+"\n"+
		"FROM golang:1.27.3@"+digest("docker.io/library/golang", "1.27.3")+" AS build\n", res.Output)

	// fixed-tag still wins over --bump.
	u = &updater.Updater{
		Mode:    updater.ModeBump,
		Config:  parse(t, tables+"  fixed-tag = true\n"),
		Sources: newFakes(),
	}
	res = u.Process(context.Background(), src)
	assert.Contains(t, res.Output, "FROM golang:1.26.10@")
}

func TestPinModePinsFloatingReferences(t *testing.T) {
	src := `FROM ubuntu:24.04
FROM golang
COPY --from=ghcr.io/astral-sh/uv:0.13.6 /uv /bin/
ARG NODE_VERSION=22
ARG NPM_VERSION=12.2
ARG ZSH_AUTOSUGGESTIONS_SHA=v0.7.1
RUN corepack prepare pnpm@latest --activate
`
	cfg := parse(t, `
[args.NODE_VERSION]
  datasource = "node-version"
[args.NPM_VERSION]
  datasource = "npm"
  package    = "npm"
[args.ZSH_AUTOSUGGESTIONS_SHA]
  datasource = "github-commits"
  repo       = "zsh-users/zsh-autosuggestions"
[images.stage-0]
  image = "ubuntu"
  tag   = "24.04"
[images.final]
  image = "golang"
[images."ghcr.io/astral-sh/uv"]
  image = "ghcr.io/astral-sh/uv"
[packages.pnpm]
  version = "latest"
`)
	u := &updater.Updater{Mode: updater.ModePin, Config: cfg, Sources: newFakes()}

	res := u.Process(context.Background(), src)
	require.Empty(t, res.Errors)
	want := `FROM ubuntu:24.04@` + digest("docker.io/library/ubuntu", "24.04") + `
FROM golang:latest@` + digest("docker.io/library/golang", "latest") + `
COPY --from=ghcr.io/astral-sh/uv:0.13.6@` + digest("ghcr.io/astral-sh/uv", "0.13.6") + ` /uv /bin/
ARG NODE_VERSION=22.24.0
ARG NPM_VERSION=12.2.1
ARG ZSH_AUTOSUGGESTIONS_SHA=` + zshV071 + `
RUN corepack prepare pnpm@13.1.0+` + hash("pnpm", "13.1.0") + ` --activate
`
	assert.Equal(t, want, res.Output)
	assert.Equal(t, []string{
		"images.stage-0 digest=" + updater.Shorten(digest("docker.io/library/ubuntu", "24.04")),
		"images.final tag=latest",
		"images.final digest=" + updater.Shorten(digest("docker.io/library/golang", "latest")),
		`images."ghcr.io/astral-sh/uv" tag=0.13.6`,
		`images."ghcr.io/astral-sh/uv" digest=` + updater.Shorten(
			digest("ghcr.io/astral-sh/uv", "0.13.6"),
		),
		"packages.pnpm version=13.1.0",
		"packages.pnpm hash=" + updater.Shorten(hash("pnpm", "13.1.0")),
	}, records(res.Records))
}

func TestCommitTrackTags(t *testing.T) {
	cfg, err := config.Parse([]byte(`
[args.SHA]
  datasource = "github-commits"
  repo       = "zsh-users/zsh-autosuggestions"
  track      = "tags"
`))
	require.NoError(t, err)
	f := newFakes()
	f.ghTags["zsh-users/zsh-autosuggestions"] = []datasource.Tag{
		{Name: "v1.0.0", SHA: "c3e8a2a5b2c1d3e4f5a6b7c8d9e0f1a2b3c4d5e6"},
		{Name: "v0.8.0", SHA: zshV080},
		{Name: "v0.7.1", SHA: zshV071},
	}

	run := func(mode updater.Mode, src string) updater.Result {
		u := &updater.Updater{Mode: mode, Config: cfg, Sources: f}
		return u.Process(context.Background(), src)
	}

	res := run(updater.ModeUpdate, "ARG SHA="+zshV071+"\n")
	require.Empty(t, res.Errors)
	assert.Equal(t, "ARG SHA="+zshV080+"\n", res.Output, "v0.7.1 -> newest v0.x")

	res = run(updater.ModeBump, "ARG SHA="+zshV071+"\n")
	assert.Equal(t, "ARG SHA=c3e8a2a5b2c1d3e4f5a6b7c8d9e0f1a2b3c4d5e6\n", res.Output)

	// An untagged commit has no major version to stay within.
	res = run(updater.ModeUpdate, "ARG SHA="+zshCur+"\n")
	require.Len(t, res.Errors, 1)
	assert.Contains(t, res.Errors[0].Error(), "not a tagged release")

	res = run(updater.ModeBump, "ARG SHA="+zshCur+"\n")
	require.Empty(t, res.Errors)
	assert.Contains(t, res.Output, "c3e8a2a5")
}

func TestErrorsAreCollectedAndProcessingContinues(t *testing.T) {
	cfg := parse(t, `
[args.X]
  datasource = "npm"
  package    = "missing"
[images."ghcr.io/unknown/image"]
  image = "ghcr.io/unknown/image"
[images.final]
  image = "ghcr.io/zizmorcore/zizmor"
[packages.cowsay]
`)
	src := "COPY --from=ghcr.io/unknown/image:1.0.0@sha256:" + strings.Repeat(
		"a",
		64,
	) + " /x /x\nARG X=1.0.0\nARG Y=2.0.0\nRUN npx cowsay@1.6.0\nFROM ghcr.io/zizmorcore/zizmor:1.31.1\n"
	u := &updater.Updater{Mode: updater.ModeUpdate, Config: cfg, Sources: newFakes()}

	res := u.Process(context.Background(), src)
	var msgs []string
	for _, e := range res.Errors {
		msgs = append(msgs, e.Error())
	}
	sort.Strings(msgs)
	require.Len(t, msgs, 3)
	assert.Contains(t, msgs[0], "line 1: COPY: unknown repository ghcr.io/unknown/image")
	assert.Contains(t, msgs[1], "line 2: ARG X: unknown package missing")
	assert.Contains(t, msgs[2], "line 4: RUN cowsay: unknown package cowsay")
	// The good reference was still updated (FROM keeps its tag; the digest is added).
	require.Len(t, res.Changes, 1)
	assert.Equal(t, 5, res.Changes[0].Line)
	assert.Equal(
		t,
		"ghcr.io/zizmorcore/zizmor:1.31.1@"+digest("ghcr.io/zizmorcore/zizmor", "1.31.1"),
		res.Changes[0].To,
	)
	assert.Equal(
		t,
		"5 checked, 1 updated, 0 up to date, 1 skipped (not in .gh-anchor.toml: 1), 3 failed",
		res.Summary.String(),
	)
}

func TestNPMRespectsLatestDistTag(t *testing.T) {
	f := newFakes()
	// pnpm publishes to next-12 before promoting to latest.
	f.npm["pnpm"] = withHashes("pnpm", datasource.Package{
		Versions: []string{"12.16.1", "12.17.0", "13.0.0-rc.1"},
		DistTags: map[string]string{"latest": "12.16.1", "next-12": "12.17.0"},
	})
	for _, src := range []string{
		"RUN corepack prepare pnpm@12.16.1+" + hash("pnpm", "12.16.1") + " --activate\n",
		"RUN npx pnpm@12.17.0 -v\n",
	} {
		for _, mode := range []updater.Mode{updater.ModeUpdate, updater.ModeBump} {
			u := &updater.Updater{Mode: mode, Config: parse(t, "[packages.pnpm]\n"), Sources: f}
			res := u.Process(context.Background(), src)
			require.Empty(t, res.Errors)
			assert.Equal(
				t,
				src,
				res.Output,
				"%s must not move past latest, nor downgrade a newer pin",
				mode,
			)
		}
	}
}

func TestCorepackHashes(t *testing.T) {
	cfg := parse(t, "[packages.pnpm]\n  version = \"12.16.1\"\n")
	run := func(src string) updater.Result {
		u := &updater.Updater{Mode: updater.ModeUpdate, Config: cfg, Sources: newFakes()}
		return u.Process(context.Background(), src)
	}

	// corepack gets the hash; npm doesn't, but it is recorded either way.
	for src, want := range map[string]string{
		"RUN corepack prepare pnpm@12.16.1 --activate\n": "RUN corepack prepare pnpm@12.17.2+" + hash("pnpm", "12.17.2") + " --activate\n",
		"RUN corepack install -g pnpm@12.16.1\n":         "RUN corepack install -g pnpm@12.17.2+" + hash("pnpm", "12.17.2") + "\n",
		"RUN npm install -g pnpm@12.16.1\n":              "RUN npm install -g pnpm@12.17.2\n",
		"RUN npm install -g pnpm@12.16.1+" + hash("pnpm", "12.16.1") + "\n": "RUN npm install -g pnpm@12.17.2+" +
			hash("pnpm", "12.17.2") + "\n",
	} {
		res := run(src)
		require.Empty(t, res.Errors, src)
		assert.Equal(t, want, res.Output, src)
		assert.Equal(
			t,
			[]string{
				"packages.pnpm version=12.17.2",
				"packages.pnpm hash=" + updater.Shorten(hash("pnpm", "12.17.2")),
			},
			records(res.Records),
			src,
		)
	}

	// A hash in the Dockerfile that npm doesn't publish for that version is an error.
	res := run("RUN corepack prepare pnpm@12.16.1+" + hash("pnpm", "12.17.2") + " --activate\n")
	require.Len(t, res.Errors, 1)
	assert.Contains(
		t,
		res.Errors[0].Error(),
		"line 1: RUN pnpm: the hash of pnpm@12.16.1 in the Dockerfile doesn't match the one npm publishes",
	)
	assert.Empty(t, res.Changes)
}

func TestSplitPackageSpec(t *testing.T) {
	for in, want := range map[string][2]string{
		"12.16.1":                 {"12.16.1", ""},
		"12.16.1+sha512.091d1dee": {"12.16.1", "sha512.091d1dee"},
		"1.0.0+build.5":           {"1.0.0+build.5", ""},
		"latest":                  {"latest", ""},
	} {
		v, h := updater.SplitPackageSpec(in)
		assert.Equal(t, want, [2]string{v, h}, in)
	}
}

// The Dockerfile wins: a recorded value it doesn't match is reported as drift and
// overwritten with the Dockerfile's value (or with the updated one), and the run goes on.
func TestDriftIsOverwritten(t *testing.T) {
	ubuntu := digest("docker.io/library/ubuntu", "24.04")
	src := "FROM ubuntu:24.04@" + ubuntu + " AS base\n" +
		"COPY --from=ghcr.io/astral-sh/uv:0.13.6 /uv /bin/\n" +
		"RUN corepack prepare pnpm@12.16.1+" + hash("pnpm", "12.16.1") + " --activate\n" +
		"FROM golang:1.26.10\n"
	stale := "sha256:" + strings.Repeat("c", 64)
	tests := map[string]struct {
		tables, drift string
		records       []string // in pin mode: the Dockerfile's values
	}{
		"tag": {
			"[images.base]\n  image = \"ubuntu\"\n  tag = \"22.04\"\n",
			`line 1: FROM: [images.base] recorded tag = "22.04", but the Dockerfile has "24.04": recording the Dockerfile's`,
			[]string{"images.base tag=24.04", "images.base digest=" + updater.Shorten(ubuntu)},
		},
		"digest": {
			"[images.base]\n  image = \"ubuntu\"\n  tag = \"24.04\"\n  digest = \"" + stale + "\"\n",
			`[images.base] recorded digest = "sha256:cccccccccccc…", but the Dockerfile has "` + updater.Shorten(
				ubuntu,
			) + `"`,
			[]string{"images.base digest=" + updater.Shorten(ubuntu)},
		},
		"image": {
			"[images.\"ghcr.io/astral-sh/uv\"]\n  image = \"ghcr.io/astral-sh/uvx\"\n",
			`[images."ghcr.io/astral-sh/uv"] recorded image = "ghcr.io/astral-sh/uvx", but the Dockerfile has "ghcr.io/astral-sh/uv"`,
			[]string{
				`images."ghcr.io/astral-sh/uv" image=ghcr.io/astral-sh/uv`,
				`images."ghcr.io/astral-sh/uv" tag=0.13.6`,
				`images."ghcr.io/astral-sh/uv" digest=` + updater.Shorten(
					digest("ghcr.io/astral-sh/uv", "0.13.6"),
				),
			},
		},
		"package version": {
			"[packages.pnpm]\n  version = \"12.17.2\"\n",
			`line 3: RUN pnpm: [packages.pnpm] recorded version = "12.17.2", but the Dockerfile has "12.16.1"`,
			[]string{
				"packages.pnpm version=12.16.1",
				"packages.pnpm hash=" + updater.Shorten(hash("pnpm", "12.16.1")),
			},
		},
		"package hash": {
			"[packages.pnpm]\n  version = \"12.16.1\"\n  hash = \"sha512.00\"\n",
			`[packages.pnpm] recorded hash = "sha512.00", but the Dockerfile has "` + updater.Shorten(
				hash("pnpm", "12.16.1"),
			) + `"`,
			[]string{"packages.pnpm hash=" + updater.Shorten(hash("pnpm", "12.16.1"))},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			u := &updater.Updater{
				Mode:    updater.ModePin,
				Config:  parse(t, tt.tables),
				Sources: newFakes(),
			}
			res := u.Process(context.Background(), src)
			require.Empty(t, res.Mismatches, "drift doesn't stop the run")
			require.Empty(t, res.Errors)
			require.Len(t, res.Drift, 1)
			assert.Contains(t, res.Drift[0].Error(), tt.drift)
			assert.Equal(t, tt.records, records(res.Records))
		})
	}

	// After an update, the updated value is recorded, not the Dockerfile's old one.
	cfg := parse(
		t,
		"[images.\"ghcr.io/astral-sh/uv\"]\n  image = \"ghcr.io/astral-sh/uv\"\n  tag = \"0.11.7\"\n",
	)
	u := &updater.Updater{Mode: updater.ModeUpdate, Config: cfg, Sources: newFakes()}
	res := u.Process(context.Background(), "COPY --from=ghcr.io/astral-sh/uv:0.13.6 /uv /bin/\n")
	require.Len(t, res.Drift, 1)
	assert.Equal(t, []string{
		`images."ghcr.io/astral-sh/uv" tag=0.14.0`,
		`images."ghcr.io/astral-sh/uv" digest=` + updater.Shorten(
			digest("ghcr.io/astral-sh/uv", "0.14.0"),
		),
	}, records(res.Records))

	// When the update lands on the stale recorded value, there is nothing to record.
	cfg = parse(
		t,
		"[images.\"ghcr.io/astral-sh/uv\"]\n  image = \"ghcr.io/astral-sh/uv\"\n  tag = \"0.14.0\"\n",
	)
	u = &updater.Updater{Mode: updater.ModeUpdate, Config: cfg, Sources: newFakes()}
	res = u.Process(context.Background(), "COPY --from=ghcr.io/astral-sh/uv:0.13.6 /uv /bin/\n")
	assert.NotContains(t, strings.Join(records(res.Records), " "), "tag=")

	// A failed lookup still corrects the record to what the Dockerfile has.
	cfg = parse(
		t,
		"[images.\"ghcr.io/unknown/x\"]\n  image = \"ghcr.io/unknown/x\"\n  tag = \"9.9.9\"\n",
	)
	u = &updater.Updater{Mode: updater.ModeUpdate, Config: cfg, Sources: newFakes()}
	res = u.Process(context.Background(), "COPY --from=ghcr.io/unknown/x:1.0.0 /x /x\n")
	require.Len(t, res.Errors, 1)
	assert.Equal(t, []string{`images."ghcr.io/unknown/x" tag=1.0.0`}, records(res.Records))
}

// One table can only record one image or version, within and across Dockerfiles.
func TestTableUsedTwice(t *testing.T) {
	cfg := parse(t, "[images.golang]\n  image = \"golang\"\n[images.final]\n  image = \"ubuntu\"\n")
	u := &updater.Updater{
		Mode:    updater.ModeUpdate,
		Config:  cfg,
		Sources: newFakes(),
		File:    "Dockerfile",
	}
	res := u.Process(
		context.Background(),
		"FROM ubuntu:24.04\nCOPY --from=golang:1.26.10 /a /a\nCOPY --from=golang:1.26.10 /b /b\nCOPY --from=golang:1.27.3 /c /c\n",
	)
	require.Len(t, res.Mismatches, 1)
	assert.Equal(t, "line 4: COPY: [images.golang] is used with golang:1.27.3 here but "+
		"golang:1.26.10 on line 2; one table can only record one (rename the stage, or list it once)",
		res.Mismatches[0].Error())

	// Another Dockerfile whose final stage uses a different image.
	u = &updater.Updater{
		Mode:    updater.ModeUpdate,
		Config:  cfg,
		Sources: newFakes(),
		File:    "Dockerfile",
	}
	require.Empty(t, u.Process(context.Background(), "FROM ubuntu:24.04\n").Mismatches)
	u.File = "build/Dockerfile"
	res = u.Process(context.Background(), "FROM ubuntu:26.04\n")
	require.Len(t, res.Mismatches, 1)
	assert.Contains(t, res.Mismatches[0].Error(), "on line 1 of Dockerfile;")
}

func TestImageSettings(t *testing.T) {
	cfg := parse(t, "[images.stage-0]\n  skip = true\n[images.final]\n  image = \"ubuntu\"\n")
	src := "FROM ghcr.io/zizmorcore/zizmor:1.31.1\nFROM ubuntu:noble@sha256:" + strings.Repeat(
		"b",
		64,
	) + "\n"
	u := &updater.Updater{Mode: updater.ModeUpdate, Config: cfg, Sources: newFakes()}

	res := u.Process(context.Background(), src)
	require.Empty(t, res.Errors)
	require.Len(t, res.Changes, 1, "skipped image untouched; non-version tag gets a digest refresh")
	assert.Equal(t, "ubuntu:noble@"+digest("docker.io/library/ubuntu", "noble"), res.Changes[0].To)
	assert.Equal(
		t,
		"2 checked, 1 updated, 0 up to date, 1 skipped (skip = true: 1), 0 failed",
		res.Summary.String(),
	)
}

func TestShorten(t *testing.T) {
	assert.Equal(
		t,
		"ubuntu:24.04@sha256:fe81560990fa…",
		updater.Shorten(
			"ubuntu:24.04@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852",
		),
	)
	assert.Equal(t, "85919cd1ffa7…", updater.Shorten(zshCur))
	assert.Equal(
		t,
		"12.16.1+sha512.091d1dee7927…",
		updater.Shorten("12.16.1+sha512.091d1dee7927575661b25dab3c2ae31cafc1c54d"),
	)
	assert.Equal(t, "22.24.0", updater.Shorten("22.24.0"))
}

// Every reference in the devcontainer Dockerfile gets one debug line describing what
// was looked up and the outcome; stage references are listed as ignored.
func TestDebugTraceCoversEveryReference(t *testing.T) {
	src, cfg := loadDevcontainer(t)
	var buf strings.Builder
	logger := log.NewWithOptions(&buf, log.Options{Level: log.DebugLevel})
	u := &updater.Updater{Mode: updater.ModeBump, Config: cfg, Sources: newFakes(), Log: logger}

	res := u.Process(context.Background(), src)
	require.Empty(t, res.Errors)
	out := buf.String()

	for _, want := range []string{
		"found 17 references (9 images, 6 ARGs, 2 packages) and 3 other candidates",
		"line 2: FROM ubuntu:24.04@sha256:fe81560990fa…: 9 tags in docker.io/library/ubuntu; 3 LTS tags; newest: 26.04;",
		"line 43: ARG NODE_VERSION 22.23.4: 5 Node.js releases; 3 LTS; newest: 24.12.0 → updated to 24.12.0",
		"line 75: ARG USERNAME devcontainer: skipped (not in gh-anchor.devcontainer.toml)",
		"line 77: ARG USER_GID ${USER_UID}: skipped (not in gh-anchor.devcontainer.toml)",
		"line 78: ARG ZSH_AUTOSUGGESTIONS_SHA 85919cd1ffa7…: zsh-users/zsh-autosuggestions master is at 444444444444",
		"line 142: COPY gh: ignored (refers to an earlier build stage)",
		"line 145: COPY node: ignored (refers to an earlier build stage)",
		"line 146: COPY node: ignored (refers to an earlier build stage)",
		"line 144: COPY golang:1.26.10@sha256:670bb5aa5349…: 7 tags in docker.io/library/golang; newest: 1.27.3;",
		"line 164: RUN pnpm 12.16.1: npm pnpm: 3 versions, latest 13.1.0; newest: 13.1.0; pinning with its sha512 " +
			"for corepack → updated to 13.1.0+sha512." + hash("pnpm", "13.1.0")[7:19] + "…",
	} {
		assert.Contains(t, out, want)
	}
	assert.Equal(
		t,
		17+3,
		strings.Count(out, "line "),
		"one line per reference and per ignored candidate",
	)
}

// GitHub tags and releases usually carry a "v" the ARG doesn't (v${VERSION} in the URL).
func TestGitHubVersionsIgnoreVPrefix(t *testing.T) {
	cfg, err := config.Parse([]byte(`
[args.HADOLINT_VERSION]
  datasource = "github-releases"
  repo       = "hadolint/hadolint"

[args.ACT_VERSION]
  datasource = "github-tags"
  repo       = "nektos/act"
`))
	require.NoError(t, err)
	f := newFakes()
	f.ghRels = map[string][]string{"hadolint/hadolint": {"v2.15.1", "v2.16.0", "v3.0.0"}}
	f.ghTags["nektos/act"] = []datasource.Tag{
		{Name: "v0.2.80", SHA: "a"},
		{Name: "v0.2.81", SHA: "b"},
	}
	u := &updater.Updater{Mode: updater.ModeUpdate, Config: cfg, Sources: f}

	res := u.Process(context.Background(), "ARG HADOLINT_VERSION=2.15.1\nARG ACT_VERSION=v0.2.80\n")
	require.Empty(t, res.Errors)
	assert.Equal(t, "ARG HADOLINT_VERSION=2.16.0\nARG ACT_VERSION=v0.2.81\n", res.Output)
}

// In update mode, tables with bump = true, or named in Bump, cross major versions;
// the rest stays within its major version.
func TestBumpPerTable(t *testing.T) {
	src := "FROM ubuntu:24.04 AS base\n" +
		"COPY --from=ghcr.io/zizmorcore/zizmor:1.31.1 /z /z\n" +
		"COPY --from=ghcr.io/hadolint/hadolint:v2.15.1 /h /h\n" +
		"ARG NPM_VERSION=12.2.1\n" +
		"RUN npx pnpm@12.16.1 -v\n"
	tables := func(bump string) string {
		return `
[images.base]
  image = "ubuntu"
  lts   = true` + bump + `
[images."ghcr.io/zizmorcore/zizmor"]
  image = "ghcr.io/zizmorcore/zizmor"
[images."ghcr.io/hadolint/hadolint"]
  image = "ghcr.io/hadolint/hadolint"
  bump  = true
[args.NPM_VERSION]
  datasource = "npm"
  package    = "npm"
[packages.pnpm]
`
	}
	run := func(u *updater.Updater) map[string]string {
		t.Helper()
		res := u.Process(context.Background(), src)
		require.Empty(t, res.Errors)
		got := map[string]string{}
		for _, c := range res.Changes {
			got[fmt.Sprintf("%d %s", c.Line, c.Label)] = updater.Shorten(c.To)
		}
		return got
	}
	trim := func(ref string) string { ref, _, _ = strings.Cut(ref, "@"); return ref }

	// bump = true on hadolint only.
	got := run(
		&updater.Updater{
			Mode:    updater.ModeUpdate,
			Config:  parse(t, tables("")),
			Sources: newFakes(),
		},
	)
	assert.Equal(t, "ubuntu:24.04", trim(got["1 FROM"]), "FROM keeps its tag unless bumped")
	assert.Equal(t, "ghcr.io/zizmorcore/zizmor:1.32.0", trim(got["2 COPY"]))
	assert.Equal(
		t,
		"ghcr.io/hadolint/hadolint:v3.0.0",
		trim(got["3 COPY"]),
		"bump = true crosses majors",
	)
	assert.Equal(t, "12.3.0", got["4 ARG NPM_VERSION"])
	assert.Equal(t, "12.17.2", got["5 RUN pnpm"])

	// -b=base,NPM_VERSION,pnpm adds to bump = true.
	got = run(&updater.Updater{
		Mode: updater.ModeUpdate, Config: parse(t, tables("")), Sources: newFakes(),
		Bump: map[string]bool{"images.base": true, "args.NPM_VERSION": true, "packages.pnpm": true},
	})
	assert.Equal(
		t,
		"ubuntu:26.04",
		trim(got["1 FROM"]),
		"a bumped FROM moves to the newest LTS tag",
	)
	assert.Equal(t, "ghcr.io/zizmorcore/zizmor:1.32.0", trim(got["2 COPY"]))
	assert.Equal(t, "ghcr.io/hadolint/hadolint:v3.0.0", trim(got["3 COPY"]))
	assert.Equal(t, "13.0.0", got["4 ARG NPM_VERSION"])
	assert.Equal(t, "13.1.0", got["5 RUN pnpm"])

	// The picker's selection replaces bump = true.
	got = run(&updater.Updater{
		Mode: updater.ModeUpdate, Config: parse(t, tables("")), Sources: newFakes(),
		Bump: map[string]bool{`images."ghcr.io/zizmorcore/zizmor"`: true}, BumpOnly: true,
	})
	assert.Equal(t, "ghcr.io/zizmorcore/zizmor:2.0.0", trim(got["2 COPY"]))
	assert.Equal(
		t,
		"ghcr.io/hadolint/hadolint:v2.15.2",
		trim(got["3 COPY"]),
		"deselected in the picker",
	)

	// Pin mode never bumps; bump mode bumps everything.
	got = run(
		&updater.Updater{
			Mode:    updater.ModePin,
			Config:  parse(t, tables("\n  bump  = true")),
			Sources: newFakes(),
		},
	)
	assert.Equal(t, "ghcr.io/hadolint/hadolint:v2.15.1", trim(got["3 COPY"]))
	assert.Equal(t, "ubuntu:24.04", trim(got["1 FROM"]))
	got = run(
		&updater.Updater{Mode: updater.ModeBump, Config: parse(t, tables("")), Sources: newFakes()},
	)
	assert.Equal(t, "ghcr.io/zizmorcore/zizmor:2.0.0", trim(got["2 COPY"]))
	assert.Equal(t, "13.1.0", got["5 RUN pnpm"])
}

func TestBumpReasonIsTraced(t *testing.T) {
	var buf strings.Builder
	logger := log.NewWithOptions(&buf, log.Options{Level: log.DebugLevel})
	cfg := parse(
		t,
		"[images.a]\n  image = \"ubuntu\"\n  bump = true\n[images.final]\n  image = \"golang\"\n",
	)
	u := &updater.Updater{
		Mode: updater.ModeUpdate, Config: cfg, Sources: newFakes(), Log: logger,
		Bump: map[string]bool{"images.final": true},
	}
	res := u.Process(context.Background(), "FROM ubuntu:24.04 AS a\nFROM golang:1.26.10\n")
	require.Empty(t, res.Errors)
	assert.Contains(t, buf.String(), "line 1: FROM ubuntu:24.04: bumping (bump = true); ")
	assert.Contains(t, buf.String(), "line 2: FROM golang:1.26.10: bumping (-b); ")
}

// target moves to the newest release in a version line and keeps it there, even
// across majors under -u; with lts = true, a line without LTS releases moves to the
// next LTS line at or above it.
func TestTarget(t *testing.T) {
	f := newFakes()
	f.node = []datasource.NodeRelease{
		{Version: "v27.0.0"},
		{Version: "v26.1.0", LTS: true},
		{Version: "v25.2.0"},
		{Version: "v24.12.0", LTS: true},
		{Version: "v24.0.0"},
		{Version: "v22.24.0", LTS: true},
		{Version: "v22.22.3", LTS: true},
	}
	run := func(mode updater.Mode, tables, src string) updater.Result {
		t.Helper()
		u := &updater.Updater{Mode: mode, Config: parse(t, tables), Sources: f}
		return u.Process(context.Background(), src)
	}
	node := func(target string, lts bool) string {
		return fmt.Sprintf(
			"[args.NODE_VERSION]\n  datasource = \"node-version\"\n  lts = %t\n  target = %s\n",
			lts,
			target,
		)
	}
	for _, tt := range []struct {
		name, tables string
		mode         updater.Mode
		want         string
	}{
		{"24 under -u", node("24", false), updater.ModeUpdate, "24.12.0"},
		{"bump doesn't go past the target", node("24", false), updater.ModeBump, "24.12.0"},
		{"25 without lts", node("25", false), updater.ModeUpdate, "25.2.0"},
		{"25 with lts: next LTS line", node("25", true), updater.ModeUpdate, "26.1.0"},
		{"23 with lts: 24", node(`"23"`, true), updater.ModeUpdate, "24.12.0"},
		{"pin mode ignores the target", node("24", false), updater.ModePin, "22.22.3"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res := run(tt.mode, tt.tables, "ARG NODE_VERSION=22.22.3\n")
			require.Empty(t, res.Errors)
			assert.Equal(t, "ARG NODE_VERSION="+tt.want+"\n", res.Output)
		})
	}

	res := run(updater.ModeUpdate, node("27", true), "ARG NODE_VERSION=22.22.3\n")
	require.Len(t, res.Errors, 1)
	assert.Contains(t, res.Errors[0].Error(), "target 27: no LTS release in the 27 line or above")

	// An older line is an explicit choice too.
	res = run(updater.ModeUpdate, node("22", false), "ARG NODE_VERSION=24.0.0\n")
	require.Empty(t, res.Errors)
	assert.Equal(t, "ARG NODE_VERSION=22.24.0\n", res.Output)

	// Images: golang stays on 1.26.x; a FROM image moves to the target line's newest
	// LTS tag without bump.
	images := `
[images.final]
  image  = "ubuntu"
  lts    = true
  target = "26"
[images.golang]
  image  = "golang"
  target = "1.26"
[packages.pnpm]
  target = 13
`
	res = run(updater.ModeUpdate, images,
		"FROM ubuntu:24.04\nCOPY --from=golang:1.26.10 /go /go\nRUN npx pnpm@12.16.1 -v\n")
	require.Empty(t, res.Errors)
	assert.Equal(t, "FROM ubuntu:26.04@"+digest("docker.io/library/ubuntu", "26.04")+"\n"+
		"COPY --from=golang:1.26.11@"+digest("docker.io/library/golang", "1.26.11")+" /go /go\n"+
		"RUN npx pnpm@13.1.0 -v\n", res.Output)
}

func TestTargetTrace(t *testing.T) {
	var buf strings.Builder
	logger := log.NewWithOptions(&buf, log.Options{Level: log.DebugLevel})
	f := newFakes()
	f.node = []datasource.NodeRelease{
		{Version: "v26.1.0", LTS: true},
		{Version: "v25.2.0"},
		{Version: "v22.22.3", LTS: true},
	}
	cfg := parse(
		t,
		"[args.NODE_VERSION]\n  datasource = \"node-version\"\n  lts = true\n  target = 25\n",
	)
	u := &updater.Updater{Mode: updater.ModeUpdate, Config: cfg, Sources: f, Log: logger}
	require.Empty(t, u.Process(context.Background(), "ARG NODE_VERSION=22.22.3\n").Errors)
	assert.Contains(
		t,
		buf.String(),
		"target 25: no LTS release in that line; next LTS line has 26.1.0 → updated to 26.1.0",
	)
}

// An image pinned by digest only gets the tag that has that digest, in every mode;
// -u then updates it like any tagged image.
func TestDigestOnlyGetsItsTag(t *testing.T) {
	cfg := parse(
		t,
		"[images.\"ghcr.io/aquasecurity/trivy\"]\n  image = \"ghcr.io/aquasecurity/trivy\"\n",
	)
	src := func(tag string) string {
		return "COPY --from=ghcr.io/aquasecurity/trivy@" + digest(
			"ghcr.io/aquasecurity/trivy",
			tag,
		) + " /t /t\n"
	}

	f := newFakes()
	u := &updater.Updater{Mode: updater.ModePin, Config: cfg, Sources: f}
	res := u.Process(context.Background(), src("0.76.1"))
	require.Empty(t, res.Errors)
	assert.Equal(
		t,
		"COPY --from=ghcr.io/aquasecurity/trivy:0.76.1@"+digest(
			"ghcr.io/aquasecurity/trivy",
			"0.76.1",
		)+" /t /t\n",
		res.Output,
		"pin mode adds the tag and keeps the digest",
	)
	assert.Equal(t, []string{
		`images."ghcr.io/aquasecurity/trivy" tag=0.76.1`,
		`images."ghcr.io/aquasecurity/trivy" digest=` + updater.Shorten(
			digest("ghcr.io/aquasecurity/trivy", "0.76.1"),
		),
	}, records(res.Records))

	u = &updater.Updater{Mode: updater.ModeUpdate, Config: cfg, Sources: newFakes()}
	res = u.Process(context.Background(), src("0.76.1"))
	require.Empty(t, res.Errors)
	assert.Contains(t, res.Output, "trivy:0.77.0@"+digest("ghcr.io/aquasecurity/trivy", "0.77.0"))

	// A digest no version tag has is skipped.
	u = &updater.Updater{Mode: updater.ModeUpdate, Config: cfg, Sources: newFakes()}
	res = u.Process(
		context.Background(),
		"COPY --from=ghcr.io/aquasecurity/trivy@sha256:"+strings.Repeat("c", 64)+" /t /t\n",
	)
	require.Empty(t, res.Errors)
	assert.Equal(
		t,
		"1 checked, 0 updated, 0 up to date, 1 skipped (no version tag has this digest: 1), 0 failed",
		res.Summary.String(),
	)
}
