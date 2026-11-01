// SPDX-License-Identifier: MIT

// Package updater finds the versions and digests referenced by a Dockerfile and
// computes their new values according to the selected Mode.
package updater

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/log"

	"github.com/esacteksab/gh-anchor/config"
	"github.com/esacteksab/gh-anchor/datasource"
	"github.com/esacteksab/gh-anchor/dockerfile"
	"github.com/esacteksab/gh-anchor/registry"
	"github.com/esacteksab/gh-anchor/versioning"
)

// Mode selects how far references may move.
type Mode int

const (
	// ModePin only pins floating references: adds digests to tag-only images, resolves
	// branch/tag names to commit SHAs and partial versions or dist-tags to exact versions.
	ModePin Mode = iota
	// ModeUpdate pins, refreshes digests and moves versions to the newest release
	// within the current major version. Tables with bump = true, and those listed in
	// Updater.Bump, are bumped.
	ModeUpdate
	// ModeBump is ModeUpdate without the major-version restriction, for every
	// reference: FROM images also move to a new tag.
	ModeBump
)

func (m Mode) String() string {
	switch m {
	case ModePin:
		return "pin"
	case ModeUpdate:
		return "update"
	case ModeBump:
		return "bump"
	default:
		return "unknown"
	}
}

// Sources provides the lookups the updater needs. LiveSources implements it against
// real registries; tests use fakes.
type Sources interface {
	ImageTags(ctx context.Context, ref registry.Reference) ([]string, error)
	ImageDigest(ctx context.Context, ref registry.Reference, tag string) (string, error)
	NPMPackage(ctx context.Context, name string) (datasource.Package, error)
	NodeReleases(ctx context.Context) ([]datasource.NodeRelease, error)
	GitHubTags(ctx context.Context, owner, repo string) ([]datasource.Tag, error)
	GitHubReleases(ctx context.Context, owner, repo string) ([]string, error)
	GitHubBranchHead(
		ctx context.Context,
		owner, repo, branch string,
	) (sha, resolvedBranch string, err error)
	GitHubResolveRef(ctx context.Context, owner, repo, ref string) (string, error)
}

// Updater rewrites Dockerfiles. It is not safe for concurrent use.
type Updater struct {
	Mode    Mode
	Config  *config.Config
	Sources Sources
	Log     *log.Logger

	// notes collects what was looked up for the reference being resolved, for the
	// per-reference debug line.
	notes []string
	// records collects the recorded values to update in the config file.
	records []Record
	// File names the Dockerfile being processed, for messages about tables used by
	// more than one Dockerfile.
	File string
	// Bump names tables (as config.TableName gives them, e.g. "images.golang") to
	// bump in ModeUpdate, in addition to tables with bump = true.
	Bump map[string]bool
	// BumpOnly ignores bump = true, so only the tables in Bump are bumped (the
	// -b -i picker, whose selection starts from the tables with bump = true).
	BumpOnly bool

	// mode is the mode for the reference being resolved.
	mode Mode
	// line is the target version line of the reference being resolved, if any, and
	// lineLTS whether its table has lts = true (so the next LTS line may be used).
	line    versioning.Line
	lineLTS bool
	// seen holds the first value each table was used with, across Process calls.
	seen map[string]seenValue
}

// Change describes one rewritten value.
type Change struct {
	Line  int
	Label string // e.g. "FROM", "ARG NODE_VERSION", "RUN pnpm"
	From  string
	To    string
}

// RefError is a failure to process a single reference. Processing continues.
type RefError struct {
	Line  int
	Label string
	Err   error
}

func (e *RefError) Error() string { return fmt.Sprintf("line %d: %s: %v", e.Line, e.Label, e.Err) }
func (e *RefError) Unwrap() error { return e.Err }

// Result is the outcome of processing one Dockerfile.
type Result struct {
	Output  string
	Changes []Change
	Errors  []error
	Summary Summary
	// Mismatches lists tables used with two different values (conflicts). When there
	// are any, nothing was processed and neither file may be written.
	Mismatches []Mismatch
	// Drift lists recorded values the Dockerfile doesn't match. They are overwritten
	// with the Dockerfile's values (in Records), and processing goes on.
	Drift []Mismatch
	// Records are the recorded values to update in the config file.
	Records []Record
	// Tables names the table of every reference, listed or not.
	Tables []string
	// Unlisted are the images, packages and version ARGs without a table.
	Unlisted []Unlisted
}

