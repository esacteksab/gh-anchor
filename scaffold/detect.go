// SPDX-License-Identifier: MIT

// Package scaffold builds a starter .gh-anchor.toml from one or more Dockerfiles.
//
// Detect works offline. It lists every image (keyed by FROM stage, or by repository
// for COPY --from) and inline package with the tag, digest, version and hash the
// Dockerfile uses. It also considers ARGs whose name contains VERSION or ends in _SHA
// or _COMMIT, and picks a datasource from the name (NODE_VERSION, NPM_VERSION, ...) or
// else from where the ARG is used (download URLs, image tags, package@version
// arguments, git remotes). Images get safe defaults (lts = true for Ubuntu and
// Node.js). Verify then confirms the suggestions against the real registries, fills in
// package hashes, and falls back to the offline suggestion with a note when a lookup
// fails. Render writes the TOML.
package scaffold

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/esacteksab/gh-anchor/config"
	"github.com/esacteksab/gh-anchor/datasource"
	"github.com/esacteksab/gh-anchor/dockerfile"
	"github.com/esacteksab/gh-anchor/registry"
	"github.com/esacteksab/gh-anchor/updater"
	"github.com/esacteksab/gh-anchor/versioning"
)

// Section names in .gh-anchor.toml.
const (
	SectionArgs     = config.SectionArgs
	SectionImages   = config.SectionImages
	SectionPackages = config.SectionPackages
)

// nodeLTSPattern matches Node.js LTS lines: even major versions, with any suffix.
const nodeLTSPattern = `^\d*[02468](\.\d+)*(-.*)?$`

// Source is a Dockerfile to scan.
type Source struct {
	Path    string
	Content string
}

// Location is where an item appears.
type Location struct {
	File string
	Line int
}

// Suggestion is one item init found and can manage.
type Suggestion struct {
	Section string // SectionArgs, SectionImages or SectionPackages
	Name    string // ARG name, image key (stage name or repository), or npm package
	Current string // ARG default, image reference or package spec as written
	Instr   string // FROM, COPY, ARG or RUN (first occurrence)
	Where   []Location

	Arg     config.Arg     // the datasource, for args
	Image   config.Image   // the recorded image and its settings, for images
	Package config.Package // the recorded version and hash, for packages
	Manager string         // the command a package is passed to, e.g. "corepack"
	Stage   string         // the FROM stage name as written, "" if unnamed

	// Reason explains how the suggestion was found, e.g. "used in https://...".
	Reason string
	// Notes are lookup results and hints, rendered as comments.
	Notes []string
	// Selected is the default choice in the form and with --yes. Deselected items are
	// written with skip = true.
	Selected bool
	// Rejected marks an ARG suggestion that a lookup disproved (e.g. the npm package
	// doesn't exist). It is rendered commented out.
	Rejected bool
}

// Key returns the TOML table name, e.g. `args.NODE_VERSION` or `images."ghcr.io/o/r"`.
func (s Suggestion) Key() string {
	return s.Section + "." + tomlKey(s.Name)
}

// Unmatched is a version-like ARG whose source couldn't be detected.
type Unmatched struct {
	Name  string
	Value string
	Where Location
}

// Conflict is a table that two references would share with different values, e.g.
// two Dockerfiles whose unnamed final stages use different images.
type Conflict struct {
	Table       string
	First, Then Location
	FirstValue  string
	ThenValue   string
}

func (c Conflict) Error() string {
	return fmt.Sprintf(
		"[%s] would record %s (%s:%d) and %s (%s:%d); one table can only record one. "+
			"Name the stages (FROM image AS name) or use one version",
		c.Table,
		shortenDigest(
			c.FirstValue,
		),
		c.First.File,
		c.First.Line,
		shortenDigest(c.ThenValue),
		c.Then.File,
		c.Then.Line,
	)
}

// Plan is everything init found.
type Plan struct {
	Files       []string
	Suggestions []Suggestion
	Unmatched   []Unmatched
	Conflicts   []Conflict
}

type argDef struct {
	name, value string
	where       Location
}

