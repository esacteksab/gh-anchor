// SPDX-License-Identifier: MIT

package scaffold_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/esacteksab/gh-anchor/config"
	"github.com/esacteksab/gh-anchor/datasource"
	"github.com/esacteksab/gh-anchor/registry"
	"github.com/esacteksab/gh-anchor/scaffold"
)

func devcontainer(t *testing.T) *scaffold.Plan {
	t.Helper()
	data, err := os.ReadFile("../testdata/Dockerfile.devcontainer")
	require.NoError(t, err)
	return scaffold.Detect([]scaffold.Source{{Path: "Dockerfile", Content: string(data)}})
}

func find(t *testing.T, p *scaffold.Plan, key string) *scaffold.Suggestion {
	t.Helper()
	for i := range p.Suggestions {
		if p.Suggestions[i].Key() == key {
			return &p.Suggestions[i]
		}
	}
	t.Fatalf("no suggestion %s", key)
	return nil
}

func TestIsCandidate(t *testing.T) {
	for _, name := range []string{"NODE_VERSION", "npm_version", "VERSION", "GO_VERSION_MINOR", "ZSH_AUTOSUGGESTIONS_SHA", "TOOL_COMMIT"} {
		assert.True(t, scaffold.IsCandidate(name), name)
	}
	for _, name := range []string{"USERNAME", "USER_UID", "USER_GID", "SHA", "TARGETARCH", "SHASUM"} {
		assert.False(t, scaffold.IsCandidate(name), name)
	}
}

func TestDetectDevcontainer(t *testing.T) {
	p := devcontainer(t)

	var keys []string
	for _, s := range p.Suggestions {
		keys = append(keys, s.Key())
	}
	assert.Equal(t, []string{
		"images.python",
		"images.gh",
		"images.node",
		"args.NODE_VERSION",
		"images.final",
		"args.ZSH_AUTOSUGGESTIONS_SHA",
		"args.NPM_VERSION",
		`images."ghcr.io/zizmorcore/zizmor"`,
		"images.golang",
		`images."ghcr.io/astral-sh/uv"`,
		`images."ghcr.io/aquasecurity/trivy"`,
		`images."ghcr.io/hadolint/hadolint"`,
		"packages.corepack",
		"packages.pnpm",
	}, keys)
	assert.Empty(t, p.Unmatched, "USERNAME, USER_UID and USER_GID are not version ARGs")

	node := find(t, p, "args.NODE_VERSION")
	assert.Equal(t, config.Arg{Datasource: "node-version", LTS: true}, node.Arg)
	assert.Equal(t, "known ARG name NODE_VERSION", node.Reason)

	npm := find(t, p, "args.NPM_VERSION")
	assert.Equal(t, config.Arg{Datasource: "npm", Package: "npm"}, npm.Arg)

	zsh := find(t, p, "args.ZSH_AUTOSUGGESTIONS_SHA")
	assert.Equal(
		t,
		config.Arg{
			Datasource: "github-commits",
			Repo:       "zsh-users/zsh-autosuggestions",
			Track:      "branch",
		},
		zsh.Arg,
	)
	assert.Equal(t, "used with https://github.com/zsh-users/zsh-autosuggestions", zsh.Reason)

	ubuntuDigest := "sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852"
	python := find(t, p, "images.python")
	assert.Equal(
		t,
		config.Image{Image: "ubuntu", Tag: "24.04", Digest: ubuntuDigest, LTS: true},
		python.Image,
	)
	assert.Equal(t, "python", python.Stage)
	final := find(t, p, "images.final")
	assert.Empty(t, final.Stage, "the unnamed last stage is keyed final")
	assert.Equal(t, "Dockerfile:71", scaffold.SortedLocations(final.Where))

	golang := find(t, p, "images.golang")
	assert.Equal(t, config.Image{
		Image: "golang", Tag: "1.26.10", Digest: "sha256:670bb5aa5349ae03b8516764718486f4f16399134bdc9acd0c46a23f043ab05c",
	}, golang.Image, "other images get no LTS default")

	pnpm := find(t, p, "packages.pnpm")
	assert.Equal(t, config.Package{Version: "12.16.1"}, pnpm.Package)
	assert.Equal(t, "corepack", pnpm.Manager)
	assert.Equal(t, "npm", find(t, p, "packages.corepack").Manager)
	assert.Empty(t, p.Conflicts)
}