// Unlisted is a reference without a table in the config.
type Unlisted struct {
	Line  int
	Label string
	Value string
	Table string // the table it would need, e.g. images.final
}

// Summary counts what happened to every reference in a Dockerfile.
type Summary struct {
	Checked  int
	Updated  int
	UpToDate int
	Failed   int
	Skipped  map[string]int // reason -> count
}

// addSkipped records a skipped reference. Callers count it in Checked themselves.
func (s *Summary) addSkipped(reason string) {
	if s.Skipped == nil {
		s.Skipped = map[string]int{}
	}
	s.Skipped[reason]++
}

// SkippedTotal returns the number of skipped references.
func (s Summary) SkippedTotal() int {
	n := 0
	for _, c := range s.Skipped {
		n += c
	}
	return n
}

// String renders e.g. "17 checked, 1 updated, 13 up to date, 3 skipped (no .gh-anchor.toml found: 3), 0 failed".
func (s Summary) String() string {
	skipped := strconv.Itoa(s.SkippedTotal()) + " skipped"
	if len(s.Skipped) > 0 {
		reasons := make([]string, 0, len(s.Skipped))
		for r, c := range s.Skipped {
			reasons = append(reasons, fmt.Sprintf("%s: %d", r, c))
		}
		sort.Strings(reasons)
		skipped += " (" + strings.Join(reasons, ", ") + ")"
	}
	return fmt.Sprintf("%d checked, %d updated, %d up to date, %s, %d failed",
		s.Checked, s.Updated, s.UpToDate, skipped, s.Failed)
}

// skipError marks a reference that was deliberately not checked.
type skipError struct{ reason string }

func (e *skipError) Error() string { return "skipped: " + e.reason }

func skipped(reason string) error { return &skipError{reason: reason} }

// Skip reasons reported in the summary.
const (
	reasonSkipSetting = "skip = true"
	reasonVariable    = "uses a variable"
	reasonNoTag       = "no version tag has this digest"
)

// Process computes the updated Dockerfile for src.
func (u *Updater) Process(ctx context.Context, src string) Result {
	if u.Config == nil {
		u.Config, _ = config.Parse(nil)
	}
	var res Result
	refs, skips := dockerfile.Extract(src)
	u.debugf("found %s", describeRefs(refs, skips))
	sort.SliceStable(skips, func(i, j int) bool { return skips[i].Line < skips[j].Line })
	u.records = nil

	if res.Drift, res.Mismatches = u.checkRecords(refs); len(res.Mismatches) > 0 {
		return conflicted(res, src)
	}

	var edits []dockerfile.Edit
	for _, ref := range refs {
		skips = u.flushSkips(skips, ref.Line, &res.Summary)
		label := labelFor(ref)
		u.begin(ref)
		u.track(&res, ref)
		newValue, err := u.resolve(ctx, ref)
		var skip *skipError
		var outcome string
		switch {
		case errors.As(err, &skip):
			outcome = "skipped (" + skip.reason + ")"
			res.Summary.addSkipped(skip.reason)
		case err != nil:
			outcome = "failed: " + err.Error()
			res.Errors = append(res.Errors, &RefError{Line: ref.Line, Label: label, Err: err})
			res.Summary.Failed++
		case newValue == "" || newValue == ref.Value:
			outcome = "up to date"
			res.Summary.UpToDate++
		default:
			outcome = "updated to " + Shorten(newValue)
			edits = append(edits, dockerfile.Edit{Start: ref.Start, End: ref.End, New: newValue})
			res.Changes = append(
				res.Changes,
				Change{Line: ref.Line, Label: label, From: ref.Value, To: newValue},
			)
			res.Summary.Updated++
		}
		res.Summary.Checked++
		u.debugf("line %d: %s %s: %s", ref.Line, label, Shorten(ref.Value), u.trace(outcome))
	}
	u.flushSkips(skips, math.MaxInt, &res.Summary)

	out, err := dockerfile.Apply(src, edits)
	if err != nil {
		res.Errors = append(res.Errors, err)
		res.Output = src
		res.Changes = nil
		res.Summary.Failed += res.Summary.Updated
		res.Summary.Updated = 0
		return res
	}
	res.Output = out
	res.Records = u.records
	return res
}

