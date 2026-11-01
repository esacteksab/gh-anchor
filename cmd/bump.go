// SPDX-License-Identifier: MIT

package cmd

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/esacteksab/gh-anchor/config"
	"github.com/esacteksab/gh-anchor/updater"
)

// bumpAll is the value of a bare -b/--bump: bump everything.
const bumpAll = "all"

// Global variables used by the bump picker. Tests replace them with mocks.
var (
	defaultBumpPicker BumpPicker = &HuhBumpPicker{}     // Default implementation of BumpPicker
	bumpFormFactory              = newHuhBumpFormRunner // Creates the bump multi-select form
)

// BumpPicker asks which tables to bump.
// This allows for dependency injection and easier testing.
type BumpPicker interface {
	// PickBumps shows the tables, with those in selected pre-selected, and returns the
	// names (config.TableName) of the tables chosen.
	PickBumps(
		cfg *config.Config,
		tables []config.Table,
		selected map[string]bool,
	) (map[string]bool, error)
}

// bumpOptions are the -b/--bump and -i/--interactive flags.
type bumpOptions struct {
	set         bool     // -b/--bump was given
	keys        []string // its values; "all" for a bare -b
	interactive bool     // -i/--interactive
	terminal    bool     // stdin and stdout are a terminal
	update      bool     // -u/--update
}

// bumpPlan is what a run bumps.
type bumpPlan struct {
	mode   updater.Mode
	tables map[string]bool // tables to bump in ModeUpdate, by config.TableName
	only   bool            // bump only tables, ignoring bump = true (the picker)
}

// errCancelled reports that the user quit the bump picker.
var errCancelled = errors.New("cancelled")

// checkBumpFlags reports flag combinations that can't work, before anything is read.
func checkBumpFlags(o bumpOptions) error {
	switch {
	case o.interactive && !o.set:
		return errors.New("-i/--interactive picks what to bump: use it with -b (gh anchor -b -i)")
	case o.interactive && !o.terminal:
		return errors.New("-b -i needs a terminal; name the tables instead, e.g. -b=golang,pnpm")
	}
	return nil
}

// planBumps decides the run's mode and which tables to bump:
//
//   - no -b: pin, or update with -u, bumping tables with bump = true;
//   - a bare -b (or -b=all): bump everything;
//   - -b=key,...: update everything, bumping the named tables and those with bump = true;
//   - -b -i: update everything, bumping the tables picked (pre-selected: bump = true
//     and any named with -b=key).
func planBumps(o bumpOptions, cfg *config.Config, picker BumpPicker) (bumpPlan, error) {
	if !o.set {
		if o.update {
			return bumpPlan{mode: updater.ModeUpdate}, nil
		}
		return bumpPlan{mode: updater.ModePin}, nil
	}
	named, all, err := resolveBumpKeys(o.keys, cfg)
	if err != nil {
		return bumpPlan{}, err
	}
	if !o.interactive {
		if all {
			return bumpPlan{mode: updater.ModeBump}, nil
		}
		return bumpPlan{mode: updater.ModeUpdate, tables: named}, nil
	}

	var tables []config.Table
	for _, t := range cfg.Tables() {
		if t.Skip {
			continue
		}
		tables = append(tables, t)
		if t.Bump {
			named[t.Name()] = true
		}
	}
	if len(tables) == 0 {
		return bumpPlan{}, fmt.Errorf("%s lists nothing to bump", cfg.Path)
	}
	picked, err := picker.PickBumps(cfg, tables, named)
	if err != nil {
		if isCancelled(err) {
			return bumpPlan{}, errCancelled
		}
		return bumpPlan{}, err
	}
	return bumpPlan{mode: updater.ModeUpdate, tables: picked, only: true}, nil
}