func TestDetectRules(t *testing.T) {
	src := `ARG BASE_VERSION=3.22
FROM alpine:${BASE_VERSION}
ARG NODE_IMAGE_VERSION=22.11.0
FROM node:${NODE_IMAGE_VERSION}-alpine AS node
FROM node:22.11.0-alpine AS runtime
ARG HADOLINT_VERSION=2.15.1
ARG ACT_VERSION=v0.2.80
ARG TOOL_VERSION=1.4.0
ARG SCOPED_VERSION=5.1.0
ARG CLI_VERSION=2.3.0
ARG GOLANG_DL_VERSION=1.27.3
ARG CPY_VERSION=3.13.1
ARG LIB_SHA=0123456789abcdef0123456789abcdef01234567
ARG RAW_VERSION=1.0.0
ARG MYSTERY_VERSION=4.5.6
ARG FLOATING_VERSION=latest
ARG REF_VERSION=${MYSTERY_VERSION}
ARG USERNAME=me
RUN curl -fsSL https://github.com/hadolint/hadolint/releases/download/v${HADOLINT_VERSION}/hadolint-Linux-x86_64 -o /bin/hadolint \
 && curl -fsSL "https://github.com/nektos/act/archive/refs/tags/${ACT_VERSION}.tar.gz" | tar xz \
 && curl -fsSL https://registry.npmjs.org/@scope%2ftool/-/tool-${SCOPED_VERSION}.tgz -o /tmp/t.tgz \
 && npm install -g @acme/cli@${CLI_VERSION} \
 && curl -fsSL https://go.dev/dl/go${GOLANG_DL_VERSION}.linux-amd64.tar.gz | tar xz \
 && curl -fsSL https://www.python.org/ftp/python/${CPY_VERSION}/Python-${CPY_VERSION}.tgz -o /tmp/py.tgz \
 && curl -fsSL https://raw.githubusercontent.com/owner/raw/v${RAW_VERSION}/install.sh | sh \
 && git clone git@github.com:owner/lib.git && git -C lib checkout ${LIB_SHA} \
 && curl -fsSL https://cli.github.com/packages/tool-$TOOL_VERSION.tgz -o /tmp/x.tgz
`
	p := scaffold.Detect([]scaffold.Source{{Path: "Dockerfile.tools", Content: src}})
	got := map[string]config.Arg{}
	for _, s := range p.Suggestions {
		if s.Section == scaffold.SectionArgs {
			got[s.Name] = s.Arg
		}
	}
	assert.Equal(t, map[string]config.Arg{
		"BASE_VERSION":      {Datasource: "docker", Image: "alpine"},
		"HADOLINT_VERSION":  {Datasource: "github-releases", Repo: "hadolint/hadolint"},
		"ACT_VERSION":       {Datasource: "github-tags", Repo: "nektos/act"},
		"SCOPED_VERSION":    {Datasource: "npm", Package: "@scope/tool"},
		"CLI_VERSION":       {Datasource: "npm", Package: "@acme/cli"},
		"GOLANG_DL_VERSION": {Datasource: "docker", Image: "golang"},
		"CPY_VERSION":       {Datasource: "docker", Image: "python"},
		"RAW_VERSION":       {Datasource: "github-tags", Repo: "owner/raw"},
		"LIB_SHA":           {Datasource: "github-commits", Repo: "owner/lib", Track: "branch"},
	}, got)

	var unmatched []string
	for _, u := range p.Unmatched {
		unmatched = append(unmatched, u.Name+"="+u.Value)
	}
	assert.Equal(
		t,
		[]string{
			"NODE_IMAGE_VERSION=22.11.0",
			"TOOL_VERSION=1.4.0",
			"MYSTERY_VERSION=4.5.6",
			"FLOATING_VERSION=latest",
		},
		unmatched,
		"NODE_IMAGE_VERSION has a tag suffix; TOOL_VERSION isn't used by git; REF_VERSION and USERNAME aren't listed",
	)

	node := find(t, p, "images.runtime")
	assert.Equal(t, `^\d*[02468](\.\d+)*(-.*)?$`, node.Image.LTSPattern)
	cfg, err := config.Parse([]byte(scaffold.Render(p)))
	require.NoError(t, err)
	img, ok := cfg.ImageFor("runtime")
	require.True(t, ok)
	assert.True(t, img.IsLTS("24.1.0-alpine"))
	assert.False(t, img.IsLTS("25.1.0-alpine"))
}

