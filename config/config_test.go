// SPDX-License-Identifier: MIT

package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/esacteksab/gh-anchor/config"
	"github.com/esacteksab/gh-anchor/registry"
)

func TestParseValid(t *testing.T) {
	cfg, err := config.Parse([]byte(`
[args.NODE_VERSION]
  datasource = "node-version"
  lts        = true

[args.NPM_VERSION]
  datasource = "npm"
  package    = "npm"

[args.ZSH_AUTOSUGGESTIONS_SHA]
  datasource = "github-commits"
  repo       = "zsh-users/zsh-autosuggestions"
  track      = "branch"

[args.GO_VERSION]
  datasource = "docker"
  image      = "golang"

[args.LEGACY]
  skip = true

[images.final]
  image     = "ubuntu"
  tag       = "24.04"
  digest    = "sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852"
  fixed-tag = false
  lts       = true
  skip      = false

[images."ghcr.io/hadolint/hadolint"]
  skip = true

[images."ghcr.io/astral-sh/uv"]
  image       = "ghcr.io/astral-sh/uv"
  lts         = true
  lts-pattern = '^0\.13\.\d+$'

[images.golang]
  image     = "golang"
  fixed-tag = true

[packages.pnpm]
  version = "12.16.1"
  hash    = "sha512.091d1dee"
  skip    = false

[packages.corepack]
  skip = true
`))
	require.NoError(t, err)
	assert.False(t, cfg.Found, "Parse does not read a file")
	assert.True(t, cfg.Args["NODE_VERSION"].LTS)
	assert.Equal(t, "zsh-users", cfg.Args["ZSH_AUTOSUGGESTIONS_SHA"].Owner())
	assert.Equal(t, "zsh-autosuggestions", cfg.Args["ZSH_AUTOSUGGESTIONS_SHA"].RepoName())

	final, ok := cfg.ImageFor("final")
	require.True(t, ok)
	assert.True(t, final.LTS)
	assert.Equal(t, "24.04", final.Tag)
	img, ok := cfg.ImageFor("ghcr.io/hadolint/hadolint")
	require.True(t, ok)
	assert.True(t, img.Skip)
	img, _ = cfg.ImageFor("golang")
	assert.True(t, img.FixedTag)
	_, ok = cfg.ImageFor("ghcr.io/zizmorcore/zizmor")
	assert.False(t, ok, "images without a table are not listed")

	pnpm, ok := cfg.PackageFor("pnpm")
	require.True(t, ok)
	assert.Equal(t, config.Package{Version: "12.16.1", Hash: "sha512.091d1dee"}, pnpm)
	corepack, _ := cfg.PackageFor("corepack")
	assert.True(t, corepack.Skip)
	_, ok = cfg.PackageFor("npm")
	assert.False(t, ok)

	for _, tt := range []struct{ section, key, field, want string }{
		{config.SectionImages, "final", "tag", "24.04"},
		{config.SectionImages, "golang", "digest", ""},
		{config.SectionPackages, "pnpm", "version", "12.16.1"},
		{config.SectionPackages, "pnpm", "hash", "sha512.091d1dee"},
	} {
		got, ok := cfg.Recorded(tt.section, tt.key, tt.field)
		assert.True(t, ok, "%v", tt)
		assert.Equal(t, tt.want, got, "%v", tt)
	}
	_, ok = cfg.Recorded(config.SectionImages, "missing", "tag")
	assert.False(t, ok)
	_, ok = cfg.Recorded(config.SectionImages, "final", "version")
	assert.False(t, ok)
	assert.False(t, cfg.Empty())
}

func TestSameRepository(t *testing.T) {
	cfg, err := config.Parse([]byte("[images.base]\n  image = \"ubuntu\"\n"))
	require.NoError(t, err)
	img, _ := cfg.ImageFor("base")
	// The recorded image matches regardless of how the reference is spelled.
	for _, s := range []string{"ubuntu:24.04", "docker.io/library/ubuntu", "index.docker.io/library/ubuntu:26.04"} {
		ref, err := registry.ParseReference(s)
		require.NoError(t, err)
		assert.True(t, img.SameRepository(ref), s)
	}
	ref, _ := registry.ParseReference("ghcr.io/owner/ubuntu:24.04")
	assert.False(t, img.SameRepository(ref))
}

