// SPDX-License-Identifier: MIT

// Package versioning parses loosely semver-shaped version strings (npm versions,
// Docker image tags, Git tags) and selects update candidates according to the
// gh-anchor update policy.
//
// A version is split into three parts:
//
//	v1.26.10-alpine
//	^ ^^^^^^^ ^^^^^^^
//	| |       suffix  ("-alpine")
//	| numbers ([1 26 10])
//	prefix ("v")
//
// Two versions have the same "shape" when prefix, number of numeric components
// and suffix all match. Update candidates must share the current version's shape,
// so `1.26.10` never moves to `1.27.0-alpine`, `1.27` or `1.27.0-rc1`.
package versioning

import (
	"regexp"
	"strconv"
	"strings"
)

// versionRe captures an optional "v" prefix, dot-separated numeric components and any suffix.
var versionRe = regexp.MustCompile(`^([vV]?)(\d+(?:\.\d+)*)(.*)$`)

// maxComponent guards against parsing very long digit strings (e.g. dates or hashes) as numbers.
const maxComponentDigits = 18

// Version is a parsed version string.
type Version struct {
	Raw    string // The original string, e.g. "v2.15.1"
	Prefix string // "v", "V" or ""
	Nums   []int  // Numeric components, e.g. [2 15 1]
	Suffix string // Everything after the numeric components, e.g. "-alpine"
}

// Parse parses s into a Version. It returns false if s does not start with a number
// (optionally prefixed by "v").
func Parse(s string) (Version, bool) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return Version{}, false
	}
	parts := strings.Split(m[2], ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		if len(p) > maxComponentDigits {
			return Version{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return Version{}, false
		}
		nums = append(nums, n)
	}
	return Version{Raw: s, Prefix: m[1], Nums: nums, Suffix: m[3]}, true
}

// Major returns the first numeric component.
func (v Version) Major() int {
	if len(v.Nums) == 0 {
		return 0
	}
	return v.Nums[0]
}

// IsStable reports whether the version has no suffix (no pre-release or build metadata).
func (v Version) IsStable() bool { return v.Suffix == "" }

// SameShape reports whether o has the same prefix, number of components and suffix as v.
// When ignorePrefix is true, a "v" prefix difference is tolerated.
func (v Version) SameShape(o Version, ignorePrefix bool) bool {
	if !ignorePrefix && !strings.EqualFold(v.Prefix, o.Prefix) {
		return false
	}
	return len(v.Nums) == len(o.Nums) && v.Suffix == o.Suffix
}

