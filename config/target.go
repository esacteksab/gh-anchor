// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"strconv"

	"github.com/esacteksab/gh-anchor/versioning"
)

// Target is a version line to move to and stay on, such as "24" (the newest 24.x.y)
// or "1.26" (the newest 1.26.x). In TOML it is a string or an integer: target = 24.
type Target string

// UnmarshalTOML accepts a string or an integer.
func (t *Target) UnmarshalTOML(v any) error {
	switch v := v.(type) {
	case string:
		*t = Target(v)
	case int64:
		*t = Target(strconv.FormatInt(v, 10))
	case float64:
		return fmt.Errorf("target = %v: quote a line with a dot, e.g. target = \"1.26\" (as a number, 1.20 would be 1.2)", v)
	default:
		return fmt.Errorf("target must be a version line such as \"24\" or \"1.26\" (got %T)", v)
	}
	return nil
}

// Line returns the version line, and false when no target is set.
func (t Target) Line() (versioning.Line, bool) {
	if t == "" {
		return nil, false
	}
	return versioning.ParseLine(string(t))
}

// validate reports a target that isn't a version line.
func (t Target) validate() error {
	if t == "" {
		return nil
	}
	if _, ok := versioning.ParseLine(string(t)); !ok {
		return fmt.Errorf(
			"target must be a version line such as \"24\" or \"1.26\" (got %q)",
			string(t),
		)
	}
	return nil
}

// TargetOf returns the target of a table, if any.
func (c *Config) TargetOf(section, key string) Target {
	switch section {
	case SectionImages:
		return c.Images[key].Target
	case SectionPackages:
		return c.Packages[key].Target
	case SectionArgs:
		return c.Args[key].Target
	}
	return ""
}