// resolveBumpKeys returns the tables named with -b=key,..., and whether "all" was given.
func resolveBumpKeys(keys []string, cfg *config.Config) (map[string]bool, bool, error) {
	named := map[string]bool{}
	all := false
	for _, k := range keys {
		switch k = strings.TrimSpace(k); k {
		case "":
		case bumpAll:
			all = true
		default:
			t, err := cfg.ResolveTable(k)
			if err != nil {
				return nil, false, fmt.Errorf("-b: %w", err)
			}
			if t.Skip {
				return nil, false, fmt.Errorf(
					"-b: [%s] has skip = true, so it isn't updated at all",
					t.Name(),
				)
			}
			named[t.Name()] = true
		}
	}
	return named, all, nil
}

// bumping lists the tables a ModeUpdate run bumps, in config order.
func (p bumpPlan) bumping(cfg *config.Config) []string {
	var out []string
	for _, t := range cfg.Tables() {
		if !t.Skip && (p.tables[t.Name()] || (!p.only && t.Bump)) {
			out = append(out, t.Name())
		}
	}
	return out
}

// HuhBumpPicker implements the BumpPicker interface with a huh multi-select.
type HuhBumpPicker struct{}

// PickBumps shows every table that isn't skipped and returns the ones chosen.
func (h *HuhBumpPicker) PickBumps(
	cfg *config.Config,
	tables []config.Table,
	selected map[string]bool,
) (map[string]bool, error) {
	accessible, _ = strconv.ParseBool(os.Getenv("ACCESSIBLE"))

	options := make([]huh.Option[int], 0, len(tables))
	picked := make([]int, 0, len(tables))
	for i, t := range tables {
		options = append(options, huh.NewOption(tableLabel(cfg, t), i).Selected(selected[t.Name()]))
		if selected[t.Name()] {
			picked = append(picked, i)
		}
	}
	if err := bumpFormFactory(options, &picked, accessible).Run(); err != nil {
		return nil, err
	}
	chosen := make(map[string]bool, len(picked))
	for _, i := range picked {
		chosen[tables[i].Name()] = true
	}
	return chosen, nil
}

// tableLabel describes a table in the bump picker, e.g.
// "images.golang: golang:1.27.3" or "args.NODE_VERSION: node-version (bump = true)".
func tableLabel(cfg *config.Config, t config.Table) string {
	var what string
	switch t.Section {
	case config.SectionImages:
		img := cfg.Images[t.Key]
		what = img.Image
		if img.Tag != "" {
			what += ":" + img.Tag
		}
		if img.FixedTag {
			what += ", fixed-tag"
		}
		if img.LTS {
			what += ", LTS tags only"
		}
	case config.SectionPackages:
		what = cmp.Or(cfg.Packages[t.Key].Version, "version not recorded yet")
	case config.SectionArgs:
		what = describeArg(cfg.Args[t.Key])
	}
	label := t.Name() + ": " + what
	if t.Bump {
		label += " (bump = true)"
	}
	return label
}

// HuhBumpFormRunner implements the FormRunner interface with the bump multi-select.
type HuhBumpFormRunner struct {
	options    []huh.Option[int]
	picked     *[]int
	accessible bool
}

func newHuhBumpFormRunner(options []huh.Option[int], picked *[]int, accessible bool) FormRunner {
	return &HuhBumpFormRunner{options: options, picked: picked, accessible: accessible}
}

// Run displays the multi-select form.
func (h *HuhBumpFormRunner) Run() error {
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[int]().
				Title("Which should be bumped this run?").
				Description("Bumped items may move to a new major version (FROM images to a new tag). " +
					"The rest is updated within its major version. Pre-selected: bump = true.").
				Options(h.options...).
				Filterable(false).
				Height(len(h.options) + formChromeLines).
				Value(h.picked),
		),
	).WithTheme(huh.ThemeBase16()).
		WithKeyMap(keyMap()).
		WithShowHelp(true).
		WithShowErrors(true).
		WithAccessible(h.accessible)
	return form.Run()
}
