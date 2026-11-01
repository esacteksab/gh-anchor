// SPDX-License-Identifier: MIT

// Package dockerfile splits a Dockerfile into instructions while keeping a map from
// every character of an instruction's logical text back to its byte offset in the
// original file. That lets gh-anchor rewrite a single token (an image reference, an
// ARG value, a package@version) in place, without reformatting anything else.
//
// It understands the parts of the Dockerfile syntax that affect where an instruction
// starts and ends: the `# escape=` parser directive, line continuations, comment and
// empty lines inside continuations, and heredocs (`RUN <<EOF ... EOF`).
package dockerfile

import (
	"regexp"
	"strings"
)

// Instruction is one Dockerfile instruction (e.g. FROM, ARG, RUN).
type Instruction struct {
	Cmd   string // Upper-case keyword, e.g. "FROM"
	Line  int    // 1-based line number of the keyword
	Start int    // Byte offset of the keyword in the source
	End   int    // Byte offset just past the instruction (including any heredoc bodies)

	// Args is the logical text following the keyword: continuation lines are joined,
	// escape characters and comment lines removed. Heredoc bodies are not included.
	Args string
	// offsets[i] is the byte offset in the source of Args[i].
	offsets []int
}

// SourceRange maps the half-open range [start, end) of Args back to the source.
// ok is false when the range spans a line continuation and therefore is not a
// contiguous run of bytes in the source.
func (in *Instruction) SourceRange(start, end int) (srcStart, srcEnd int, ok bool) {
	if start < 0 || end > len(in.offsets) || start >= end {
		return 0, 0, false
	}
	srcStart = in.offsets[start]
	srcEnd = in.offsets[end-1] + 1
	return srcStart, srcEnd, srcEnd-srcStart == end-start
}

// line is a physical line of the source.
type line struct {
	start, end int // byte offsets; end excludes the newline and any trailing '\r'
	next       int // offset of the following line
}

// directiveRe matches parser directives such as `# escape=“ ` or `# syntax=docker/dockerfile:1`.
var directiveRe = regexp.MustCompile(`^#\s*([a-zA-Z][a-zA-Z0-9]*)\s*=\s*(.+?)\s*$`)

// heredocRe matches a heredoc word such as `<<EOF`, `<<-EOT`, `<<"EOF"` or `3<<EOF`.
var heredocRe = regexp.MustCompile(`^\d*<<(-?)(.+)$`)

// heredocCommands are the instructions that accept heredocs.
var heredocCommands = map[string]bool{"RUN": true, "COPY": true, "ADD": true}

func splitLines(src string) []line {
	var lines []line
	for pos := 0; pos < len(src); {
		nl := strings.IndexByte(src[pos:], '\n')
		var end, next int
		if nl < 0 {
			end, next = len(src), len(src)
		} else {
			end, next = pos+nl, pos+nl+1
		}
		if end > pos && src[end-1] == '\r' {
			end--
		}
		lines = append(lines, line{start: pos, end: end, next: next})
		pos = next
	}
	return lines
}

// leftTrim returns the offset of the first non-blank byte of s[start:end].
func leftTrim(src string, start, end int) int {
	for start < end && (src[start] == ' ' || src[start] == '\t') {
		start++
	}
	return start
}

// rightTrim returns the offset just past the last non-blank byte of s[start:end].
func rightTrim(src string, start, end int) int {
	for end > start && (src[end-1] == ' ' || src[end-1] == '\t') {
		end--
	}
	return end
}

