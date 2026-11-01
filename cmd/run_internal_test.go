// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/huh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/esacteksab/gh-anchor/datasource"
	"github.com/esacteksab/gh-anchor/registry"
	"github.com/esacteksab/gh-anchor/updater"
	"github.com/esacteksab/gh-anchor/utils"
)

// A pinned image: pin mode records its tag and digest without any lookup.
const (
	runDigest     = "sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852"
	runDockerfile = "FROM ubuntu:24.04@" + runDigest + "\n"
	runConfig     = "[images.final]\n  image = \"ubuntu\"\n"
)

var errOffline = errors.New("no network in this test")

// offlineSources fails every lookup, like a machine without network access.
type offlineSources struct{}

func (offlineSources) ImageTags(context.Context, registry.Reference) ([]string, error) {
	return nil, errOffline
}

func (offlineSources) ImageDigest(context.Context, registry.Reference, string) (string, error) {
	return "", errOffline
}

func (offlineSources) NPMPackage(context.Context, string) (datasource.Package, error) {
	return datasource.Package{}, errOffline
}

func (offlineSources) NodeReleases(context.Context) ([]datasource.NodeRelease, error) {
	return nil, errOffline
}

func (offlineSources) GitHubTags(context.Context, string, string) ([]datasource.Tag, error) {
	return nil, errOffline
}

func (offlineSources) GitHubReleases(context.Context, string, string) ([]string, error) {
	return nil, errOffline
}

func (offlineSources) GitHubBranchHead(
	context.Context,
	string,
	string,
	string,
) (string, string, error) {
	return "", "", errOffline
}

func (offlineSources) GitHubResolveRef(context.Context, string, string, string) (string, error) {
	return "", errOffline
}

// MockRunPrompt is a mock implementation of RunPrompt.
type MockRunPrompt struct{ mock.Mock }

func (m *MockRunPrompt) AskAction(files []string, cfgFile string) (string, error) {
	args := m.Called(files, cfgFile)
	return args.String(0), args.Error(1)
}

func (m *MockRunPrompt) AskUpdateNow(cfgFile string) (bool, error) {
	args := m.Called(cfgFile)
	return args.Bool(0), args.Error(1)
}

func useRunPrompt(t *testing.T, p RunPrompt) {
	t.Helper()
	orig := defaultRunPrompt
	defaultRunPrompt = p
	t.Cleanup(func() { defaultRunPrompt = orig })
}