func TestDetectMergesFiles(t *testing.T) {
	p := scaffold.Detect([]scaffold.Source{
		{Path: "Dockerfile", Content: "FROM ubuntu:24.04\nARG NODE_VERSION=22.1.0\n"},
		{Path: "build/Dockerfile", Content: "FROM ubuntu:24.04\nARG NODE_VERSION=22.1.0\n"},
	})
	require.Len(t, p.Suggestions, 2)
	assert.Equal(
		t,
		"Dockerfile:1; build/Dockerfile:1",
		scaffold.SortedLocations(p.Suggestions[0].Where),
	)
	assert.Equal(
		t,
		"Dockerfile:2; build/Dockerfile:2",
		scaffold.SortedLocations(p.Suggestions[1].Where),
	)
	assert.Empty(t, p.Conflicts)
}

// A table that two references would share with different values is a conflict.
func TestDetectConflicts(t *testing.T) {
	p := scaffold.Detect([]scaffold.Source{
		{
			Path:    "Dockerfile",
			Content: "FROM ubuntu:24.04\nRUN npx pnpm@12.16.1 -v && corepack prepare pnpm@12.16.1+sha512.0a --activate\n",
		},
		{Path: "build/Dockerfile", Content: "FROM ubuntu:26.04\nRUN npx pnpm@12.17.2 -v\n"},
	})
	require.Len(t, p.Conflicts, 2)
	assert.Equal(
		t,
		"[images.final] would record ubuntu:24.04 (Dockerfile:1) and ubuntu:26.04 (build/Dockerfile:1); "+
			"one table can only record one. Name the stages (FROM image AS name) or use one version",
		p.Conflicts[0].Error(),
	)
	assert.Contains(
		t,
		p.Conflicts[1].Error(),
		"[packages.pnpm] would record 12.16.1+sha512.0a (Dockerfile:2) and 12.17.2",
	)
	// The same version with and without its hash is one package.
	assert.Equal(
		t,
		config.Package{Version: "12.16.1", Hash: "sha512.0a"},
		find(t, p, "packages.pnpm").Package,
	)
}

// fakeSources answers lookups for Verify.
type fakeSources struct {
	tags     map[string][]string
	missing  map[string]bool // image repositories, npm packages or GitHub repos that 404
	broken   bool            // every lookup fails with a network error
	npm      map[string]datasource.Package
	node     []datasource.NodeRelease
	ghTags   []datasource.Tag
	ghRels   []string
	ghHead   string
	ghBranch string
}

var errNetwork = errors.New("dial tcp: i/o timeout")

func (f *fakeSources) check(key string) error {
	if f.broken {
		return errNetwork
	}
	if f.missing[key] {
		return fmt.Errorf("GET https://example/%s: 404 Not Found: %w", key, datasource.ErrNotFound)
	}
	return nil
}

func (f *fakeSources) ImageTags(_ context.Context, ref registry.Reference) ([]string, error) {
	return f.tags[ref.Repository()], f.check(ref.Repository())
}

func (f *fakeSources) ImageDigest(
	_ context.Context,
	ref registry.Reference,
	_ string,
) (string, error) {
	return "sha256:x", f.check(ref.Repository())
}