// flushSkips logs and counts the skipped candidates before line, returning the rest.
func (u *Updater) flushSkips(skips []dockerfile.Skip, line int, sum *Summary) []dockerfile.Skip {
	for len(skips) > 0 && skips[0].Line < line {
		s := skips[0]
		skips = skips[1:]
		switch s.Reason {
		case dockerfile.ReasonVariable, dockerfile.ReasonContinuation:
			reason := s.Reason
			if reason == dockerfile.ReasonVariable {
				reason = reasonVariable
			}
			sum.addSkipped(reason)
			sum.Checked++
			u.debugf("line %d: %s %s: skipped (%s)", s.Line, s.Instr, s.Text, reason)
		default:
			u.debugf("line %d: %s %s: ignored (%s)", s.Line, s.Instr, s.Text, s.Reason)
		}
	}
	return skips
}

// describeRefs summarizes what Extract found, e.g. "17 references (9 images, 6 ARGs, 2 packages), 2 ignored".
func describeRefs(refs []dockerfile.Ref, skips []dockerfile.Skip) string {
	counts := map[dockerfile.Kind]int{}
	for _, r := range refs {
		counts[r.Kind]++
	}
	return fmt.Sprintf(
		"%d references (%d images, %d ARGs, %d packages) and %d other candidates",
		len(
			refs,
		),
		counts[dockerfile.KindImage],
		counts[dockerfile.KindArg],
		counts[dockerfile.KindPackage],
		len(skips),
	)
}

// note records a detail about the reference being resolved.
func (u *Updater) note(format string, args ...any) {
	u.notes = append(u.notes, fmt.Sprintf(format, args...))
}

// trace joins the collected notes and the outcome.
func (u *Updater) trace(outcome string) string {
	if len(u.notes) == 0 {
		return outcome
	}
	return strings.Join(u.notes, "; ") + " → " + outcome
}

// noteChoice records the result of a newest-version search from cur.
func (u *Updater) noteChoice(cur versioning.Version, best string, found bool) {
	scope := "newest"
	if u.mode != ModeBump {
		scope = fmt.Sprintf("newest in major %d", cur.Major())
	}
	if found {
		u.note("%s: %s", scope, best)
		return
	}
	u.note("%s: %s (current)", scope, cur.Raw)
}

// conflicted is the result when a table is used with two values: nothing changes.
func conflicted(res Result, src string) Result {
	res.Output = src
	res.Drift = nil
	return res
}

// track notes ref's table, and whether it is missing, for an audit.
func (u *Updater) track(res *Result, ref dockerfile.Ref) {
	section, key := tableOf(ref)
	name := config.TableName(section, key)
	res.Tables = append(res.Tables, name)
	if !u.Config.HasTable(section, key) &&
		(ref.Kind != dockerfile.KindArg || config.IsVersionArg(ref.Name)) {
		res.Unlisted = append(
			res.Unlisted,
			Unlisted{Line: ref.Line, Label: labelFor(ref), Value: ref.Value, Table: name},
		)
	}
}

// begin resets the per-reference state before ref is resolved.
func (u *Updater) begin(ref dockerfile.Ref) {
	u.notes = u.notes[:0]
	var why string
	if u.mode, why = u.modeFor(ref); why != "" {
		u.note("%s", why)
	}
	section, key := tableOf(ref)
	u.line, _ = u.Config.TargetOf(section, key).Line()
	if u.mode == ModePin {
		u.line = nil // a target is an update: pin mode doesn't move versions
	}
	switch section {
	case config.SectionImages:
		img, _ := u.Config.ImageFor(key)
		u.lineLTS = img.LTS
	case config.SectionArgs:
		u.lineLTS = u.Config.Args[key].LTS
	default:
		u.lineLTS = false
	}
}

