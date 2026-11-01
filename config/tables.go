// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"strings"
)

// Table is one table of the config, e.g. [images.golang].
type Table struct {
	Section string // SectionImages, SectionPackages or SectionArgs
	Key     string // e.g. "golang", "ghcr.io/owner/tool", "pnpm" or "NODE_VERSION"
	Bump    bool
	Skip    bool
}

// Name returns the table name as written in TOML, e.g. images."ghcr.io/o/r".
func (t Table) Name() string { return TableName(t.Section, t.Key) }

// Tables returns every table: images, then packages, then args, each sorted by key.
func (c *Config) Tables() []Table {
	var out []Table
	for _, k := range sortedKeys(c.Images) {
		out = append(
			out,
			Table{Section: SectionImages, Key: k, Bump: c.Images[k].Bump, Skip: c.Images[k].Skip},
		)
	}
	for _, k := range sortedKeys(c.Packages) {
		out = append(
			out,
			Table{
				Section: SectionPackages,
				Key:     k,
				Bump:    c.Packages[k].Bump,
				Skip:    c.Packages[k].Skip,
			},
		)
	}
	for _, k := range sortedKeys(c.Args) {
		out = append(
			out,
			Table{Section: SectionArgs, Key: k, Bump: c.Args[k].Bump, Skip: c.Args[k].Skip},
		)
	}
	return out
}

// HasTable reports whether the config has a table for section and key.
func (c *Config) HasTable(section, key string) bool {
	var ok bool
	switch section {
	case SectionImages:
		_, ok = c.Images[key]
	case SectionPackages:
		_, ok = c.Packages[key]
	case SectionArgs:
		_, ok = c.Args[key]
	}
	return ok
}

// IsVersionArg reports whether an ARG name looks like a version: it contains VERSION
// or ends in _SHA or _COMMIT. Other ARGs (USERNAME, USER_UID, ...) are not versions.
func IsVersionArg(name string) bool {
	n := strings.ToUpper(name)
	return strings.Contains(n, "VERSION") || strings.HasSuffix(n, "_SHA") ||
		strings.HasSuffix(n, "_COMMIT")
}

// BumpsTable reports whether the table has bump = true.
func (c *Config) BumpsTable(section, key string) bool {
	switch section {
	case SectionImages:
		return c.Images[key].Bump
	case SectionPackages:
		return c.Packages[key].Bump
	case SectionArgs:
		return c.Args[key].Bump
	}
	return false
}

// ResolveTable finds the table a name refers to. The name is either a table's key
// ("golang", "pnpm", "NODE_VERSION", "ghcr.io/owner/tool") when only one table has
// it, or the full name: "images.golang", `images."ghcr.io/owner/tool"` or, without
// the quotes, "images.ghcr.io/owner/tool".
func (c *Config) ResolveTable(name string) (Table, error) {
	tables := c.Tables()
	for _, t := range tables {
		if name == t.Name() || name == t.Section+"."+t.Key {
			return t, nil
		}
	}
	var matches []Table
	for _, t := range tables {
		if t.Key == name {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		keys := make([]string, len(tables))
		for i, t := range tables {
			keys[i] = t.Key
		}
		return Table{}, fmt.Errorf(
			"no table named %q (the config has: %s)",
			name,
			strings.Join(keys, ", "),
		)
	}
	names := make([]string, len(matches))
	for i, t := range matches {
		names[i] = t.Name()
	}
	return Table{}, fmt.Errorf(
		"%q matches %s; use the full name",
		name,
		strings.Join(names, " and "),
	)
}