func (f *fakeSources) NPMPackage(_ context.Context, name string) (datasource.Package, error) {
	return f.npm[name], f.check(name)
}

func (f *fakeSources) NodeReleases(context.Context) ([]datasource.NodeRelease, error) {
	return f.node, f.check("node")
}

func (f *fakeSources) GitHubTags(_ context.Context, owner, repo string) ([]datasource.Tag, error) {
	return f.ghTags, f.check(owner + "/" + repo)
}

func (f *fakeSources) GitHubReleases(_ context.Context, owner, repo string) ([]string, error) {
	return f.ghRels, f.check(owner + "/" + repo)
}

func (f *fakeSources) GitHubBranchHead(
	_ context.Context,
	owner, repo, _ string,
) (string, string, error) {
	return f.ghHead, f.ghBranch, f.check(owner + "/" + repo)
}

func (f *fakeSources) GitHubResolveRef(context.Context, string, string, string) (string, error) {
	return "", errors.New("unused")
}

const zshSHA = "85919cd1ffa7d2d5412f6d3fe437ebdbeeec4fc5"

func TestVerifyDevcontainer(t *testing.T) {
	p := devcontainer(t)
	p.Verify(context.Background(), &fakeSources{
		npm: map[string]datasource.Package{
			"npm": {
				Versions: []string{"12.2.1"},
				DistTags: map[string]string{"latest": "12.2.1"},
			},
			"corepack": {
				Versions: []string{"0.36.0"},
				Hashes:   map[string]string{"0.36.0": "sha512.c0"},
			},
			"pnpm": {
				Versions: []string{"12.16.1"},
				Hashes:   map[string]string{"12.16.1": "sha512.091d"},
			},
		},
		node: []datasource.NodeRelease{{Version: "v22.23.4", LTS: true}},
		ghTags: []datasource.Tag{
			{Name: "v0.7.1", SHA: "a3e8a2a5b2c1d3e4f5a6b7c8d9e0f1a2b3c4d5e6"},
		},
		ghHead:   zshSHA,
		ghBranch: "master",
	})

	assert.Equal(
		t,
		[]string{"verified: Node.js 22.23.4 is an LTS release"},
		find(t, p, "args.NODE_VERSION").Notes,
	)
	assert.Equal(
		t,
		[]string{"verified: 12.2.1 is a version of npm package npm"},
		find(t, p, "args.NPM_VERSION").Notes,
	)
	zsh := find(t, p, "args.ZSH_AUTOSUGGESTIONS_SHA")
	assert.Equal(t, "branch", zsh.Arg.Track)
	assert.Equal(
		t,
		[]string{
			`verified: 85919cd1ffa7 is the head of master and not tagged, so track = "branch"`,
		},
		zsh.Notes,
	)
	for _, s := range p.Suggestions {
		assert.True(t, s.Selected, s.Key())
		assert.False(t, s.Rejected, s.Key())
	}
	pnpm := find(t, p, "packages.pnpm")
	assert.Equal(
		t,
		config.Package{Version: "12.16.1", Hash: "sha512.091d"},
		pnpm.Package,
		"the hash comes from npm",
	)
	assert.Contains(t, pnpm.Notes, "verified: pnpm@12.16.1 is published; hash from npm")
	assert.Equal(t, "sha512.c0", find(t, p, "packages.corepack").Package.Hash)
}