// inLine picks the newest candidate in the target line (with lts = true, the next LTS
// line at or above it), even if that is older than the current version.
func (u *Updater) inLine(
	cur versioning.Version,
	candidates []string,
	ignorePrefix bool,
) (string, error) {
	best, ok := versioning.NewestInLine(cur, candidates, u.line, u.lineLTS, ignorePrefix)
	switch {
	case !ok && u.lineLTS:
		return "", fmt.Errorf("target %s: no LTS release in the %s line or above", u.line, u.line)
	case !ok:
		return "", fmt.Errorf("target %s: no release in the %s line", u.line, u.line)
	}
	bv, _ := versioning.Parse(best)
	if !u.line.Contains(bv) {
		u.note("target %s: no LTS release in that line; next LTS line has %s", u.line, best)
	} else {
		u.note("target %s: newest %s", u.line, best)
	}
	if versioning.Compare(bv, cur) < 0 {
		u.note("older than the current %s, as the target asks", cur.Raw)
	}
	return best, nil
}

// modeFor returns the mode for one reference, and why it is bumped when only some
// tables are: in ModeUpdate, a table with bump = true or named in Bump is bumped.
func (u *Updater) modeFor(ref dockerfile.Ref) (Mode, string) {
	if u.Mode != ModeUpdate {
		return u.Mode, ""
	}
	section, key := tableOf(ref)
	switch {
	case u.Bump[config.TableName(section, key)]:
		return ModeBump, "bumping (-b)"
	case !u.BumpOnly && u.Config.BumpsTable(section, key):
		return ModeBump, "bumping (bump = true)"
	}
	return ModeUpdate, ""
}

// tableOf returns the config section and key of the table that lists ref.
func tableOf(ref dockerfile.Ref) (string, string) {
	switch ref.Kind {
	case dockerfile.KindImage:
		return config.SectionImages, ref.Key
	case dockerfile.KindPackage:
		return config.SectionPackages, ref.Name
	}
	return config.SectionArgs, ref.Name
}

// resolve computes the new value of one reference.
func (u *Updater) resolve(ctx context.Context, ref dockerfile.Ref) (string, error) {
	switch ref.Kind {
	case dockerfile.KindImage:
		return u.image(ctx, ref)
	case dockerfile.KindArg:
		return u.arg(ctx, ref.Name, ref.Value)
	case dockerfile.KindPackage:
		return u.pkg(ctx, ref)
	}
	return ref.Value, nil
}

func labelFor(ref dockerfile.Ref) string {
	switch ref.Kind {
	case dockerfile.KindArg:
		return "ARG " + ref.Name
	case dockerfile.KindPackage:
		return "RUN " + ref.Name
	default:
		return ref.Instr
	}
}

func (u *Updater) debugf(format string, args ...any) {
	if u.Log != nil {
		u.Log.Helper()
		u.Log.Debugf(format, args...)
	}
}

// image computes the new value of an image reference in a FROM or COPY --from.
//
// The image needs a table in the config (keyed by stage name for FROM, by repository
// for COPY --from). With --update, FROM images keep their tag and only get a fresh
// digest: the base image release is a deliberate choice. COPY --from images move to
// the newest tag in the same major version. With --bump, both move to the newest tag;
// an image with lts = true only moves between LTS tags.
func (u *Updater) image(ctx context.Context, r dockerfile.Ref) (_ string, err error) {
	ref, err := registry.ParseReference(r.Value)
	if err != nil {
		return "", err
	}
	settings, ok := u.Config.ImageFor(r.Key)
	switch {
	case !ok:
		return r.Value, u.notListed()
	case settings.Skip:
		return r.Value, skipped(reasonSkipSetting)
	}
	tag, err := u.currentTag(ctx, ref)
	if err != nil || tag == "" {
		return r.Value, err
	}
	defer func() {
		if err == nil {
			u.record(SectionImages, r.Key, "tag", settings.Tag, ref.Tag)
			u.record(SectionImages, r.Key, "digest", settings.Digest, ref.Digest)
		}
	}()
	if u.mode == ModePin && ref.Digest != "" {
		if ref.Tag == "" {
			ref.Tag = tag // add the tag; the digest stays
			return ref.String(), nil
		}
		u.note("already pinned (pin mode doesn't refresh digests)")
		return r.Value, nil
	}

	newTag, err := u.newImageTag(ctx, ref, r.Instr, tag, settings)
	if err != nil {
		return "", err
	}
	digest, err := u.Sources.ImageDigest(ctx, ref, newTag)
	if err != nil {
		return "", err
	}
	switch ref.Digest {
	case "":
		u.note("digest of %s: %s", newTag, Shorten(digest))
	case digest:
		u.note("digest unchanged")
	default:
		u.note("digest of %s changed to %s", newTag, Shorten(digest))
	}
	ref.Tag = newTag
	ref.Digest = digest
	return ref.String(), nil
}