// Detect scans the Dockerfiles and returns suggestions, without network access.
func Detect(sources []Source) *Plan {
	p := &Plan{}
	argIndex := map[string]int{}
	imageIndex := map[string]int{}
	pkgIndex := map[string]int{}

	for _, src := range sources {
		p.Files = append(p.Files, src.Path)
		instructions := dockerfile.Parse(src.Content)
		refs, _ := dockerfile.Extract(src.Content)
		for _, ref := range refs {
			loc := Location{File: src.Path, Line: ref.Line}
			switch ref.Kind {
			case dockerfile.KindArg:
				p.addArg(
					argIndex,
					argDef{name: ref.Name, value: ref.Value, where: loc},
					instructions,
				)
			case dockerfile.KindImage:
				p.addImage(imageIndex, ref, loc)
			case dockerfile.KindPackage:
				p.addPackage(pkgIndex, ref, loc)
			}
		}
	}
	return p
}

func (p *Plan) addArg(index map[string]int, def argDef, instructions []dockerfile.Instruction) {
	if i, ok := index[def.name]; ok {
		p.Suggestions[i].Where = append(p.Suggestions[i].Where, def.where)
		return
	}
	for _, u := range p.Unmatched {
		if u.Name == def.name {
			return
		}
	}
	if !IsCandidate(def.name) || strings.Contains(def.value, "$") {
		return
	}
	arg, reason, ok := classifyArg(def.name, def.value, usages(def.name, instructions))
	if !ok {
		p.Unmatched = append(
			p.Unmatched,
			Unmatched{Name: def.name, Value: def.value, Where: def.where},
		)
		return
	}
	index[def.name] = len(p.Suggestions)
	p.Suggestions = append(p.Suggestions, Suggestion{
		Section:  SectionArgs,
		Name:     def.name,
		Current:  def.value,
		Instr:    "ARG",
		Where:    []Location{def.where},
		Arg:      arg,
		Reason:   reason,
		Selected: true,
	})
}

// seen reports whether the table already exists, adding loc to it. A different value
// is recorded as a conflict.
func (p *Plan) seen(index map[string]int, key, value string, loc Location) bool {
	i, ok := index[key]
	if !ok {
		return false
	}
	s := &p.Suggestions[i]
	if s.Current != value {
		p.Conflicts = append(p.Conflicts, Conflict{
			Table: s.Key(), First: s.Where[0], Then: loc, FirstValue: s.Current, ThenValue: value,
		})
	}
	s.Where = append(s.Where, loc)
	return true
}

func (p *Plan) addImage(index map[string]int, ref dockerfile.Ref, loc Location) {
	parsed, err := registry.ParseReference(ref.Value)
	if err != nil || p.seen(index, ref.Key, ref.Value, loc) {
		return
	}
	tag := parsed.Tag
	if tag == "" && parsed.Digest == "" {
		tag = "latest"
	}
	s := Suggestion{
		Section:  SectionImages,
		Name:     ref.Key,
		Current:  ref.Value,
		Instr:    ref.Instr,
		Where:    []Location{loc},
		Image:    config.Image{Image: parsed.Name, Tag: tag, Digest: parsed.Digest},
		Selected: true,
	}
	if ref.Instr == "FROM" {
		s.Stage = ref.Stage
	}
	switch last := parsed.Repo[strings.LastIndexByte(parsed.Repo, '/')+1:]; last {
	case "ubuntu":
		s.Image.LTS = true
		s.Notes = []string{
			"default: lts = true, so bumping only moves between Ubuntu LTS releases (24.04 -> 26.04)",
		}
	case "node":
		s.Image.LTS = true
		s.Image.LTSPattern = nodeLTSPattern
		s.Notes = []string{
			"default: lts = true, so bumping only moves to Node.js LTS lines (even major versions)",
		}
	}
	index[ref.Key] = len(p.Suggestions)
	p.Suggestions = append(p.Suggestions, s)
}