func TestUbuntuLTSRule(t *testing.T) {
	cfg, err := config.Parse([]byte("[images.final]\n  image = \"ubuntu\"\n  lts = true\n"))
	require.NoError(t, err)
	img, _ := cfg.ImageFor("final")
	for tag, want := range map[string]bool{
		"20.04": true, "22.04": true, "24.04": true, "26.04": true,
		"23.04": false, "24.10": false, "25.04": false, "25.10": false, "noble": false, "24.04.1": false,
	} {
		assert.Equal(t, want, img.IsLTS(tag), tag)
	}

	// The built-in rule also applies to Ubuntu mirrors.
	cfg, err = config.Parse(
		[]byte("[images.base]\n  image = \"public.ecr.aws/lts/ubuntu\"\n  lts = true\n"),
	)
	require.NoError(t, err)
	img, _ = cfg.ImageFor("base")
	assert.True(t, img.IsLTS("26.04"))
}

func TestLTSPattern(t *testing.T) {
	cfg, err := config.Parse(
		[]byte(
			"[images.node]\n  image = \"node\"\n  lts         = true\n  lts-pattern = '^(20|22|24)\\.'\n",
		),
	)
	require.NoError(t, err)
	img, _ := cfg.ImageFor("node")
	assert.True(t, img.IsLTS("24.12.0"))
	assert.False(t, img.IsLTS("25.1.0"))
}

