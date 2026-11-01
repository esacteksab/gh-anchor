// SPDX-License-Identifier: MIT

package updater

import (
	"fmt"
	"slices"
	"strings"

	"github.com/esacteksab/gh-anchor/config"
	"github.com/esacteksab/gh-anchor/dockerfile"
	"github.com/esacteksab/gh-anchor/registry"
)

// Config sections whose tables record what the Dockerfile uses.
const (
	SectionImages   = config.SectionImages
	SectionPackages = config.SectionPackages
)

// Record is a value to store in the config file: field = value in [section.key].
type Record struct {
	Section string // SectionImages or SectionPackages
	Key     string // table key, e.g. "python" or "ghcr.io/owner/tool"
	Field   string // "tag", "digest", "version" or "hash"
	Value   string
}

// Table returns the table name as written in TOML, e.g. images."ghcr.io/o/r".
func (r Record) Table() string { return TableName(r.Section, r.Key) }

// TableName renders [section.key] with the key quoted when needed.
func TableName(section, key string) string { return config.TableName(section, key) }

// Mismatch is a recorded value the Dockerfile doesn't match. A plain mismatch (drift)
// is overwritten with the Dockerfile's value; one with OtherLine set is a conflict (one
// table used with two values) and stops the run.
type Mismatch struct {
	Line       int
	Label      string // e.g. "FROM" or "RUN pnpm"
	Table      string // e.g. images.python
	Field      string
	Recorded   string
	Dockerfile string
	// OtherLine is set when the same table is used with two different values; OtherFile
	// is set when that other use is in another Dockerfile.
	OtherLine int
	OtherFile string

	section, key string // the table, for overwriting a drifted value
}

func (m Mismatch) Error() string {
	if m.OtherLine > 0 {
		where := fmt.Sprintf("line %d", m.OtherLine)
		if m.OtherFile != "" {
			where += " of " + m.OtherFile
		}
		return fmt.Sprintf("line %d: %s: [%s] is used with %s here but %s on %s; "+
			"one table can only record one (rename the stage, or list it once)",
			m.Line, m.Label, m.Table, Shorten(m.Dockerfile), Shorten(m.Recorded), where)
	}
	return fmt.Sprintf(
		"line %d: %s: [%s] recorded %s = %q, but the Dockerfile has %q: recording the Dockerfile's",
		m.Line,
		m.Label,
		m.Table,
		m.Field,
		Shorten(m.Recorded),
		Shorten(m.Dockerfile),
	)
}

// SplitPackageSpec splits a package version that carries a hash, such as
// "12.16.1+sha512.091d…", into the version and the hash ("sha512.091d…").
func SplitPackageSpec(value string) (version, hash string) {
	if i := strings.Index(value, "+sha"); i >= 0 {
		return value[:i], value[i+1:]
	}
	return value, ""
}

// seenValue is the first value a table was used with.
type seenValue struct {
	file  string
	line  int
	value string
}

// checkRecords compares the recorded image/tag/digest/version/hash of every listed
// image and package with the Dockerfile. The Dockerfile wins: a recorded value it
// doesn't match is drift, and is overwritten with the Dockerfile's value. Empty
// recorded values are just filled in. Tables are also checked across the Dockerfiles
// processed by u: one table used with two different values is a conflict.
func (u *Updater) checkRecords(refs []dockerfile.Ref) (drift, conflicts []Mismatch) {
	if u.seen == nil {
		u.seen = map[string]seenValue{}
	}
	for _, ref := range refs {
		var ms []Mismatch
		var table, value string
		switch ref.Kind {
		case dockerfile.KindImage:
			ms, table, value = u.checkImage(ref)
		case dockerfile.KindPackage:
			ms, table, value = u.checkPackage(ref)
		default:
			continue
		}
		for _, m := range ms {
			u.overwrite(m.section, m.key, m.Field, m.Dockerfile)
		}
		drift = append(drift, ms...)
		if table == "" {
			continue
		}
		if prev, ok := u.seen[table]; ok && prev.value != value {
			m := Mismatch{
				Line: ref.Line, Label: labelFor(ref), Table: table,
				Recorded: prev.value, Dockerfile: value, OtherLine: prev.line,
			}
			if prev.file != u.File {
				m.OtherFile = prev.file
			}
			conflicts = append(conflicts, m)
			continue
		}
		if _, ok := u.seen[table]; !ok {
			u.seen[table] = seenValue{file: u.File, line: ref.Line, value: value}
		}
	}
	return drift, conflicts
}