func (p *Plan) addPackage(index map[string]int, ref dockerfile.Ref, loc Location) {
	version, hash := updater.SplitPackageSpec(ref.Value)
	if i, ok := index[ref.Name]; ok {
		prev := &p.Suggestions[i]
		switch {
		case prev.Package.Version != version || (hash != "" && prev.Package.Hash != "" && prev.Package.Hash != hash):
			p.Conflicts = append(p.Conflicts, Conflict{
				Table: prev.Key(), First: prev.Where[0], Then: loc, FirstValue: prev.Current, ThenValue: ref.Value,
			})
		case prev.Package.Hash == "" && hash != "":
			// The same version, written with its hash here.
			prev.Package.Hash = hash
			prev.Current = ref.Value
		}
		prev.Where = append(prev.Where, loc)
		return
	}
	s := Suggestion{
		Section:  SectionPackages,
		Name:     ref.Name,
		Current:  ref.Value,
		Instr:    "RUN",
		Where:    []Location{loc},
		Package:  config.Package{Version: version, Hash: hash},
		Manager:  ref.Manager,
		Selected: true,
	}
	if ref.Manager == "corepack" {
		s.Notes = []string{
			"passed to corepack, so gh-anchor pins it as " + ref.Name + "@<version>+sha512.<hex>",
		}
	}
	index[ref.Name] = len(p.Suggestions)
	p.Suggestions = append(p.Suggestions, s)
}

// usage is an instruction that references an ARG.
type usage struct {
	instr string
	text  string // the instruction's logical text
}

// usages returns the instructions (other than ARG) that reference $name or ${name}.
func usages(name string, instructions []dockerfile.Instruction) []usage {
	re := varPattern(name)
	var out []usage
	for _, in := range instructions {
		if in.Cmd == "ARG" || !re.MatchString(in.Args) {
			continue
		}
		out = append(out, usage{instr: in.Cmd, text: in.Args})
	}
	return out
}

// varPattern matches a reference to the variable: ${NAME}, ${NAME:-x} or $NAME.
func varPattern(name string) *regexp.Regexp {
	q := regexp.QuoteMeta(name)
	return regexp.MustCompile(`\$\{` + q + `(?::[-+?][^}]*)?\}|\$` + q + `\b`)
}

// varRE is the source of varPattern without the capturing alternatives, for embedding.
func varRE(name string) string {
	q := regexp.QuoteMeta(name)
	return `(?:\$\{` + q + `(?::[-+?][^}]*)?\}|\$` + q + `\b)`
}

// IsCandidate reports whether init should look at an ARG: its name contains VERSION
// or ends in _SHA or _COMMIT. Other ARGs (USERNAME, USER_UID, ...) are not versions.
func IsCandidate(name string) bool { return config.IsVersionArg(name) }

func isVersionLike(v string) bool {
	_, ok := versioning.Parse(v)
	return ok
}

// nameDefaults maps well-known ARG names to a datasource when usage gives no clue.
var nameDefaults = map[string]config.Arg{
	"NODE_VERSION":     {Datasource: config.DatasourceNodeVersion, LTS: true},
	"NODEJS_VERSION":   {Datasource: config.DatasourceNodeVersion, LTS: true},
	"NPM_VERSION":      {Datasource: config.DatasourceNPM, Package: "npm"},
	"PNPM_VERSION":     {Datasource: config.DatasourceNPM, Package: "pnpm"},
	"YARN_VERSION":     {Datasource: config.DatasourceNPM, Package: "yarn"},
	"COREPACK_VERSION": {Datasource: config.DatasourceNPM, Package: "corepack"},
	"GO_VERSION":       {Datasource: config.DatasourceDocker, Image: "golang"},
	"GOLANG_VERSION":   {Datasource: config.DatasourceDocker, Image: "golang"},
	"PYTHON_VERSION":   {Datasource: config.DatasourceDocker, Image: "python"},
}

// classifyArg picks a datasource for an ARG from its name, then from how it is used.
func classifyArg(name, value string, uses []usage) (config.Arg, string, bool) {
	if arg, ok := nameDefaults[strings.ToUpper(name)]; ok {
		return arg, "known ARG name " + name, true
	}
	v := varRE(name)
	for _, rule := range usageRules {
		for _, u := range uses {
			if arg, evidence, ok := rule(name, value, v, u); ok {
				return arg, evidence, true
			}
		}
	}
	return config.Arg{}, "", false
}

// usageRule recognizes one way an ARG can be used. v is the variable regexp source.
type usageRule func(name, value, v string, u usage) (config.Arg, string, bool)