// currentTag returns the tag an image follows: its own, "latest" when it has neither tag
// nor digest, or the tag that has its digest when it is pinned by digest only.
func (u *Updater) currentTag(ctx context.Context, ref registry.Reference) (string, error) {
	switch {
	case ref.Tag != "":
		return ref.Tag, nil
	case ref.Digest == "":
		return "latest", nil
	}
	tag, err := u.tagForDigest(ctx, ref)
	if err == nil && tag == "" {
		err = skipped(reasonNoTag)
	}
	return tag, err
}

// maxDigestScan bounds how many tags are checked to find the tag of a digest.
const maxDigestScan = 50

// tagForDigest finds the version tag whose digest is ref's, checking the newest
// version tags first (stable before suffixed, more specific before less: 0.76.1 before
// 0.76). It returns "" when none of the newest maxDigestScan tags has the digest.
func (u *Updater) tagForDigest(ctx context.Context, ref registry.Reference) (string, error) {
	tags, err := u.Sources.ImageTags(ctx, ref)
	if err != nil {
		return "", err
	}
	var vs []versioning.Version
	for _, t := range tags {
		if v, ok := versioning.Parse(t); ok {
			vs = append(vs, v)
		}
	}
	sort.SliceStable(vs, func(i, j int) bool {
		a, b := vs[i], vs[j]
		if a.IsStable() != b.IsStable() {
			return a.IsStable()
		}
		if c := versioning.Compare(a, b); c != 0 {
			return c > 0
		}
		return len(a.Nums) > len(b.Nums)
	})
	vs = vs[:min(len(vs), maxDigestScan)]
	for i, v := range vs {
		d, err := u.Sources.ImageDigest(ctx, ref, v.Raw)
		switch {
		case registry.IsNotFound(err):
			continue
		case err != nil:
			return "", err
		case d == ref.Digest:
			u.note(
				"pinned by digest only: it is tag %s (checked %d of %d tags)",
				v.Raw,
				i+1,
				len(tags),
			)
			return v.Raw, nil
		}
	}
	u.note(
		"pinned by digest only: none of the %d newest version tags has digest %s",
		len(vs),
		Shorten(ref.Digest),
	)
	return "", nil
}

// newImageTag picks the tag an image should move to (possibly its current tag).
func (u *Updater) newImageTag(
	ctx context.Context,
	ref registry.Reference,
	instr, tag string,
	settings config.Image,
) (string, error) {
	switch {
	case u.mode == ModePin:
		return tag, nil
	case settings.FixedTag:
		u.note("fixed-tag: keeping tag %q", tag)
		return tag, nil
	case instr == "FROM" && u.mode != ModeBump && u.line == nil:
		u.note("base image: keeping tag %q (bump = true or -b changes it)", tag)
		return tag, nil
	}
	cur, ok := versioning.Parse(tag)
	if !ok {
		u.note("tag %q is not a version: refreshing its digest only", tag)
		return tag, nil
	}
	tags, err := u.Sources.ImageTags(ctx, ref)
	if err != nil {
		return "", err
	}
	u.note("%d tags in %s", len(tags), ref.Repository())
	if settings.LTS {
		tags = slices.DeleteFunc(
			slices.Clone(tags),
			func(t string) bool { return !settings.IsLTS(t) },
		)
		u.note("%d LTS tags", len(tags))
	}
	if u.line != nil {
		return u.inLine(cur, tags, false)
	}
	best, found := versioning.Newest(cur, tags, u.mode == ModeBump, false)
	u.noteChoice(cur, best, found)
	if found {
		return best, nil
	}
	return tag, nil
}

// semverLen is the number of numeric components of an exact npm or Node.js version.
const semverLen = 3

// argHandler computes the new value of an ARG for one datasource.
type argHandler func(u *Updater, ctx context.Context, spec config.Arg, value string) (string, error)

