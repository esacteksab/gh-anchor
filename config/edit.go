// SPDX-License-Identifier: MIT

package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Set returns src with field = "value" in the table named by path, such as
// ["images", "python"]. It edits the text in place, so comments, ordering and
// alignment are kept: an existing value is replaced (keeping a trailing comment), and a
// missing field is added after the table's last key, aligned like the keys around it.
// The table must be written as a [header]; dotted keys and inline tables aren't edited.
func Set(src string, path []string, field, value string) (string, error) {
	lines := strings.SplitAfter(src, "\n")
	start, end := findTable(lines, path)
	if start < 0 {
		return "", fmt.Errorf("no [%s] table to record %s in (it must be written as a [table])",
			TableName(path...), field)
	}
	quoted := quote(value)
	last := start
	// Keys are aligned when any of them is padded before "=".
	align, padded := 0, false
	for i := start + 1; i < end; i++ {
		key, eq, valueStart, ok := keyValue(lines[i])
		if !ok {
			continue
		}
		last = i
		align = max(align, eq)
		padded = padded || eq > len(key)+1
		if key != field {
			continue
		}
		valueEnd, err := stringEnd(lines[i], valueStart)
		if err != nil {
			return "", fmt.Errorf("[%s] %s: %w", TableName(path...), field, err)
		}
		lines[i] = lines[i][:valueStart] + quoted + lines[i][valueEnd:]
		return strings.Join(lines, ""), nil
	}

	pad := " "
	if padded && align > len(field) {
		pad = strings.Repeat(" ", align-len(field))
	}
	indent := leadingSpace(lines[last])
	if last == start {
		indent = ""
	}
	line := indent + field + pad + "= " + quoted + "\n"
	if !strings.HasSuffix(lines[last], "\n") {
		lines[last] += "\n"
	}
	lines = append(lines[:last+1], append([]string{line}, lines[last+1:]...)...)
	return strings.Join(lines, ""), nil
}

// findTable returns the index of the header line of the table named by path and the
// index of the line after its last line (the next header or the end), or -1.
func findTable(lines, path []string) (int, int) {
	start := -1
	for i, line := range lines {
		name, ok := header(line)
		if !ok {
			continue
		}
		if start >= 0 {
			return start, i
		}
		if equalKeys(name, path) {
			start = i
		}
	}
	return start, len(lines)
}

func equalKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// header parses a [table] or [[array]] header line. Array headers are returned with
// a nil name, so they end the previous table without matching any path.
func header(line string) ([]string, bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "[") {
		return nil, false
	}
	if strings.HasPrefix(s, "[[") {
		return nil, true
	}
	keys, rest, err := parseKey(s[1:])
	if err != nil {
		return nil, false
	}
	rest = strings.TrimLeft(rest, " \t")
	if !strings.HasPrefix(rest, "]") {
		return nil, false
	}
	return keys, true
}

var bareRe = regexp.MustCompile(`^[A-Za-z0-9_-]+`)

// parseKey parses a dotted TOML key and returns its parts and the rest of s.
func parseKey(s string) ([]string, string, error) {
	var keys []string
	for {
		s = strings.TrimLeft(s, " \t")
		var key string
		switch {
		case strings.HasPrefix(s, `"`):
			end, err := stringEnd(s, 0)
			if err != nil {
				return nil, "", err
			}
			key, err = unquote(s[1 : end-1])
			if err != nil {
				return nil, "", err
			}
			s = s[end:]
		case strings.HasPrefix(s, "'"):
			end := strings.IndexByte(s[1:], '\'')
			if end < 0 {
				return nil, "", errUnterminated
			}
			key, s = s[1:end+1], s[end+2:]
		default:
			key = bareRe.FindString(s)
			if key == "" {
				return nil, "", errors.New("expected a key")
			}
			s = s[len(key):]
		}
		keys = append(keys, key)
		rest := strings.TrimLeft(s, " \t")
		if !strings.HasPrefix(rest, ".") {
			return keys, s, nil
		}
		s = rest[1:]
	}
}

// keyValue parses a "key = value" line with a bare key. It returns the key, the
// offset of "=" and the offset where the value starts.
func keyValue(line string) (string, int, int, bool) {
	indent := len(leadingSpace(line))
	key := bareRe.FindString(line[indent:])
	if key == "" {
		return "", 0, 0, false
	}
	eq := indent + len(key)
	eq += len(line[eq:]) - len(strings.TrimLeft(line[eq:], " \t"))
	if eq >= len(line) || line[eq] != '=' {
		return "", 0, 0, false
	}
	valueStart := eq + 1
	valueStart += len(line[valueStart:]) - len(strings.TrimLeft(line[valueStart:], " \t"))
	return key, eq - indent, valueStart, true
}

var errUnterminated = errors.New("unterminated string")

// stringEnd returns the offset just past the single-line string that starts at
// line[i], which must be a basic ("…") or literal ('…') string.
func stringEnd(line string, i int) (int, error) {
	if strings.HasPrefix(line[i:], `"""`) || strings.HasPrefix(line[i:], "'''") {
		return 0, errors.New("multi-line strings can't be updated; use a single-line string")
	}
	if i >= len(line) || (line[i] != '"' && line[i] != '\'') {
		return 0, errors.New("the value isn't a string")
	}
	q := line[i]
	for j := i + 1; j < len(line); j++ {
		switch {
		case q == '"' && line[j] == '\\':
			j++
		case line[j] == q:
			return j + 1, nil
		case line[j] == '\n':
			return 0, errUnterminated
		}
	}
	return 0, errUnterminated
}

// unquote decodes the escapes allowed in the keys gh-anchor reads (\" and \\).
func unquote(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) || (s[i] != '"' && s[i] != '\\') {
			return "", errors.New("unsupported escape in key")
		}
		b.WriteByte(s[i])
	}
	return b.String(), nil
}

// quote returns value as a TOML basic string.
func quote(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < ' ' || r == '\x7f':
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// TableName renders a dotted key such as images."ghcr.io/o/r", quoting the parts
// that aren't bare keys.
func TableName(path ...string) string {
	parts := make([]string, len(path))
	for i, p := range path {
		if bareRe.FindString(p) == p && p != "" {
			parts[i] = p
		} else {
			parts[i] = quote(p)
		}
	}
	return strings.Join(parts, ".")
}

func leadingSpace(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}