func TestVerifyPackages(t *testing.T) {
	src := "RUN corepack prepare pnpm@12.16.1+sha512.ff --activate && npx cowsay@latest && npx yo@9.9.9\n"
	p := scaffold.Detect([]scaffold.Source{{Path: "Dockerfile", Content: src}})
	p.Verify(context.Background(), &fakeSources{npm: map[string]datasource.Package{
		"pnpm": {
			Versions: []string{"12.16.1"},
			Hashes:   map[string]string{"12.16.1": "sha512.091d"},
		},
		"cowsay": {Versions: []string{"1.6.0"}, DistTags: map[string]string{"latest": "1.6.0"}},
		"yo":     {Versions: []string{"5.0.0"}},
	}})
	pnpm := find(t, p, "packages.pnpm")
	assert.Equal(t, "sha512.ff", pnpm.Package.Hash, "the Dockerfile's hash is what's recorded")
	assert.Contains(
		t,
		pnpm.Notes[len(pnpm.Notes)-1],
		"the Dockerfile's hash differs from the one npm publishes",
	)
	assert.Equal(
		t,
		[]string{"latest is a dist-tag (now 1.6.0); the first run pins a version"},
		find(t, p, "packages.cowsay").Notes,
	)
	assert.Equal(
		t,
		[]string{"9.9.9 is not a published version of yo; check the value"},
		find(t, p, "packages.yo").Notes,
	)
}

func TestVerifyRefines(t *testing.T) {
	src := `ARG LIB_SHA=` + zshSHA + `
ARG HADOLINT_VERSION=2.15.1
ARG GHOST_VERSION=1.0.0
RUN git clone https://github.com/owner/lib && git -C lib checkout ${LIB_SHA} \
 && curl -L https://github.com/hadolint/hadolint/releases/download/v${HADOLINT_VERSION}/x \
 && curl -L https://registry.npmjs.org/ghost-pkg/-/ghost-pkg-${GHOST_VERSION}.tgz \
 && npx missing-pkg@1.0.0
FROM ghcr.io/private/thing:1.0.0
`
	p := scaffold.Detect([]scaffold.Source{{Path: "Dockerfile", Content: src}})
	p.Verify(context.Background(), &fakeSources{
		missing: map[string]bool{
			"ghost-pkg":             true,
			"missing-pkg":           true,
			"ghcr.io/private/thing": true,
		},
		ghTags: []datasource.Tag{{Name: "v0.7.1", SHA: zshSHA}, {Name: "v2.15.1", SHA: "b"}},
		ghRels: []string{"v2.15.0"},
		npm:    map[string]datasource.Package{},
	})

	lib := find(t, p, "args.LIB_SHA")
	assert.Equal(t, "tags", lib.Arg.Track, "a tagged commit follows tags")
	assert.Contains(t, lib.Notes[0], "is tag v0.7.1")

	hadolint := find(t, p, "args.HADOLINT_VERSION")
	assert.Equal(t, "github-tags", hadolint.Arg.Datasource, "not a release, but a tag")

	ghost := find(t, p, "args.GHOST_VERSION")
	assert.True(t, ghost.Rejected)
	assert.False(t, ghost.Selected)
	assert.Equal(t, []string{`npm package "ghost-pkg" not found`}, ghost.Notes)

	pkg := find(t, p, "packages.missing-pkg")
	assert.False(t, pkg.Selected, "a package that doesn't exist is deselected")

	img := find(t, p, "images.final")
	assert.True(t, img.Selected, "images stay selected; a missing tag is only noted")
	assert.Equal(t, []string{"tag 1.0.0 of ghcr.io/private/thing not found"}, img.Notes)

	out := scaffold.Render(p)
	cfg, err := config.Parse([]byte(out))
	require.NoError(t, err)
	assert.Contains(
		t,
		out,
		"# [args.GHOST_VERSION]\n#   datasource = \"npm\"\n#   package    = \"ghost-pkg\"\n#   target     = \"\"\n#   bump       = false\n",
	)
	assert.Contains(
		t,
		out,
		"[packages.missing-pkg]\n  version = \"1.0.0\"\n  hash    = \"\"\n  target  = \"\"\n  bump    = false\n  skip    = true\n",
	)
	_, ok := cfg.Args["GHOST_VERSION"]
	assert.False(t, ok)
	assert.True(t, cfg.Packages["missing-pkg"].Skip)
}