var argHandlers = map[string]argHandler{
	config.DatasourceNPM:            (*Updater).argNPM,
	config.DatasourceNodeVersion:    (*Updater).argNode,
	config.DatasourceDocker:         (*Updater).argDocker,
	config.DatasourceGitHubTags:     (*Updater).argGitHubTags,
	config.DatasourceGitHubReleases: (*Updater).argGitHubReleases,
	config.DatasourceGitHubCommits:  (*Updater).commit,
}

// arg computes the new value of an ARG default.
func (u *Updater) arg(ctx context.Context, name, value string) (string, error) {
	spec, ok := u.Config.Args[name]
	switch {
	case !ok:
		return value, u.notListed()
	case spec.Skip:
		return value, skipped(reasonSkipSetting)
	case strings.Contains(value, "$"):
		return value, skipped(reasonVariable)
	}
	handle, ok := argHandlers[spec.Datasource]
	if !ok {
		return "", fmt.Errorf("unsupported datasource %q", spec.Datasource)
	}
	return handle(u, ctx, spec, value)
}

func (u *Updater) argNPM(ctx context.Context, spec config.Arg, value string) (string, error) {
	pkg, err := u.Sources.NPMPackage(ctx, spec.Package)
	if err != nil {
		return "", err
	}
	return u.npmVersion(pkg, spec.Package, value)
}

// npmVersion picks the new version of an npm package.
func (u *Updater) npmVersion(pkg datasource.Package, name, value string) (string, error) {
	candidates := capAtLatest(pkg, value)
	if latest := pkg.DistTags["latest"]; latest != "" {
		u.note("npm %s: %d versions, latest %s", name, len(pkg.Versions), latest)
	} else {
		u.note("npm %s: %d versions", name, len(pkg.Versions))
	}
	if n := len(pkg.Versions) - len(candidates); n > 0 {
		u.note("ignoring %d newer than latest", n)
	}
	return u.version(value, pkg.Versions, candidates, pkg.DistTags, false, semverLen)
}

func (u *Updater) argNode(ctx context.Context, spec config.Arg, value string) (string, error) {
	releases, err := u.Sources.NodeReleases(ctx)
	if err != nil {
		return "", err
	}
	all := make([]string, 0, len(releases))
	var candidates []string
	for _, r := range releases {
		all = append(all, r.Version)
		if r.LTS || !spec.LTS {
			candidates = append(candidates, r.Version)
		}
	}
	u.note("%d Node.js releases", len(all))
	if spec.LTS {
		u.note("%d LTS", len(candidates))
	}
	return u.version(value, all, candidates, nil, true, semverLen)
}

func (u *Updater) argDocker(ctx context.Context, spec config.Arg, value string) (string, error) {
	ref, err := registry.ParseReference(spec.Image)
	if err != nil {
		return "", err
	}
	tags, err := u.Sources.ImageTags(ctx, ref)
	if err != nil {
		return "", err
	}
	u.note("%d tags in %s", len(tags), ref.Repository())
	return u.version(value, tags, tags, nil, false, 0)
}

func (u *Updater) argGitHubTags(
	ctx context.Context,
	spec config.Arg,
	value string,
) (string, error) {
	tags, err := u.Sources.GitHubTags(ctx, spec.Owner(), spec.RepoName())
	if err != nil {
		return "", err
	}
	names := tagNames(tags)
	u.note("%d tags in %s", len(names), spec.Repo)
	// Ignore a "v" prefix: ARG HADOLINT_VERSION=2.15.1 is often used as v${HADOLINT_VERSION}.
	return u.version(value, names, names, nil, true, 0)
}

func (u *Updater) argGitHubReleases(
	ctx context.Context,
	spec config.Arg,
	value string,
) (string, error) {
	names, err := u.Sources.GitHubReleases(ctx, spec.Owner(), spec.RepoName())
	if err != nil {
		return "", err
	}
	u.note("%d releases in %s", len(names), spec.Repo)
	return u.version(value, names, names, nil, true, 0)
}

// configName is the config file name used in messages.
func (u *Updater) configName() string {
	if u.Config.Path != "" {
		return filepath.Base(u.Config.Path)
	}
	return config.DefaultFile
}

