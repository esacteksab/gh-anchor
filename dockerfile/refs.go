// SPDX-License-Identifier: MIT

package dockerfile

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Kind identifies what a Ref points at.
type Kind int

const (
	// KindImage is an image reference in `FROM` or `COPY --from=`.
	KindImage Kind = iota
	// KindArg is the default value of an `ARG`.
	KindArg
	// KindPackage is an inline `name@version` passed to a JavaScript package manager in `RUN`.
	KindPackage
)

func (k Kind) String() string {
	switch k {
	case KindImage:
		return "image"
	case KindArg:
		return "arg"
	case KindPackage:
		return "package"
	default:
		return "unknown"
	}
}

// Ref is an updatable value found in a Dockerfile.
type Ref struct {
	Kind  Kind
	Instr string // Instruction keyword: FROM, COPY, ARG or RUN
	Line  int    // 1-based line of the value
	Name  string // ARG name or package name; for images, the reference as written
	Value string // Current value: the image reference, ARG default or package version

	// Key identifies the config table for images and packages:
	//   - FROM: the build stage name, "final" for an unnamed last stage, or
	//     "stage-N" (Docker's stage index) for another unnamed stage;
	//   - COPY --from: the image repository as written, e.g. "ghcr.io/owner/tool";
	//   - RUN: the package name.
	Key string
	// Manager is the command a package was passed to, e.g. "npm" or "corepack".
	Manager string

	// Start and End are the byte range of Value in the source.
	Start, End int

	Stage      string // FROM: the stage name (AS name) as written, "" if unnamed
	stageIndex int    // FROM: Docker's stage index
}

// FinalStageKey is the config key of an unnamed last build stage.
const FinalStageKey = "final"

// Skip records a candidate that was deliberately ignored.
type Skip struct {
	Line   int
	Instr  string // Instruction keyword: FROM, COPY, ARG or RUN
	Text   string
	Reason string
}

// Skip reasons.
const (
	ReasonContinuation = "value is split across a line continuation"
	ReasonVariable     = "image uses a build argument"
	// The following are not references at all, so they are not counted as skipped.
	ReasonStage     = "refers to an earlier build stage"
	ReasonScratch   = "scratch is not an image"
	ReasonNoDefault = "ARG has no default value"
)

// packageManagers are the commands after which `name@version` words are treated as npm packages.
var packageManagers = map[string]bool{
	"npm": true, "npx": true, "pnpm": true, "pnpx": true, "yarn": true,
	"corepack": true, "bun": true, "bunx": true,
}

// pkgSpecRe matches npm package specs with a version or dist-tag: `pnpm@12.16.1`, `@scope/pkg@1`, `corepack@latest`.
var pkgSpecRe = regexp.MustCompile(
	`^((?:@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*)@([0-9A-Za-z][0-9A-Za-z.+_-]*)$`,
)

// shellSeparators end the current command within a RUN instruction.
var shellSeparators = map[string]bool{"&&": true, "||": true, ";": true, "|": true, "&": true}

// Extract finds every updatable value in src.
func Extract(src string) ([]Ref, []Skip) {
	var refs []Ref
	var skips []Skip
	stages := map[string]bool{}

	instructions := Parse(src)
	stageIndex := -1
	for i := range instructions {
		in := &instructions[i]
		var r []Ref
		var s []Skip
		switch in.Cmd {
		case "FROM":
			stageIndex++
			r, s = extractFrom(src, in, stages)
			for j := range r {
				r[j].stageIndex = stageIndex
			}
		case "COPY":
			r, s = extractCopy(src, in, stages)
		case "ARG":
			r, s = extractArg(src, in)
		case "RUN":
			r, s = extractRun(src, in)
		}
		refs = append(refs, r...)
		skips = append(skips, s...)
	}
	for i := range refs {
		refs[i].Key = refKey(refs[i], stageIndex)
	}
	return refs, skips
}

// refKey returns the config table key of a reference; lastStage is the index of the
// last FROM in the file.
func refKey(r Ref, lastStage int) string {
	switch {
	case r.Kind == KindPackage:
		return r.Name
	case r.Kind != KindImage:
		return ""
	case r.Instr != "FROM":
		return Repository(r.Value)
	case r.Stage != "":
		return r.Stage
	case r.stageIndex == lastStage:
		return FinalStageKey
	default:
		return "stage-" + strconv.Itoa(r.stageIndex)
	}
}

// Repository returns an image reference without its tag and digest, as written:
// "ghcr.io/owner/tool:1.2.3@sha256:..." -> "ghcr.io/owner/tool".
func Repository(ref string) string {
	if at := strings.IndexByte(ref, '@'); at >= 0 {
		ref = ref[:at]
	}
	if colon := strings.LastIndexByte(ref, ':'); colon > strings.LastIndexByte(ref, '/') {
		ref = ref[:colon]
	}
	return ref
}

// lineAt returns the 1-based line number of byte offset off.
func lineAt(src string, off int) int {
	return strings.Count(src[:off], "\n") + 1
}

// makeRef maps a range of in.Args to the source and builds a Ref, or a Skip if the
// value spans a line continuation.
func makeRef(src string, in *Instruction, kind Kind, name string, start, end int) (Ref, *Skip) {
	value := in.Args[start:end]
	s, e, ok := in.SourceRange(start, end)
	if !ok {
		return Ref{}, &Skip{
			Line:   in.Line,
			Instr:  in.Cmd,
			Text:   value,
			Reason: ReasonContinuation,
		}
	}
	return Ref{
		Kind:  kind,
		Instr: in.Cmd,
		Line:  lineAt(src, s),
		Name:  name,
		Value: value,
		Start: s,
		End:   e,
	}, nil
}