// Parse splits src into instructions.
func Parse(src string) []Instruction {
	lines := splitLines(src)
	escape := byte('\\')

	i := 0
	// Parser directives must appear before anything else, one per line.
	for ; i < len(lines); i++ {
		l := lines[i]
		m := directiveRe.FindStringSubmatch(src[l.start:l.end])
		if m == nil {
			break
		}
		if strings.EqualFold(m[1], "escape") && (m[2] == "`" || m[2] == `\`) {
			escape = m[2][0]
		}
	}

	var out []Instruction
	for i < len(lines) {
		l := lines[i]
		s := leftTrim(src, l.start, l.end)
		if s == l.end || src[s] == '#' { // blank or comment
			i++
			continue
		}
		in, consumed := parseInstruction(src, lines, i, s, escape)
		out = append(out, in)
		i += consumed
	}
	return out
}

// parseInstruction reads one instruction starting at lines[i] (keyword at offset kw).
// It returns the instruction and the number of physical lines consumed.
func parseInstruction(src string, lines []line, i, kw int, escape byte) (Instruction, int) {
	l := lines[i]
	kwEnd := kw
	for kwEnd < l.end && !isBlank(src[kwEnd]) {
		kwEnd++
	}
	in := Instruction{Cmd: strings.ToUpper(src[kw:kwEnd]), Line: i + 1, Start: kw}

	j := in.readLogicalLine(src, lines, i, kwEnd, escape)
	if heredocCommands[in.Cmd] {
		for _, w := range Words(in.Args) {
			if m := heredocRe.FindStringSubmatch(w.Text); m != nil {
				j = in.readHeredoc(src, lines, j, m[1] == "-", strings.Trim(m[2], `"'`))
			}
		}
	}
	return in, j - i
}

// readLogicalLine joins the instruction's physical lines, starting at lines[i] just after
// the keyword, into in.Args and in.offsets. It returns the index of the next line.
func (in *Instruction) readLogicalLine(src string, lines []line, i, segStart int, escape byte) int {
	var args strings.Builder
	var offsets []int
	appendRange := func(start, end int) {
		args.WriteString(src[start:end])
		for o := start; o < end; o++ {
			offsets = append(offsets, o)
		}
	}

	j := i
	for j < len(lines) {
		cur := lines[j]
		trimmed := rightTrim(src, segStart, cur.end)
		in.End = cur.end
		j++
		if trimmed == segStart || src[trimmed-1] != escape {
			appendRange(segStart, cur.end)
			break
		}
		appendRange(segStart, trimmed-1)
		j = skipBlankAndComments(src, lines, j)
		if j < len(lines) {
			segStart = lines[j].start
		}
	}

	in.Args = strings.TrimLeft(args.String(), " \t")
	in.offsets = offsets[len(offsets)-len(in.Args):]
	return j
}

// skipBlankAndComments returns the index of the first line at or after j that is
// neither empty nor a comment. Such lines do not end a line continuation.
func skipBlankAndComments(src string, lines []line, j int) int {
	for j < len(lines) {
		s := leftTrim(src, lines[j].start, lines[j].end)
		if s != lines[j].end && src[s] != '#' {
			break
		}
		j++
	}
	return j
}

// readHeredoc consumes a heredoc body starting at lines[j] through the line holding
// delim, extending in.End. It returns the index of the next line.
func (in *Instruction) readHeredoc(
	src string,
	lines []line,
	j int,
	stripTabs bool,
	delim string,
) int {
	for j < len(lines) {
		hl := lines[j]
		body := src[hl.start:hl.end]
		if stripTabs {
			body = strings.TrimLeft(body, "\t")
		}
		j++
		in.End = hl.end
		if body == delim {
			break
		}
	}
	return j
}

// Word is a whitespace-separated token of an instruction's Args.
type Word struct {
	Text       string // Raw text including any quotes
	Start, End int    // Half-open range within Args
}

// Words splits s on unquoted blanks. Quotes are kept in Text.
func Words(s string) []Word {
	var words []Word
	for i := 0; i < len(s); {
		if isBlank(s[i]) {
			i++
			continue
		}
		end := wordEnd(s, i)
		words = append(words, Word{Text: s[i:end], Start: i, End: end})
		i = end
	}
	return words
}

// wordEnd returns the index just past the word starting at s[i], honoring quotes
// and backslash escapes.
func wordEnd(s string, i int) int {
	var quote byte
	for ; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' && i+1 < len(s) {
				i++
			} else if c == quote {
				quote = 0
			}
		case isBlank(c):
			return i
		case c == '"' || c == '\'':
			quote = c
		case c == '\\' && i+1 < len(s):
			i++
		}
	}
	return len(s)
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' || c == '\n' }