// newRun sets up a project with the Dockerfile and, if not empty, the config.
func newRun(t *testing.T, cfg string, terminal bool) runOptions {
	t.Helper()
	t.Chdir(t.TempDir())
	utils.CreateLogger(false)
	Logger = utils.Logger
	require.NoError(t, os.WriteFile("Dockerfile", []byte(runDockerfile), 0o600))
	if cfg != "" {
		require.NoError(t, os.WriteFile(".gh-anchor.toml", []byte(cfg), 0o600))
	}
	return runOptions{
		files: []string{"Dockerfile"}, configPath: ".gh-anchor.toml", terminal: terminal,
		bump:    bumpOptions{terminal: terminal},
		sources: func() (updater.Sources, error) { return offlineSources{}, nil },
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func recorded(t *testing.T) bool {
	t.Helper()
	return strings.Contains(readFile(t, ".gh-anchor.toml"), `digest = "`+runDigest+`"`)
}

func TestRunAsksUpdateOrAudit(t *testing.T) {
	t.Run("audit writes nothing and reports what is pending", func(t *testing.T) {
		o := newRun(t, runConfig, true)
		p := new(MockRunPrompt)
		p.On("AskAction", []string{"Dockerfile"}, ".gh-anchor.toml").Return(actionAudit, nil).Once()
		useRunPrompt(t, p)

		require.NoError(t, runAnchor(t.Context(), o), "findings are information, not errors")
		assert.Equal(t, runConfig, readFile(t, ".gh-anchor.toml"))
		p.AssertExpectations(t)
	})
	t.Run("update", func(t *testing.T) {
		o := newRun(t, runConfig, true)
		p := new(MockRunPrompt)
		p.On("AskAction", mock.Anything, mock.Anything).Return(actionUpdate, nil).Once()
		useRunPrompt(t, p)

		require.NoError(t, runAnchor(t.Context(), o))
		assert.True(t, recorded(t))
		p.AssertExpectations(t)
	})
	t.Run("quitting the prompt changes nothing", func(t *testing.T) {
		o := newRun(t, runConfig, true)
		p := new(MockRunPrompt)
		p.On("AskAction", mock.Anything, mock.Anything).Return("", huh.ErrUserAborted).Once()
		useRunPrompt(t, p)

		require.NoError(t, runAnchor(t.Context(), o))
		assert.Equal(t, runConfig, readFile(t, ".gh-anchor.toml"))
	})
}

func TestRunWithoutPrompt(t *testing.T) {
	for name, set := range map[string]func(*runOptions){
		"--yes":       func(o *runOptions) { o.yes = true },
		"no terminal": func(o *runOptions) { o.terminal = false },
	} {
		t.Run(name, func(t *testing.T) {
			o := newRun(t, runConfig, true)
			set(&o)
			p := new(MockRunPrompt)
			useRunPrompt(t, p)

			require.NoError(t, runAnchor(t.Context(), o))
			assert.True(t, recorded(t))
			p.AssertNotCalled(t, "AskAction", mock.Anything, mock.Anything)
		})
	}

	t.Run("--audit is clean once everything is recorded", func(t *testing.T) {
		o := newRun(t, runConfig, false)
		require.NoError(t, runAnchor(t.Context(), o))
		o.audit = true
		require.NoError(t, runAnchor(t.Context(), o))
	})
}

func TestRunWithoutConfig(t *testing.T) {
	t.Run("no terminal: nothing happens", func(t *testing.T) {
		o := newRun(t, "", false)
		require.NoError(t, runAnchor(t.Context(), o))
		assert.False(t, doesExist(".gh-anchor.toml"))
	})
	t.Run("--audit fails", func(t *testing.T) {
		o := newRun(t, "", true)
		o.audit = true
		require.ErrorIs(t, runAnchor(t.Context(), o), errNoConfig)
	})

	initThen := func(t *testing.T, updateNow bool) {
		t.Helper()
		o := newRun(t, "", true)
		up := new(MockUserPrompt)
		up.On("AskOverwrite", ".gh-anchor.toml", false).Return(true, nil).Once()
		picker := new(MockItemPicker)
		picker.On("PickItems", mock.Anything).Return(nil).Once()
		usePrompts(t, &RealFileChecker{}, up, picker)
		p := new(MockRunPrompt)
		p.On("AskUpdateNow", ".gh-anchor.toml").Return(updateNow, nil).Once()
		useRunPrompt(t, p)

		require.NoError(t, runAnchor(t.Context(), o))
		cfg := readFile(t, ".gh-anchor.toml")
		assert.Contains(t, cfg, "[images.final]", "init wrote the config")
		assert.Contains(
			t,
			cfg,
			`digest    = "`+runDigest+`"`,
			"init records what the Dockerfile has",
		)
		p.AssertExpectations(t)
		p.AssertNotCalled(
			t,
			"AskAction",
			mock.Anything,
			mock.Anything,
		) // "update now" already answered it
		picker.AssertExpectations(t)
	}
	t.Run("terminal: init, then update now", func(t *testing.T) { initThen(t, true) })
	t.Run("terminal: init, then stop to review", func(t *testing.T) { initThen(t, false) })

	t.Run("terminal: init only printed the config", func(t *testing.T) {
		o := newRun(t, "", true)
		up := new(MockUserPrompt)
		up.On("AskOverwrite", ".gh-anchor.toml", false).Return(false, nil).Once()
		picker := new(MockItemPicker)
		picker.On("PickItems", mock.Anything).Return(nil).Once()
		usePrompts(t, &RealFileChecker{}, up, picker)
		p := new(MockRunPrompt)
		useRunPrompt(t, p)

		require.NoError(t, runAnchor(t.Context(), o))
		assert.False(t, doesExist(".gh-anchor.toml"))
		p.AssertNotCalled(t, "AskUpdateNow", mock.Anything)
	})
}