// usageRules are tried in order; the first match wins.
var usageRules = []usageRule{
	imageTagRule,
	urlRule(`nodejs\.org/dist/v?%s|node-v%s`, func([]string) config.Arg {
		return config.Arg{Datasource: config.DatasourceNodeVersion, LTS: true}
	}),
	urlRule(
		`registry\.npmjs\.org/((?:@[^/\s"']+(?:/|%%2[fF]))?[^/\s"'@]+)/(?:-/[^/\s"']+-)?%s`,
		func(m []string) config.Arg {
			pkg := strings.NewReplacer("%2f", "/", "%2F", "/").Replace(m[1])
			return config.Arg{Datasource: config.DatasourceNPM, Package: pkg}
		},
	),
	inlinePackageRule,
	urlRule(`github\.com/([\w.-]+)/([\w.-]+)/releases/download/v?%s`, func(m []string) config.Arg {
		return config.Arg{Datasource: config.DatasourceGitHubReleases, Repo: m[1] + "/" + m[2]}
	}),
	urlRule(
		`github\.com/([\w.-]+)/([\w.-]+)/archive/(?:refs/tags/)?v?%s`,
		func(m []string) config.Arg {
			return config.Arg{Datasource: config.DatasourceGitHubTags, Repo: m[1] + "/" + m[2]}
		},
	),
	urlRule(`raw\.githubusercontent\.com/([\w.-]+)/([\w.-]+)/v?%s/`, func(m []string) config.Arg {
		return config.Arg{Datasource: config.DatasourceGitHubTags, Repo: m[1] + "/" + m[2]}
	}),
	urlRule(`(?:go\.dev/dl|golang\.org/dl|dl\.google\.com/go)/go%s`, func([]string) config.Arg {
		return config.Arg{Datasource: config.DatasourceDocker, Image: "golang"}
	}),
	urlRule(`python\.org/ftp/python/%s/`, func([]string) config.Arg {
		return config.Arg{Datasource: config.DatasourceDocker, Image: "python"}
	}),
	gitRepoRule,
}

// urlRule builds a rule from a pattern with %s placeholders for the variable.
func urlRule(pattern string, build func(m []string) config.Arg) usageRule {
	return func(name, value, v string, u usage) (config.Arg, string, bool) {
		n := strings.Count(pattern, "%s")
		args := make([]any, n)
		for i := range args {
			args[i] = v
		}
		re := regexp.MustCompile(fmt.Sprintf(pattern, args...))
		m := re.FindStringSubmatch(u.text)
		if m == nil {
			return config.Arg{}, "", false
		}
		return build(m), "used in " + tokenAround(u.text, m[0]), true
	}
}

// imageTagRule matches FROM image:${ARG} and COPY --from=image:${ARG}.
func imageTagRule(name, value, v string, u usage) (config.Arg, string, bool) {
	if u.instr != "FROM" && u.instr != "COPY" {
		return config.Arg{}, "", false
	}
	re := regexp.MustCompile(`(?:^|\s|--from=)([a-z0-9][a-z0-9._/:-]*?):` + v + `(?:\s|$)`)
	m := re.FindStringSubmatch(u.text)
	if m == nil {
		return config.Arg{}, "", false
	}
	if _, err := registry.ParseReference(m[1]); err != nil {
		return config.Arg{}, "", false
	}
	return config.Arg{Datasource: config.DatasourceDocker, Image: m[1]},
		"used as the tag of " + m[1] + " in " + u.instr, true
}

// inlinePackageRule matches pkg@${ARG} passed to npm, npx, pnpm, yarn or corepack.
func inlinePackageRule(name, value, v string, u usage) (config.Arg, string, bool) {
	if u.instr != "RUN" ||
		!regexp.MustCompile(`\b(npm|npx|pnpm|pnpx|yarn|corepack|bunx?)\b`).MatchString(u.text) {
		return config.Arg{}, "", false
	}
	re := regexp.MustCompile(`(?:^|[\s"'])((?:@[a-z0-9][\w.~-]*/)?[a-z0-9][\w.~-]*)@` + v)
	m := re.FindStringSubmatch(u.text)
	if m == nil {
		return config.Arg{}, "", false
	}
	return config.Arg{
		Datasource: config.DatasourceNPM,
		Package:    m[1],
	}, "used in " + tokenAround(
		u.text,
		m[0],
	), true
}