func TestVerifyFallsBackOffline(t *testing.T) {
	p := devcontainer(t)
	p.Verify(context.Background(), &fakeSources{broken: true})
	for _, s := range p.Suggestions {
		assert.True(t, s.Selected, s.Key())
		assert.False(t, s.Rejected, s.Key())
	}
	zsh := find(t, p, "args.ZSH_AUTOSUGGESTIONS_SHA")
	assert.Equal(t, "branch", zsh.Arg.Track)
	assert.Equal(
		t,
		[]string{"not verified (dial tcp: i/o timeout); using the offline default"},
		zsh.Notes,
	)
}

func TestRenderDevcontainer(t *testing.T) {
	p := devcontainer(t)
	out := scaffold.Render(p)

	// The golden file is also the config the updater tests use.
	golden, err := os.ReadFile("../testdata/gh-anchor.devcontainer.toml")
	require.NoError(t, err)
	assert.Equal(t, string(golden), out)

	cfg, err := config.Parse([]byte(out))
	require.NoError(t, err)
	assert.Len(t, cfg.Images, 9)
	assert.Len(t, cfg.Packages, 2)
	assert.Len(t, cfg.Args, 3)
	assert.NotContains(t, out, "without any settings")
}

func TestRenderDeselected(t *testing.T) {
	p := devcontainer(t)
	for i := range p.Suggestions {
		switch p.Suggestions[i].Key() {
		case "args.NPM_VERSION", "images.golang", "images.final", "packages.pnpm":
			p.Suggestions[i].Selected = false
		}
	}
	out := scaffold.Render(p)
	cfg, err := config.Parse([]byte(out))
	require.NoError(t, err)

	assert.Contains(
		t,
		out,
		"# Deselected in `gh anchor init`: skip = true, so gh-anchor leaves it alone.\n[args.NPM_VERSION]\n",
	)
	assert.True(t, cfg.Args["NPM_VERSION"].Skip)
	for _, key := range []string{"golang", "final"} {
		img, ok := cfg.ImageFor(key)
		require.True(t, ok, key)
		assert.True(t, img.Skip, key)
		assert.NotEmpty(t, img.Tag, "deselected images still record what the Dockerfile uses")
	}
	final, _ := cfg.ImageFor("final")
	assert.True(t, final.LTS)
	assert.True(t, cfg.Packages["pnpm"].Skip)
	assert.Equal(t, "12.16.1", cfg.Packages["pnpm"].Version)
}

func TestRenderSpellsOutDefaults(t *testing.T) {
	src := "ARG LIB_SHA=0123456789abcdef0123456789abcdef01234567\n" +
		"RUN git clone https://github.com/owner/lib && git -C lib checkout ${LIB_SHA}\n" +
		"ARG GO_VERSION=1.27.3\nARG HADOLINT_VERSION=2.15.1\n" +
		"RUN curl -L https://github.com/hadolint/hadolint/releases/download/v${HADOLINT_VERSION}/x\n" +
		"FROM node:22.11.0 AS node\nFROM scratch\n"
	out := scaffold.Render(scaffold.Detect([]scaffold.Source{{Path: "Dockerfile", Content: src}}))
	for _, want := range []string{
		"# FROM node:22.11.0 AS node (Dockerfile:6)\n",
		"[images.node]\n  image       = \"node\"\n  tag         = \"22.11.0\"\n  digest      = \"\"\n" +
			"  fixed-tag   = false\n  lts         = true\n  lts-pattern = '^\\d*[02468](\\.\\d+)*(-.*)?$'\n  target      = \"\"\n  bump        = false\n  skip        = false\n",
		"[args.LIB_SHA]\n  datasource = \"github-commits\"\n  repo       = \"owner/lib\"\n  track      = \"branch\"\n  bump       = false\n  skip       = false\n",
		"[args.GO_VERSION]\n  datasource = \"docker\"\n  image      = \"golang\"\n  target     = \"\"\n  bump       = false\n  skip       = false\n",
		"[args.HADOLINT_VERSION]\n  datasource = \"github-releases\"\n  repo       = \"hadolint/hadolint\"\n  target     = \"\"\n  bump       = false\n  skip       = false\n",
	} {
		assert.Contains(t, out, want)
	}
	_, err := config.Parse([]byte(out))
	require.NoError(t, err)
}
