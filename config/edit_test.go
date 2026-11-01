// SPDX-License-Identifier: MIT

package config_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/esacteksab/gh-anchor/config"
)

const editSrc = `# Recorded by gh-anchor.
[images.final]
  image     = "ubuntu"
  tag       = "24.04"   # the LTS we ship
  digest    = ""
  lts       = true

[images."ghcr.io/astral-sh/uv"]
image = 'ghcr.io/astral-sh/uv'
fixed-tag = false
tag = "0.13.6"

[ packages . "pnpm" ]
  version = "12.16.1"
  skip    = false

[[unrelated]]
  tag = "keep"
`

func TestSet(t *testing.T) {
	tests := map[string]struct {
		path         []string
		field, value string
		want         string // the changed line(s)
		was          string // the line(s) it replaces
	}{
		"replace keeps alignment and comment": {
			[]string{"images", "final"},
			"tag", "26.04",
			`  tag       = "26.04"   # the LTS we ship` + "\n", `  tag       = "24.04"   # the LTS we ship` + "\n",
		},
		"replace an empty value": {
			[]string{"images", "final"},
			"digest", "sha256:abc",
			`  digest    = "sha256:abc"` + "\n", `  digest    = ""` + "\n",
		},
		"replace a literal string": {
			[]string{"images", "ghcr.io/astral-sh/uv"},
			"image", "ghcr.io/astral-sh/uv",
			`image = "ghcr.io/astral-sh/uv"` + "\n", `image = 'ghcr.io/astral-sh/uv'` + "\n",
		},
		"insert aligned after the last key": {
			[]string{"images", "final"},
			"fixed-tag", "x",
			"  lts       = true\n  fixed-tag = \"x\"\n", "  lts       = true\n",
		},
		"insert unaligned": {
			[]string{"images", "ghcr.io/astral-sh/uv"},
			"digest", "sha256:abc",
			"tag = \"0.13.6\"\ndigest = \"sha256:abc\"\n", "tag = \"0.13.6\"\n",
		},
		"spaced and quoted header": {
			[]string{"packages", "pnpm"},
			"hash", "sha512.0a1b",
			"  skip    = false\n  hash    = \"sha512.0a1b\"\n", "  skip    = false\n",
		},
		"escapes quotes": {
			[]string{"packages", "pnpm"},
			"version", `a"b\c`,
			`  version = "a\"b\\c"` + "\n", `  version = "12.16.1"` + "\n",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := config.Set(editSrc, tt.path, tt.field, tt.value)
			require.NoError(t, err)
			require.Contains(t, editSrc, tt.was)
			assert.Equal(t, strings.Replace(editSrc, tt.was, tt.want, 1), got)
		})
	}
}

func TestSetResultParses(t *testing.T) {
	src := "[images.final]\n  image = \"ubuntu\"\n  tag = \"24.04\"\n\n[packages.pnpm]\n  version = \"12.16.1\""
	out, err := config.Set(src, []string{"packages", "pnpm"}, "hash", "sha512.0a1b")
	require.NoError(t, err)
	out, err = config.Set(out, []string{"images", "final"}, "digest",
		"sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852")
	require.NoError(t, err)
	cfg, err := config.Parse([]byte(out))
	require.NoError(t, err)
	got, _ := cfg.Recorded(config.SectionPackages, "pnpm", "hash")
	assert.Equal(t, "sha512.0a1b", got)
	got, _ = cfg.Recorded(config.SectionImages, "final", "digest")
	assert.Equal(t, "sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852", got)
	got, _ = cfg.Recorded(config.SectionImages, "final", "tag")
	assert.Equal(t, "24.04", got)

	// A table with only a header gets the field right below it.
	out, err = config.Set("[images.x]\n", []string{"images", "x"}, "image", "ubuntu")
	require.NoError(t, err)
	assert.Equal(t, "[images.x]\nimage = \"ubuntu\"\n", out)
}

func TestSetErrors(t *testing.T) {
	tests := map[string]struct{ src, want string }{
		"no table":     {"[images.other]\n  tag = \"1\"\n", "no [images.final] table"},
		"dotted keys":  {"[images]\n  final.tag = \"1\"\n", "no [images.final] table"},
		"not a string": {"[images.final]\n  tag = 1\n", "isn't a string"},
		"multi-line":   {"[images.final]\n  tag = \"\"\"1\"\"\"\n", "multi-line"},
		"unterminated": {"[images.final]\n  tag = \"1\n", "unterminated"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := config.Set(tt.src, []string{"images", "final"}, "tag", "2")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestTableName(t *testing.T) {
	assert.Equal(t, "images.final", config.TableName("images", "final"))
	assert.Equal(t, `images."ghcr.io/o/r"`, config.TableName("images", "ghcr.io/o/r"))
	assert.Equal(t, `packages."@scope/pkg"`, config.TableName("packages", "@scope/pkg"))
}