// githubRepoRe matches https://github.com/o/r(.git) and git@github.com:o/r, but not
// hosts such as cli.github.com.
var githubRepoRe = regexp.MustCompile(
	`(?:^|[\s"'@/=])github\.com[/:]([\w.-]+)/([\w-]+(?:\.[\w-]+)*?)(?:\.git)?(?:[/\s"'#]|$)`,
)

// shellSegmentRe splits a RUN command into its && / || / ; / | separated commands.
var shellSegmentRe = regexp.MustCompile(`&&|\|\||;|\|`)

// gitRepoRule matches a commit SHA (or tag) used in a git command, taken from the
// GitHub repository cloned or added as a remote earlier in the same RUN, e.g.
//
//	git remote add origin https://github.com/owner/repo.git && git fetch origin "${SHA}"
func gitRepoRule(name, value, v string, u usage) (config.Arg, string, bool) {
	if u.instr != "RUN" {
		return config.Arg{}, "", false
	}
	varRe := regexp.MustCompile(v)
	repo := ""
	for _, seg := range shellSegmentRe.Split(u.text, -1) {
		seg = strings.TrimSpace(seg)
		if !isGitCommand(seg) {
			continue
		}
		if m := githubRepoRe.FindStringSubmatch(seg); m != nil {
			repo = m[1] + "/" + m[2]
		}
		if varRe.MatchString(seg) {
			break
		}
	}
	if repo == "" || !gitUsesVar(u.text, varRe) {
		return config.Arg{}, "", false
	}
	evidence := "used with https://github.com/" + repo
	switch {
	case datasource.IsSHA(value):
		return config.Arg{
			Datasource: config.DatasourceGitHubCommits,
			Repo:       repo,
			Track:      config.TrackBranch,
		}, evidence, true
	case isVersionLike(value):
		return config.Arg{Datasource: config.DatasourceGitHubTags, Repo: repo}, evidence, true
	}
	return config.Arg{}, "", false
}

// isGitCommand reports whether a shell command runs git.
func isGitCommand(seg string) bool {
	fields := strings.Fields(seg)
	return len(fields) > 0 && strings.Trim(fields[0], `"'`) == "git"
}

// gitUsesVar reports whether the variable appears in a git command.
func gitUsesVar(text string, varRe *regexp.Regexp) bool {
	for _, seg := range shellSegmentRe.Split(text, -1) {
		if isGitCommand(strings.TrimSpace(seg)) && varRe.MatchString(seg) {
			return true
		}
	}
	return false
}

// tokenAround returns the whitespace-delimited token containing match, without quotes.
func tokenAround(text, match string) string {
	i := strings.Index(text, match)
	if i < 0 {
		return match
	}
	start := strings.LastIndexAny(text[:i], " \t\"'") + 1
	end := i + len(match)
	if j := strings.IndexAny(text[end:], " \t\"'"); j >= 0 {
		end += j
	} else {
		end = len(text)
	}
	const maxLen = 120
	tok := text[start:end]
	if len(tok) > maxLen {
		tok = tok[:maxLen] + "…"
	}
	return tok
}

// tomlKey quotes a key when it isn't a bare TOML key.
func tomlKey(k string) string {
	if regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(k) {
		return k
	}
	return fmt.Sprintf("%q", k)
}

// SortedLocations formats locations as "Dockerfile:2, 14, 37".
func SortedLocations(where []Location) string {
	byFile := map[string][]int{}
	var files []string
	for _, w := range where {
		if _, ok := byFile[w.File]; !ok {
			files = append(files, w.File)
		}
		byFile[w.File] = append(byFile[w.File], w.Line)
	}
	parts := make([]string, 0, len(files))
	for _, f := range files {
		lines := byFile[f]
		sort.Ints(lines)
		nums := make([]string, len(lines))
		for i, l := range lines {
			nums[i] = strconv.Itoa(l)
		}
		parts = append(parts, f+":"+strings.Join(nums, ", "))
	}
	return strings.Join(parts, "; ")
}