func TestParseErrors(t *testing.T) {
	tests := map[string]struct{ data, want string }{
		"unknown key": {
			"[args.X]\n  datasource = \"npm\"\n  pakage = \"npm\"\n", "unknown key(s): args.X.pakage",
		},
		"unknown section":    {"[arg.X]\n  datasource = \"npm\"\n", "unknown key(s): arg"},
		"missing datasource": {"[args.X]\n  package = \"npm\"\n", "args.X: datasource is required"},
		"unknown datasource": {"[args.X]\n  datasource = \"pypi\"\n", `unknown datasource "pypi"`},
		"npm needs package": {
			"[args.X]\n  datasource = \"npm\"\n",
			`datasource "npm" needs package`,
		},
		"bad repo": {
			"[args.X]\n  datasource = \"github-tags\"\n  repo = \"justname\"\n", "needs repo",
		},
		"bad track": {
			"[args.X]\n  datasource = \"github-commits\"\n  repo = \"a/b\"\n  track = \"release\"\n", "track must be",
		},
		"docker needs image": {
			"[args.X]\n  datasource = \"docker\"\n",
			`datasource "docker" needs image`,
		},
		"image required": {"[images.final]\n  tag = \"24.04\"\n", "image is required"},
		"bad image":      {"[images.x]\n  image = \"Not/Valid\"\n", "invalid repository name"},
		"image with tag": {
			"[images.x]\n  image = \"ubuntu:24.04\"\n",
			"must not include a tag or digest",
		},
		"bad digest": {
			"[images.x]\n  image = \"ubuntu\"\n  digest = \"sha256:abc\"\n",
			"digest must look like",
		},
		"bad package hash": {
			"[packages.pnpm]\n  hash = \"sha512-AAAA\"\n",
			"hash must look like",
		},
		"lts needs pattern": {
			"[images.go]\n  image = \"golang\"\n  lts = true\n",
			"lts = true needs lts-pattern",
		},
		"bad lts pattern": {
			"[images.go]\n  image = \"golang\"\n  lts = true\n  lts-pattern = '('\n",
			"lts-pattern",
		},
		"pattern without lts": {
			"[images.go]\n  image = \"golang\"\n  lts-pattern = '^1\\.'\n", "lts-pattern is set but lts is not true",
		},
		"unknown image key": {
			"[images.go]\n  image = \"golang\"\n  version = \"1\"\n",
			"images.go.version",
		},
		"unknown package key": {"[packages.pnpm]\n  tag = \"1\"\n", "packages.pnpm.tag"},
		"wrong type": {
			"[args.X]\n  datasource = \"node-version\"\n  lts = \"yes\"\n",
			"lts",
		},
		"bad syntax":  {"[args.X\n", "toml"},
		"yaml syntax": {"args:\n  X:\n    datasource: npm\n", "toml"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := config.Parse([]byte(tt.data))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()

	// A missing default file is fine...
	cfg, err := config.Load(filepath.Join(dir, config.DefaultFile), false)
	require.NoError(t, err)
	assert.True(t, cfg.Empty())
	assert.False(t, cfg.Found)

	// ...but an explicitly requested one must exist.
	_, err = config.Load(filepath.Join(dir, "custom.toml"), true)
	require.Error(t, err)

	path := filepath.Join(dir, "empty.toml")
	require.NoError(t, os.WriteFile(path, []byte("# nothing yet\n"), 0o600))
	cfg, err = config.Load(path, true)
	require.NoError(t, err)
	assert.True(t, cfg.Found)
	assert.Equal(t, path, cfg.Path)
}

const tablesSrc = `
[images.node]
  image = "ubuntu"
  bump  = true
[images."ghcr.io/zizmorcore/zizmor"]
  image = "ghcr.io/zizmorcore/zizmor"
[images.old]
  skip = true
[packages.node]
[packages.pnpm]
  bump = true
[args.NODE_VERSION]
  datasource = "node-version"
`

func TestTables(t *testing.T) {
	cfg, err := config.Parse([]byte(tablesSrc))
	require.NoError(t, err)
	var names []string
	for _, tb := range cfg.Tables() {
		names = append(names, fmt.Sprintf("%s bump=%t skip=%t", tb.Name(), tb.Bump, tb.Skip))
	}
	assert.Equal(t, []string{
		`images."ghcr.io/zizmorcore/zizmor" bump=false skip=false`,
		"images.node bump=true skip=false",
		"images.old bump=false skip=true",
		"packages.node bump=false skip=false",
		"packages.pnpm bump=true skip=false",
		"args.NODE_VERSION bump=false skip=false",
	}, names)
	assert.True(t, cfg.BumpsTable(config.SectionImages, "node"))
	assert.False(t, cfg.BumpsTable(config.SectionPackages, "node"))
	assert.True(t, cfg.BumpsTable(config.SectionPackages, "pnpm"))
	assert.False(t, cfg.BumpsTable(config.SectionArgs, "NODE_VERSION"))
	assert.False(t, cfg.BumpsTable(config.SectionArgs, "MISSING"))
}

func TestResolveTable(t *testing.T) {
	cfg, err := config.Parse([]byte(tablesSrc))
	require.NoError(t, err)
	for name, want := range map[string]string{
		"pnpm":                               "packages.pnpm",
		"NODE_VERSION":                       "args.NODE_VERSION",
		"ghcr.io/zizmorcore/zizmor":          `images."ghcr.io/zizmorcore/zizmor"`,
		"images.node":                        "images.node",
		"packages.node":                      "packages.node",
		`images."ghcr.io/zizmorcore/zizmor"`: `images."ghcr.io/zizmorcore/zizmor"`,
		"images.ghcr.io/zizmorcore/zizmor":   `images."ghcr.io/zizmorcore/zizmor"`,
	} {
		got, err := cfg.ResolveTable(name)
		require.NoError(t, err, name)
		assert.Equal(t, want, got.Name(), name)
	}

	_, err = cfg.ResolveTable("node")
	require.Error(t, err)
	assert.Equal(t, `"node" matches images.node and packages.node; use the full name`, err.Error())

	_, err = cfg.ResolveTable("golang")
	require.Error(t, err)
	assert.Equal(
		t,
		`no table named "golang" (the config has: ghcr.io/zizmorcore/zizmor, node, old, node, pnpm, NODE_VERSION)`,
		err.Error(),
	)

	_, err = cfg.ResolveTable("node_version")
	require.Error(t, err, "names are case-sensitive, like ARG names")
}

func TestTarget(t *testing.T) {
	cfg, err := config.Parse([]byte(`
[args.NODE_VERSION]
  datasource = "node-version"
  lts        = true
  target     = 24
[images.golang]
  image  = "golang"
  target = "1.26"
[packages.pnpm]
  target = "10"
[packages.npm]
`))
	require.NoError(t, err)
	assert.Equal(t, config.Target("24"), cfg.Args["NODE_VERSION"].Target, "an integer is accepted")
	assert.Equal(t, config.Target("1.26"), cfg.TargetOf(config.SectionImages, "golang"))
	assert.Equal(t, config.Target("10"), cfg.TargetOf(config.SectionPackages, "pnpm"))
	assert.Equal(t, config.Target(""), cfg.TargetOf(config.SectionPackages, "npm"))
	line, ok := cfg.Args["NODE_VERSION"].Target.Line()
	require.True(t, ok)
	assert.Equal(t, "24", line.String())
	_, ok = cfg.Packages["npm"].Target.Line()
	assert.False(t, ok)

	for src, want := range map[string]string{
		"[args.X]\n  datasource = \"npm\"\n  package = \"x\"\n  target = 1.26\n":            `quote a line with a dot, e.g. target = "1.26"`,
		"[args.X]\n  datasource = \"npm\"\n  package = \"x\"\n  target = \"lts\"\n":         `target must be a version line such as "24" or "1.26" (got "lts")`,
		"[images.x]\n  image = \"golang\"\n  target = \"1.26\"\n  fixed-tag = true\n":       "fixed-tag and target can't both be set",
		"[packages.p]\n  target = true\n":                                                   "target must be a version line",
		"[args.S]\n  datasource = \"github-commits\"\n  repo = \"a/b\"\n  target = \"1\"\n": `target needs track = "tags"`,
	} {
		_, err := config.Parse([]byte(src))
		require.Error(t, err, src)
		assert.Contains(t, err.Error(), want, src)
	}
}