// pkg computes the new value of an inline npm package spec. Specs passed to
// corepack (or that already carry a hash) are pinned with the package's sha512,
// e.g. pnpm@12.16.1+sha512.091d…, which corepack verifies when it downloads it.
func (u *Updater) pkg(ctx context.Context, r dockerfile.Ref) (string, error) {
	tbl, ok := u.Config.PackageFor(r.Name)
	switch {
	case !ok:
		return r.Value, u.notListed()
	case tbl.Skip:
		return r.Value, skipped(reasonSkipSetting)
	}
	version, hash := SplitPackageSpec(r.Value)
	p, err := u.Sources.NPMPackage(ctx, r.Name)
	if err != nil {
		return "", err
	}
	if hash != "" && p.Hashes[version] != "" && p.Hashes[version] != hash {
		return "", fmt.Errorf(
			"the hash of %s@%s in the Dockerfile doesn't match the one npm publishes (%s)",
			r.Name,
			version,
			Shorten(p.Hashes[version]),
		)
	}
	newVersion, err := u.npmVersion(p, r.Name, version)
	if err != nil {
		return "", err
	}
	newHash := p.Hashes[newVersion]
	u.record(SectionPackages, r.Name, "version", tbl.Version, newVersion)
	u.record(SectionPackages, r.Name, "hash", tbl.Hash, newHash)

	if (r.Manager != "corepack" && hash == "") || newHash == "" {
		return newVersion, nil
	}
	if hash == "" {
		u.note("pinning with its sha512 for corepack")
	}
	return newVersion + "+" + newHash, nil
}

// capAtLatest drops versions newer than the package's `latest` dist-tag. Maintainers
// often publish to channels such as `next-12` before promoting a release to `latest`
// (pnpm does this), and those should not be picked up. If the current value is
// already newer than `latest`, nothing is dropped.
func capAtLatest(p datasource.Package, current string) []string {
	latest, ok := versioning.Parse(p.DistTags["latest"])
	if !ok {
		return p.Versions
	}
	if cur, ok := versioning.Parse(current); ok && versioning.Compare(cur, latest) > 0 {
		return p.Versions
	}
	out := make([]string, 0, len(p.Versions))
	for _, v := range p.Versions {
		if pv, ok := versioning.Parse(v); ok && versioning.Compare(pv, latest) <= 0 {
			out = append(out, v)
		}
	}
	return out
}

// version picks the new value for a version string.
//
//   - all lists every known version (used to check the current value exists);
//   - candidates lists the versions that may be selected;
//   - distTags maps floating tags such as "latest" to versions (npm only);
//   - exactLen is the number of components of an exact version (3 for semver), or 0
//     when any shape is acceptable (Docker and Git tags).
func (u *Updater) version(
	value string,
	all, candidates []string,
	distTags map[string]string,
	ignorePrefix bool,
	exactLen int,
) (string, error) {
	cur, ok := versioning.Parse(value)
	if !ok {
		if v, ok := distTags[value]; ok {
			u.note("dist-tag %q is %s", value, v)
			return v, nil // pin a dist-tag such as "latest"
		}
		return "", fmt.Errorf("%q is not a version", value)
	}
	allowMajor := u.mode == ModeBump

	if !versioning.Contains(value, all, ignorePrefix) {
		if exactLen > 0 && len(cur.Nums) < exactLen && cur.IsStable() {
			// A floating version such as "22" or "12.16": pin it to the newest match.
			pinned, ok := versioning.NewestMatching(cur, candidates, exactLen, ignorePrefix)
			if !ok {
				return "", fmt.Errorf("no published version matches %q", value)
			}
			u.note("floating version %q: newest match %s", value, pinned)
			if u.line != nil {
				pv, _ := versioning.Parse(pinned)
				return u.inLine(pv, candidates, ignorePrefix)
			}
			if allowMajor {
				pv, _ := versioning.Parse(pinned)
				if best, ok := versioning.Newest(pv, candidates, true, ignorePrefix); ok {
					return best, nil
				}
			}
			return pinned, nil
		}
		if u.mode == ModePin {
			return "", fmt.Errorf("version %q was not found in the datasource", value)
		}
		u.note("%q is not published", value)
	}
	if u.mode == ModePin {
		u.note("exact version (pin mode keeps it)")
		return value, nil
	}
	if u.line != nil {
		return u.inLine(cur, candidates, ignorePrefix)
	}
	best, found := versioning.Newest(cur, candidates, allowMajor, ignorePrefix)
	u.noteChoice(cur, best, found)
	if found {
		return best, nil
	}
	return value, nil
}