// Compare compares the numeric components of a and b. Missing components count as zero.
// It returns -1, 0 or +1.
func Compare(a, b Version) int {
	n := max(len(a.Nums), len(b.Nums))
	for i := range n {
		var x, y int
		if i < len(a.Nums) {
			x = a.Nums[i]
		}
		if i < len(b.Nums) {
			y = b.Nums[i]
		}
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	return 0
}

// HasNumericPrefix reports whether v's numeric components start with prefix.
func (v Version) HasNumericPrefix(prefix []int) bool {
	if len(prefix) > len(v.Nums) {
		return false
	}
	for i, n := range prefix {
		if v.Nums[i] != n {
			return false
		}
	}
	return true
}

// Render returns o formatted with v's prefix. Used when a datasource reports versions
// with a "v" (e.g. Node.js "v22.1.0") but the Dockerfile stores them without.
func (v Version) Render(o Version) string {
	if v.Prefix == o.Prefix {
		return o.Raw
	}
	return v.Prefix + strings.TrimPrefix(o.Raw, o.Prefix)
}

// Newest returns the newest candidate that has the same shape as current and is
// strictly newer than it. When allowMajor is false the candidate must also share
// current's major component. The returned string is rendered with current's prefix.
func Newest(current Version, candidates []string, allowMajor, ignorePrefix bool) (string, bool) {
	best := current
	found := false
	for _, c := range candidates {
		cv, ok := Parse(c)
		if !ok || !current.SameShape(cv, ignorePrefix) {
			continue
		}
		if !allowMajor && cv.Major() != current.Major() {
			continue
		}
		if Compare(cv, best) > 0 {
			best = cv
			found = true
		}
	}
	if !found {
		return "", false
	}
	return current.Render(best), true
}

// NewestMatching returns the newest stable candidate whose numeric components start with
// spec's numbers (e.g. spec "22" matches "22.23.4"). It is used to pin a floating
// version such as `22` or `12.16` to an exact release. wantLen, when > 0, restricts
// candidates to that many numeric components.
func NewestMatching(
	spec Version,
	candidates []string,
	wantLen int,
	ignorePrefix bool,
) (string, bool) {
	var best Version
	found := false
	for _, c := range candidates {
		cv, ok := Parse(c)
		if !ok || !cv.IsStable() {
			continue
		}
		if !ignorePrefix && !strings.EqualFold(spec.Prefix, cv.Prefix) {
			continue
		}
		if wantLen > 0 && len(cv.Nums) != wantLen {
			continue
		}
		if !cv.HasNumericPrefix(spec.Nums) {
			continue
		}
		if !found || Compare(cv, best) > 0 {
			best = cv
			found = true
		}
	}
	if !found {
		return "", false
	}
	return spec.Render(best), true
}

// NewestStable returns the newest stable candidate regardless of shape. It is used when
// there is no current version to compare against (e.g. a commit that is not tagged).
func NewestStable(candidates []string) (string, bool) {
	var best Version
	found := false
	for _, c := range candidates {
		cv, ok := Parse(c)
		if !ok || !cv.IsStable() {
			continue
		}
		if !found || Compare(cv, best) > 0 {
			best = cv
			found = true
		}
	}
	return best.Raw, found
}

// Contains reports whether value is present in candidates, optionally ignoring a "v" prefix.
func Contains(value string, candidates []string, ignorePrefix bool) bool {
	norm := func(s string) string {
		if ignorePrefix {
			return strings.TrimLeft(s, "vV")
		}
		return s
	}
	want := norm(value)
	for _, c := range candidates {
		if norm(c) == want {
			return true
		}
	}
	return false
}

// Line is a version line such as 24 or 1.26: the leading numeric components a
// version must start with.
type Line []int

// ParseLine parses a line such as "24", "1.26" or "v2".
func ParseLine(s string) (Line, bool) {
	v, ok := Parse(strings.TrimSpace(s))
	if !ok || v.Suffix != "" {
		return nil, false
	}
	return Line(v.Nums), true
}

// String formats the line, e.g. "1.26".
func (l Line) String() string {
	parts := make([]string, len(l))
	for i, n := range l {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

// Contains reports whether v is in the line.
func (l Line) Contains(v Version) bool { return v.HasNumericPrefix(l) }

// compareNums compares two equally long lists of numbers.
func compareNums(a, b []int) int {
	for i := range a {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}

// NewestInLine returns the newest candidate with current's shape in line, even when it
// is older than current (the line is an explicit choice). With atOrAbove, when the line
// has no candidate, the lowest line above it that has one is used: given only LTS
// releases, target 25 moves to the 26 line. The result is rendered with current's prefix.
func NewestInLine(
	current Version,
	candidates []string,
	line Line,
	atOrAbove, ignorePrefix bool,
) (string, bool) {
	var best Version
	var bestKey []int
	found := false
	for _, c := range candidates {
		cv, ok := Parse(c)
		if !ok || !current.SameShape(cv, ignorePrefix) || len(cv.Nums) < len(line) {
			continue
		}
		key := cv.Nums[:len(line)]
		if cmp := compareNums(key, line); cmp < 0 || (cmp > 0 && !atOrAbove) {
			continue
		}
		// The lowest line wins (the line itself when it has candidates), then the newest in it.
		if !found || compareNums(key, bestKey) < 0 ||
			(compareNums(key, bestKey) == 0 && Compare(cv, best) > 0) {
			best, bestKey, found = cv, key, true
		}
	}
	if !found {
		return "", false
	}
	return current.Render(best), true
}
