// SPDX-License-Identifier: MIT

package cmd

import (
	"errors"
	"testing"

	"github.com/charmbracelet/huh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/esacteksab/gh-anchor/config"
	"github.com/esacteksab/gh-anchor/updater"
)

const bumpConfig = `
[images.final]
  image = "ubuntu"
  tag   = "24.04"
  lts   = true
[images.golang]
  image = "golang"
  tag   = "1.26.10"
  bump  = true
[images.node]
  image = "node"
[images.old]
  skip = true
[packages.pnpm]
  version = "12.16.1"
[packages.node]
[args.NODE_VERSION]
  datasource = "node-version"
  lts        = true
`

func bumpCfg(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(bumpConfig))
	require.NoError(t, err)
	cfg.Path = ".gh-anchor.toml"
	return cfg
}

// MockBumpPicker is a mock implementation of BumpPicker.
type MockBumpPicker struct{ mock.Mock }

func (m *MockBumpPicker) PickBumps(
	cfg *config.Config,
	tables []config.Table,
	selected map[string]bool,
) (map[string]bool, error) {
	args := m.Called(cfg, tables, selected)
	picked, _ := args.Get(0).(map[string]bool)
	return picked, args.Error(1)
}

func TestCheckBumpFlags(t *testing.T) {
	require.NoError(t, checkBumpFlags(bumpOptions{}))
	require.NoError(t, checkBumpFlags(bumpOptions{set: true, interactive: true, terminal: true}))
	err := checkBumpFlags(bumpOptions{interactive: true, terminal: true})
	require.EqualError(
		t,
		err,
		"-i/--interactive picks what to bump: use it with -b (gh anchor -b -i)",
	)
	err = checkBumpFlags(bumpOptions{set: true, interactive: true})
	require.ErrorContains(t, err, "-b -i needs a terminal")
}

func TestPlanBumps(t *testing.T) {
	cfg := bumpCfg(t)
	unused := &MockBumpPicker{}
	tests := map[string]struct {
		opts   bumpOptions
		mode   updater.Mode
		tables map[string]bool
		logged []string // plan.bumping: what the run bumps
	}{
		"no flags pins": {bumpOptions{}, updater.ModePin, nil, nil},
		"-u bumps bump = true": {
			bumpOptions{update: true},
			updater.ModeUpdate,
			nil,
			[]string{"images.golang"},
		},
		"bare -b bumps everything": {
			bumpOptions{set: true, keys: []string{bumpAll}},
			updater.ModeBump,
			nil,
			nil,
		},
		"-b=all,pnpm is all": {
			bumpOptions{set: true, keys: []string{"all", "pnpm"}},
			updater.ModeBump,
			nil,
			nil,
		},
		"-b=keys adds to bump = true": {
			bumpOptions{set: true, keys: []string{"pnpm", "packages.node", "NODE_VERSION"}},
			updater.ModeUpdate,
			map[string]bool{
				"packages.pnpm":     true,
				"packages.node":     true,
				"args.NODE_VERSION": true,
			},
			[]string{"images.golang", "packages.node", "packages.pnpm", "args.NODE_VERSION"},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			plan, err := planBumps(tt.opts, cfg, unused)
			require.NoError(t, err)
			assert.Equal(t, tt.mode, plan.mode)
			if tt.tables != nil {
				assert.Equal(t, tt.tables, plan.tables)
			}
			assert.False(t, plan.only)
			if tt.mode == updater.ModeUpdate {
				assert.Equal(t, tt.logged, plan.bumping(cfg))
			}
		})
	}
	unused.AssertNotCalled(t, "PickBumps")
}

func TestPlanBumpsErrors(t *testing.T) {
	cfg := bumpCfg(t)
	for keys, want := range map[string]string{
		"node":   `-b: "node" matches images.node and packages.node; use the full name`,
		"python": `-b: no table named "python"`,
		"old":    "-b: [images.old] has skip = true",
	} {
		_, err := planBumps(bumpOptions{set: true, keys: []string{keys}}, cfg, &MockBumpPicker{})
		require.ErrorContains(t, err, want, keys)
	}
}

func TestPlanBumpsInteractive(t *testing.T) {
	cfg := bumpCfg(t)
	picker := &MockBumpPicker{}
	// Skipped tables aren't offered; bump = true and -b=keys are pre-selected.
	picker.On("PickBumps", cfg, mock.MatchedBy(func(tables []config.Table) bool {
		for _, tb := range tables {
			if tb.Skip {
				return false
			}
		}
		return len(tables) == 6
	}), map[string]bool{"images.golang": true, "packages.pnpm": true}).
		Return(map[string]bool{"images.final": true}, nil).Once()

	plan, err := planBumps(
		bumpOptions{set: true, keys: []string{"pnpm"}, interactive: true, terminal: true},
		cfg,
		picker,
	)
	require.NoError(t, err)
	assert.Equal(t, updater.ModeUpdate, plan.mode)
	assert.True(t, plan.only, "the selection replaces bump = true")
	assert.Equal(t, []string{"images.final"}, plan.bumping(cfg))
	picker.AssertExpectations(t)

	// Quitting the picker cancels the run.
	picker = &MockBumpPicker{}
	picker.On("PickBumps", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, huh.ErrUserAborted)
	_, err = planBumps(
		bumpOptions{set: true, keys: []string{bumpAll}, interactive: true, terminal: true},
		cfg,
		picker,
	)
	require.ErrorIs(t, err, errCancelled)

	picker = &MockBumpPicker{}
	picker.On("PickBumps", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("boom"))
	_, err = planBumps(
		bumpOptions{set: true, keys: []string{bumpAll}, interactive: true, terminal: true},
		cfg,
		picker,
	)
	require.EqualError(t, err, "boom")
}

// pickFormRunner stands in for the form: it picks the given options.
type pickFormRunner struct {
	picked *[]int
	pick   []int
}

func (p *pickFormRunner) Run() error { *p.picked = p.pick; return nil }

func TestHuhBumpPicker(t *testing.T) {
	cfg := bumpCfg(t)
	var labels []string
	var preselected []int
	orig := bumpFormFactory
	t.Cleanup(func() { bumpFormFactory = orig })
	bumpFormFactory = func(options []huh.Option[int], picked *[]int, _ bool) FormRunner {
		for _, o := range options {
			labels = append(labels, o.Key)
		}
		preselected = append(preselected, *picked...)
		return &pickFormRunner{picked: picked, pick: []int{0, 5}}
	}
	var tables []config.Table
	for _, tb := range cfg.Tables() {
		if !tb.Skip {
			tables = append(tables, tb)
		}
	}
	chosen, err := (&HuhBumpPicker{}).PickBumps(cfg, tables, map[string]bool{"images.golang": true})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"images.final: ubuntu:24.04, LTS tags only",
		"images.golang: golang:1.26.10 (bump = true)",
		"images.node: node",
		"packages.node: version not recorded yet",
		"packages.pnpm: 12.16.1",
		"args.NODE_VERSION: Node.js releases (LTS only when bumped)",
	}, labels)
	assert.Equal(t, []int{1}, preselected)
	assert.Equal(t, map[string]bool{"images.final": true, "args.NODE_VERSION": true}, chosen)
}
