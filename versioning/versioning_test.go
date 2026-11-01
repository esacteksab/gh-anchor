// SPDX-License-Identifier: MIT

package versioning

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	tests := []struct {
		in     string
		ok     bool
		prefix string
		nums   []int
		suffix string
	}{
		{"1.26.10", true, "", []int{1, 26, 10}, ""},
		{"v2.15.1", true, "v", []int{2, 15, 1}, ""},
		{"24.04", true, "", []int{24, 4}, ""},
		{"1.27.3-alpine3.22", true, "", []int{1, 27, 3}, "-alpine3.22"},
		{"12.3.1-beta.0", true, "", []int{12, 3, 1}, "-beta.0"},
		{"1.28rc1", true, "", []int{1, 28}, "rc1"},
		{"latest", false, "", nil, ""},
		{"noble", false, "", nil, ""},
		{"1234567890123456789012", false, "", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			v, ok := Parse(tt.in)
			require.Equal(t, tt.ok, ok)
			if !ok {
				return
			}
			assert.Equal(t, tt.prefix, v.Prefix)
			assert.Equal(t, tt.nums, v.Nums)
			assert.Equal(t, tt.suffix, v.Suffix)
		})
	}
}

func TestNewest(t *testing.T) {
	golang := []string{
		"1.26.10",
		"1.26.11",
		"1.27.3",
		"1.27.3-alpine",
		"1.27",
		"1.28rc1",
		"2.0.0",
		"latest",
	}
	tests := []struct {
		name         string
		current      string
		candidates   []string
		allowMajor   bool
		ignorePrefix bool
		want         string
		wantOK       bool
	}{
		{"same major keeps shape", "1.26.10", golang, false, false, "1.27.3", true},
		{"bump allows major", "1.26.10", golang, true, false, "2.0.0", true},
		{"suffix must match", "1.27.3-alpine", golang, false, false, "", false},
		{"already newest", "1.27.3", []string{"1.26.10", "1.27.3"}, false, false, "", false},
		{
			"zero major stays zero",
			"0.13.6",
			[]string{"0.13.7", "0.14.0", "1.0.0"},
			false,
			false,
			"0.14.0",
			true,
		},
		{
			"v prefix must match",
			"v2.15.1",
			[]string{"2.16.0", "v2.15.2", "v3.0.0"},
			false,
			false,
			"v2.15.2",
			true,
		},
		{
			"node index with v prefix",
			"22.23.4",
			[]string{"v22.23.4", "v22.24.0", "v24.1.0"},
			false,
			true,
			"22.24.0",
			true,
		},
		{
			"prerelease ignored",
			"12.2.1",
			[]string{"12.3.1-beta.0", "12.3.0"},
			false,
			false,
			"12.3.0",
			true,
		},
		{
			"two components",
			"24.04",
			[]string{"24.04", "24.10", "26.04", "24.04.1"},
			true,
			false,
			"26.04",
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cur, ok := Parse(tt.current)
			require.True(t, ok)
			got, ok := Newest(cur, tt.candidates, tt.allowMajor, tt.ignorePrefix)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewestMatching(t *testing.T) {
	cands := []string{"v22.1.0", "v22.23.4", "v22.24.0", "v22.25.0-rc.1", "v24.0.0"}
	spec, _ := Parse("22")
	got, ok := NewestMatching(spec, cands, 3, true)
	require.True(t, ok)
	assert.Equal(t, "22.24.0", got)

	spec, _ = Parse("22.23")
	got, ok = NewestMatching(spec, cands, 3, true)
	require.True(t, ok)
	assert.Equal(t, "22.23.4", got)

	spec, _ = Parse("30")
	_, ok = NewestMatching(spec, cands, 3, true)
	assert.False(t, ok)
}

func TestNewestStable(t *testing.T) {
	got, ok := NewestStable([]string{"v0.7.0", "v0.7.1", "v0.8.0-beta", "nightly"})
	require.True(t, ok)
	assert.Equal(t, "v0.7.1", got)

	_, ok = NewestStable([]string{"nightly"})
	assert.False(t, ok)
}

func TestContains(t *testing.T) {
	assert.True(t, Contains("22.23.4", []string{"v22.23.4"}, true))
	assert.False(t, Contains("22.23.4", []string{"v22.23.4"}, false))
	assert.True(t, Contains("v2.15.1", []string{"v2.15.1"}, false))
}

func TestParseLine(t *testing.T) {
	for in, want := range map[string]string{"24": "24", "1.26": "1.26", "v2": "2", " 26.04 ": "26.4"} {
		l, ok := ParseLine(in)
		require.True(t, ok, in)
		assert.Equal(t, want, l.String(), in)
	}
	for _, in := range []string{"", "latest", "24-alpine", "x1"} {
		_, ok := ParseLine(in)
		assert.False(t, ok, in)
	}
	l, _ := ParseLine("1.26")
	v, _ := Parse("1.26.3")
	assert.True(t, l.Contains(v))
	v, _ = Parse("1.27.0")
	assert.False(t, l.Contains(v))
}

func TestNewestInLine(t *testing.T) {
	node := []string{
		"v26.1.0",
		"v26.0.0",
		"v25.2.0",
		"v24.12.0",
		"v24.11.1",
		"v22.24.0",
		"v22.22.3",
		"v20.19.0",
	}
	lts := []string{
		"v26.1.0",
		"v24.12.0",
		"v24.11.1",
		"v22.24.0",
		"v22.22.3",
		"v20.19.0",
	} // 25 is never LTS
	cur, _ := Parse("22.22.3")
	line := func(s string) Line { l, _ := ParseLine(s); return l }
	tests := []struct {
		name       string
		candidates []string
		line       string
		atOrAbove  bool
		want       string
		found      bool
	}{
		{"newest in 24", node, "24", false, "24.12.0", true},
		{"stays in 22", node, "22", false, "22.24.0", true},
		{"older line is allowed", node, "20", false, "20.19.0", true},
		{"25 without lts", node, "25", false, "25.2.0", true},
		{"25 with lts: next LTS line", lts, "25", true, "26.1.0", true},
		{"23 with lts: 24, not 26", lts, "23", true, "24.12.0", true},
		{"21 with lts: 22", lts, "21", true, "22.24.0", true},
		{"no line above", lts, "27", true, "", false},
		{"missing line without atOrAbove", lts, "25", false, "", false},
		{
			"minor line",
			[]string{"1.26.3", "1.26.10", "1.27.3", "1.27.3-alpine"},
			"1.26",
			false,
			"1.26.10",
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := cur
			if tt.name == "minor line" {
				c, _ = Parse("1.26.3")
			}
			got, found := NewestInLine(c, tt.candidates, line(tt.line), tt.atOrAbove, true)
			assert.Equal(t, tt.found, found)
			assert.Equal(t, tt.want, got)
		})
	}
	// Ubuntu tags: 24.04 with target 26 moves to 26.04, never 26.10 when only LTS tags are given.
	u, _ := Parse("24.04")
	got, ok := NewestInLine(u, []string{"24.04", "26.04", "26.10"}, line("26"), false, false)
	assert.True(t, ok)
	assert.Equal(t, "26.10", got, "without the LTS filter 26.10 is newest in the 26 line")
	got, _ = NewestInLine(u, []string{"24.04", "26.04"}, line("25"), true, false)
	assert.Equal(t, "26.04", got)
}