func (u *Updater) checkImage(ref dockerfile.Ref) ([]Mismatch, string, string) {
	tbl, ok := u.Config.ImageFor(ref.Key)
	if !ok || tbl.Skip {
		return nil, "", ""
	}
	parsed, err := registry.ParseReference(ref.Value)
	if err != nil {
		return nil, "", ""
	}
	table := TableName(SectionImages, ref.Key)
	tag := parsed.Tag
	if tag == "" && parsed.Digest == "" {
		tag = "latest"
	}
	mismatch := func(field, recorded, actual string) Mismatch {
		return Mismatch{
			Line: ref.Line, Label: labelFor(ref), Table: table,
			Field: field, Recorded: recorded, Dockerfile: actual,
			section: SectionImages, key: ref.Key,
		}
	}
	var ms []Mismatch
	if tbl.Image != "" && !tbl.SameRepository(parsed) {
		ms = append(ms, mismatch("image", tbl.Image, parsed.Name))
	}
	if tbl.Tag != "" && tbl.Tag != tag {
		ms = append(ms, mismatch("tag", tbl.Tag, tag))
	}
	if tbl.Digest != "" && tbl.Digest != parsed.Digest {
		ms = append(ms, mismatch("digest", tbl.Digest, parsed.Digest))
	}
	value := parsed.Name + ":" + tag
	if parsed.Digest != "" {
		value += "@" + parsed.Digest
	}
	return ms, table, value
}

func (u *Updater) checkPackage(ref dockerfile.Ref) ([]Mismatch, string, string) {
	tbl, ok := u.Config.PackageFor(ref.Key)
	if !ok || tbl.Skip {
		return nil, "", ""
	}
	table := TableName(SectionPackages, ref.Key)
	version, hash := SplitPackageSpec(ref.Value)
	var ms []Mismatch
	if tbl.Version != "" && tbl.Version != version {
		ms = append(ms, Mismatch{
			Line: ref.Line, Label: labelFor(ref), Table: table,
			Field: "version", Recorded: tbl.Version, Dockerfile: version,
			section: SectionPackages, key: ref.Key,
		})
	}
	if tbl.Hash != "" && hash != "" && tbl.Hash != hash {
		ms = append(ms, Mismatch{
			Line: ref.Line, Label: labelFor(ref), Table: table,
			Field: "hash", Recorded: tbl.Hash, Dockerfile: hash,
			section: SectionPackages, key: ref.Key,
		})
	}
	return ms, table, version
}

// record notes a recorded value to update if it differs from the config. It replaces
// a value noted earlier for the same field (an overwritten drift), and drops that one
// when the final value is what the config already has.
func (u *Updater) record(section, key, field, current, value string) {
	if value == "" {
		return
	}
	for i, r := range u.records {
		if r.Section == section && r.Key == key && r.Field == field {
			if value == current {
				u.records = slices.Delete(u.records, i, i+1)
			} else {
				u.records[i].Value = value
			}
			return
		}
	}
	if value != current {
		u.records = append(
			u.records,
			Record{Section: section, Key: key, Field: field, Value: value},
		)
	}
}

// overwrite notes the Dockerfile's value for a drifted field, even when it is empty
// (e.g. the Dockerfile has no digest), so the stale value doesn't stay. A later record
// of the same field, after the reference is updated, replaces it.
func (u *Updater) overwrite(section, key, field, value string) {
	u.records = append(u.records, Record{Section: section, Key: key, Field: field, Value: value})
}

// notListed is the skip reason for a reference without a table.
func (u *Updater) notListed() error {
	if u.Config.Empty() && !u.Config.Found {
		return skipped("no " + config.DefaultFile + " found")
	}
	return skipped("not in " + u.configName())
}