// commit computes the new value of a github-commits ARG.
func (u *Updater) commit(ctx context.Context, spec config.Arg, value string) (string, error) {
	owner, repo := spec.Owner(), spec.RepoName()
	switch {
	case u.mode == ModePin && datasource.IsSHA(value):
		u.note("already a commit SHA")
		return value, nil
	case u.mode == ModePin:
		sha, err := u.Sources.GitHubResolveRef(ctx, owner, repo, value)
		if err == nil {
			u.note("%s %s is commit %s", spec.Repo, value, short(sha))
		}
		return sha, err
	case spec.Track != config.TrackTags:
		sha, branch, err := u.Sources.GitHubBranchHead(ctx, owner, repo, spec.Branch)
		if err != nil {
			return "", err
		}
		u.note("%s %s is at %s", spec.Repo, branch, short(sha))
		return sha, nil
	}

	tags, err := u.Sources.GitHubTags(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	current := currentTag(tags, value)
	u.note("%d tags in %s", len(tags), spec.Repo)
	if current != "" {
		u.note("current commit is tag %s", current)
	}
	target, err := u.targetTag(current, tagNames(tags))
	if err != nil {
		return "", err
	}
	if target == "" {
		return "", fmt.Errorf("no version tags found in %s/%s", owner, repo)
	}
	u.note("following tag %s", target)
	for _, t := range tags {
		if t.Name == target {
			return t.SHA, nil
		}
	}
	return "", fmt.Errorf("tag %s not found in %s/%s", target, owner, repo)
}

// currentTag returns the tag that value names or, when value is a commit SHA, the
// highest-versioned tag pointing at it. It returns "" when there is none.
func currentTag(tags []datasource.Tag, value string) string {
	if !datasource.IsSHA(value) {
		for _, t := range tags {
			if t.Name == value {
				return value
			}
		}
		return ""
	}
	current := ""
	var best versioning.Version
	for _, t := range tags {
		if !strings.EqualFold(t.SHA, value) {
			continue
		}
		v, ok := versioning.Parse(t.Name)
		if current == "" || (ok && versioning.Compare(v, best) > 0) {
			current, best = t.Name, v
		}
	}
	return current
}

// targetTag picks the tag to move to from current (which may be "").
func (u *Updater) targetTag(current string, names []string) (string, error) {
	if current == "" && u.line != nil {
		inLine := slices.DeleteFunc(slices.Clone(names), func(n string) bool {
			v, ok := versioning.Parse(n)
			return !ok || !u.line.Contains(v)
		})
		best, ok := versioning.NewestStable(inLine)
		if !ok {
			return "", fmt.Errorf("target %s: no tag in the %s line", u.line, u.line)
		}
		u.note("target %s: newest tag %s", u.line, best)
		return best, nil
	}
	if cv, ok := versioning.Parse(current); ok && u.line != nil {
		return u.inLine(cv, names, false)
	}
	if current == "" {
		if u.mode != ModeBump {
			return "", errors.New(
				"current value is not a tagged release, so its major version is unknown; " +
					"bump it (bump = true or -b) to move to the newest tag, or set track = \"branch\"",
			)
		}
		target, _ := versioning.NewestStable(names)
		return target, nil
	}
	if cv, ok := versioning.Parse(current); ok {
		if best, ok := versioning.Newest(cv, names, u.mode == ModeBump, false); ok {
			return best, nil
		}
	}
	return current, nil
}

func tagNames(tags []datasource.Tag) []string {
	names := make([]string, 0, len(tags))
	for _, t := range tags {
		names = append(names, t.Name)
	}
	return names
}

// shortLen is how many characters of a SHA or digest are shown in logs.
const shortLen = 12

func short(sha string) string {
	if len(sha) > shortLen {
		return sha[:shortLen]
	}
	return sha
}

// Shorten abbreviates digests and commit SHAs in s for display.
func Shorten(s string) string {
	for _, marker := range []string{"sha256:", "sha512."} {
		if i := strings.Index(s, marker); i >= 0 && len(s) > i+len(marker)+shortLen {
			return s[:i+len(marker)+shortLen] + "…"
		}
	}
	if datasource.IsSHA(s) {
		return s[:shortLen] + "…"
	}
	return s
}