func extractFrom(src string, in *Instruction, stages map[string]bool) ([]Ref, []Skip) {
	words := Words(in.Args)
	var image *Word
	stage := ""
	for idx := range words {
		w := words[idx]
		if strings.HasPrefix(w.Text, "--") {
			continue
		}
		if image == nil {
			image = &words[idx]
			continue
		}
		if strings.EqualFold(w.Text, "AS") && idx+1 < len(words) {
			stage = words[idx+1].Text
			stages[strings.ToLower(stage)] = true
			break
		}
	}
	if image == nil {
		return nil, nil
	}
	text := image.Text
	switch {
	case strings.Contains(text, "$"):
		return nil, []Skip{{Line: in.Line, Instr: in.Cmd, Text: text, Reason: ReasonVariable}}
	case strings.EqualFold(text, "scratch"):
		return nil, []Skip{{Line: in.Line, Instr: in.Cmd, Text: text, Reason: ReasonScratch}}
	case stages[strings.ToLower(text)]:
		return nil, []Skip{{Line: in.Line, Instr: in.Cmd, Text: text, Reason: ReasonStage}}
	}
	ref, skip := makeRef(src, in, KindImage, text, image.Start, image.End)
	if skip != nil {
		return nil, []Skip{*skip}
	}
	ref.Stage = stage
	return []Ref{ref}, nil
}

func extractCopy(src string, in *Instruction, stages map[string]bool) ([]Ref, []Skip) {
	const flag = "--from="
	for _, w := range Words(in.Args) {
		if !strings.HasPrefix(w.Text, "--") {
			break // flags come first
		}
		if !strings.HasPrefix(strings.ToLower(w.Text), flag) {
			continue
		}
		text := w.Text[len(flag):]
		switch {
		case text == "":
			return nil, nil
		case strings.Contains(text, "$"):
			return nil, []Skip{{Line: in.Line, Instr: in.Cmd, Text: text, Reason: ReasonVariable}}
		case stages[strings.ToLower(text)], isDigits(text): // stage name or index
			return nil, []Skip{{Line: in.Line, Instr: in.Cmd, Text: text, Reason: ReasonStage}}
		}
		ref, skip := makeRef(src, in, KindImage, text, w.Start+len(flag), w.End)
		if skip != nil {
			return nil, []Skip{*skip}
		}
		return []Ref{ref}, nil
	}
	return nil, nil
}

func extractArg(src string, in *Instruction) ([]Ref, []Skip) {
	var refs []Ref
	var skips []Skip
	for _, w := range Words(in.Args) {
		eq := strings.IndexByte(w.Text, '=')
		if eq <= 0 {
			skips = append(
				skips,
				Skip{Line: in.Line, Instr: in.Cmd, Text: w.Text, Reason: ReasonNoDefault},
			)
			continue
		}
		name := w.Text[:eq]
		start, end := w.Start+eq+1, w.End
		value := in.Args[start:end]
		if n := len(value); n >= 2 && (value[0] == '"' || value[0] == '\'') &&
			value[n-1] == value[0] {
			start++
			end--
		}
		if start >= end {
			skips = append(
				skips,
				Skip{Line: in.Line, Instr: in.Cmd, Text: name, Reason: ReasonNoDefault},
			)
			continue
		}
		ref, skip := makeRef(src, in, KindArg, name, start, end)
		if skip != nil {
			skips = append(skips, *skip)
			continue
		}
		refs = append(refs, ref)
	}
	return refs, skips
}

func extractRun(src string, in *Instruction) ([]Ref, []Skip) {
	var refs []Ref
	var skips []Skip
	if strings.HasPrefix(strings.TrimSpace(in.Args), "[") {
		return nil, nil // exec form
	}
	manager := ""
	for _, w := range Words(in.Args) {
		text := w.Text
		end := w.End
		resetAfter := false
		if shellSeparators[text] {
			manager = ""
			continue
		}
		if before, ok := strings.CutSuffix(text, ";"); ok {
			text = before
			end--
			resetAfter = true
		}
		base := text[strings.LastIndexByte(text, '/')+1:]
		if packageManagers[base] {
			manager = base
		} else if manager != "" {
			if m := pkgSpecRe.FindStringSubmatch(text); m != nil {
				name := m[1]
				start := w.Start + len(name) + 1
				ref, skip := makeRef(src, in, KindPackage, name, start, end)
				if skip != nil {
					skips = append(skips, *skip)
				} else {
					ref.Manager = manager
					refs = append(refs, ref)
				}
			}
		}
		if resetAfter {
			manager = ""
		}
	}
	return refs, skips
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// Edit replaces the source bytes [Start, End) with New.
type Edit struct {
	Start, End int
	New        string
}

// ErrOverlap is returned by Apply when two edits overlap.
var ErrOverlap = errors.New("overlapping edits")

// Apply applies edits to src. Edits must not overlap.
func Apply(src string, edits []Edit) (string, error) {
	sorted := append([]Edit(nil), edits...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	var b strings.Builder
	pos := 0
	for _, e := range sorted {
		if e.Start < pos || e.End < e.Start || e.End > len(src) {
			return "", fmt.Errorf("%w at byte %d", ErrOverlap, e.Start)
		}
		b.WriteString(src[pos:e.Start])
		b.WriteString(e.New)
		pos = e.End
	}
	b.WriteString(src[pos:])
	return b.String(), nil
}
