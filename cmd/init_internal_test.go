// SPDX-License-Identifier: MIT

package cmd

import (
	"bytes"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/esacteksab/gh-anchor/scaffold"
	"github.com/esacteksab/gh-anchor/updater"
	"github.com/esacteksab/gh-anchor/utils"
)

const initDockerfile = "FROM ubuntu:24.04\nARG NODE_VERSION=22.23.4\n"

// Make sure your mock types embed mock.Mock.
type MockFileChecker struct {
	mock.Mock
}

func (m *MockFileChecker) DoesExist(cfgFile string) bool {
	args := m.Called(cfgFile)
	return args.Bool(0)
}

type MockUserPrompt struct {
	mock.Mock
}

func (m *MockUserPrompt) AskOverwrite(cfgFile string, configExists bool) (bool, error) {
	args := m.Called(cfgFile, configExists)
	return args.Bool(0), args.Error(1)
}

// MockItemPicker deselects the suggestions in deselect.
type MockItemPicker struct {
	mock.Mock
	deselect map[string]bool
}

func (m *MockItemPicker) PickItems(plan *scaffold.Plan) error {
	args := m.Called(plan)
	for i := range plan.Suggestions {
		if m.deselect[plan.Suggestions[i].Key()] {
			plan.Suggestions[i].Selected = false
		}
	}
	return args.Error(0)
}

type MockFormRunner struct {
	err error
}

func (m *MockFormRunner) Run() error { return m.err }

// usePrompts swaps the package-level prompts for mocks for the duration of a test.
func usePrompts(t *testing.T, fc FileChecker, up UserPrompt, ip ItemPicker) {
	t.Helper()
	origFC, origUP, origIP := defaultFileChecker, defaultUserPrompt, defaultItemPicker
	defaultFileChecker, defaultUserPrompt, defaultItemPicker = fc, up, ip
	t.Cleanup(
		func() { defaultFileChecker, defaultUserPrompt, defaultItemPicker = origFC, origUP, origIP },
	)
}

func newInitOptions(t *testing.T) (initOptions, *bytes.Buffer) {
	t.Helper()
	t.Chdir(t.TempDir())
	utils.CreateLogger(false)
	Logger = utils.Logger
	require.NoError(t, os.WriteFile("Dockerfile", []byte(initDockerfile), 0o600))
	var out bytes.Buffer
	return initOptions{
		files:      []string{"Dockerfile"},
		configPath: ".gh-anchor.toml",
		out:        &out,
		now:        func() time.Time { return time.Date(2026, 10, 31, 9, 41, 0, 0, time.Local) },
		sources:    func() (updater.Sources, error) { return nil, errors.New("no network in this test") },
	}, &out
}

func TestInitYesBacksUpExistingConfig(t *testing.T) {
	o, _ := newInitOptions(t)
	require.NoError(t, os.WriteFile(".gh-anchor.toml", []byte("# old\n"), 0o600))
	o.yes = true

	require.NoError(t, runInit(t.Context(), o))

	backup, err := os.ReadFile(".gh-anchor.toml-202610310941")
	require.NoError(t, err)
	assert.Equal(t, "# old\n", string(backup))
	written, err := os.ReadFile(".gh-anchor.toml")
	require.NoError(t, err)
	assert.Contains(t, string(written), "[args.NODE_VERSION]")
	assert.Contains(
		t,
		string(written),
		"[images.final]\n  image     = \"ubuntu\"\n  tag       = \"24.04\"\n",
	)
}

func TestInitFallsBackOfflineWhenLookupsUnavailable(t *testing.T) {
	o, out := newInitOptions(t)
	o.stdout = true

	require.NoError(t, runInit(t.Context(), o))
	assert.Contains(
		t,
		out.String(),
		"[args.NODE_VERSION]\n  datasource = \"node-version\"\n  lts        = true\n",
	)
	assert.False(t, doesExist(".gh-anchor.toml"), "--stdout never writes")
}

func TestInitCustomConfigPath(t *testing.T) {
	o, _ := newInitOptions(t)
	o.yes, o.offline, o.configPath = true, true, "anchor.toml"
	require.NoError(t, runInit(t.Context(), o))
	assert.True(t, doesExist("anchor.toml"))
}

func TestInitNoTerminalPrints(t *testing.T) {
	o, out := newInitOptions(t)
	o.offline = true
	picker := new(MockItemPicker)
	usePrompts(t, &RealFileChecker{}, new(MockUserPrompt), picker)

	require.NoError(t, runInit(t.Context(), o))
	assert.Contains(t, out.String(), "[args.NODE_VERSION]")
	assert.False(t, doesExist(".gh-anchor.toml"))
	picker.AssertNotCalled(t, "PickItems", mock.Anything)
}

func TestInitInteractive(t *testing.T) {
	t.Run("NewConfigCreationSuccess", func(t *testing.T) {
		o, _ := newInitOptions(t)
		o.offline, o.interactive = true, true
		fc, up := new(MockFileChecker), new(MockUserPrompt)
		picker := &MockItemPicker{deselect: map[string]bool{"args.NODE_VERSION": true}}
		usePrompts(t, fc, up, picker)
		picker.On("PickItems", mock.Anything).Return(nil)
		fc.On("DoesExist", ".gh-anchor.toml").Return(false)
		up.On("AskOverwrite", ".gh-anchor.toml", false).Return(true, nil)

		require.NoError(t, runInit(t.Context(), o))

		written, err := os.ReadFile(".gh-anchor.toml")
		require.NoError(t, err)
		assert.Contains(
			t,
			string(written),
			"# Deselected in `gh anchor init`: skip = true, so gh-anchor leaves it alone.\n[args.NODE_VERSION]\n",
		)
		assert.Contains(
			t,
			string(written),
			"[images.final]\n  image     = \"ubuntu\"\n  tag       = \"24.04\"\n",
		)
		fc.AssertExpectations(t)
		up.AssertExpectations(t)
		picker.AssertExpectations(t)
	})

	t.Run("OverwriteDeclinedPrints", func(t *testing.T) {
		o, out := newInitOptions(t)
		o.offline, o.interactive = true, true
		require.NoError(t, os.WriteFile(".gh-anchor.toml", []byte("# keep me\n"), 0o600))
		fc, up, picker := new(MockFileChecker), new(MockUserPrompt), new(MockItemPicker)
		usePrompts(t, fc, up, picker)
		picker.On("PickItems", mock.Anything).Return(nil)
		fc.On("DoesExist", ".gh-anchor.toml").Return(true)
		up.On("AskOverwrite", ".gh-anchor.toml", true).Return(false, nil)

		require.NoError(t, runInit(t.Context(), o))

		assert.Contains(t, out.String(), "[args.NODE_VERSION]")
		kept, err := os.ReadFile(".gh-anchor.toml")
		require.NoError(t, err)
		assert.Equal(t, "# keep me\n", string(kept))
		assert.False(
			t,
			doesExist(".gh-anchor.toml-202610310941"),
			"no backup when nothing is overwritten",
		)
	})

	t.Run("CancelledInPicker", func(t *testing.T) {
		o, out := newInitOptions(t)
		o.offline, o.interactive = true, true
		fc, up, picker := new(MockFileChecker), new(MockUserPrompt), new(MockItemPicker)
		usePrompts(t, fc, up, picker)
		picker.On("PickItems", mock.Anything).Return(huh.ErrUserAborted)

		require.NoError(t, runInit(t.Context(), o), "quitting is not an error")
		assert.Empty(t, out.String())
		assert.False(t, doesExist(".gh-anchor.toml"))
		up.AssertNotCalled(t, "AskOverwrite", mock.Anything, mock.Anything)
	})
}

func TestCreateOrOverwriteWithMock(t *testing.T) {
	utils.CreateLogger(false)
	Logger = utils.Logger
	fc, up := new(MockFileChecker), new(MockUserPrompt)
	fc.On("DoesExist", "anchor.toml").Return(true)
	up.On("AskOverwrite", "anchor.toml", true).Return(true, nil)

	exists, create, err := createOrOverwrite("anchor.toml", fc, up)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.True(t, create)
	fc.AssertExpectations(t)
	up.AssertExpectations(t)
}

func TestQuery(t *testing.T) {
	original := formRunnerFactory
	t.Cleanup(func() { formRunnerFactory = original })

	tests := []struct {
		name         string
		exists       bool
		answer       bool
		wantTitle    string
		wantDefault  bool
		wantAccessed bool
	}{
		{"new file defaults to yes", false, true, "Create new file .gh-anchor.toml?", true, true},
		{
			"existing file defaults to no",
			true,
			false,
			"Overwrite existing config file .gh-anchor.toml? (a backup is kept)",
			false,
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ACCESSIBLE", "1")
			var gotTitle string
			var gotDefault, gotAccessible bool
			formRunnerFactory = func(title string, createFile *bool, accessible bool) FormRunner {
				gotTitle, gotDefault, gotAccessible = title, *createFile, accessible
				*createFile = tt.answer // simulate the user's selection
				return &MockFormRunner{}
			}

			got, err := query(".gh-anchor.toml", tt.exists)
			require.NoError(t, err)
			assert.Equal(t, tt.answer, got)
			assert.Equal(t, tt.wantTitle, gotTitle)
			assert.Equal(t, tt.wantDefault, gotDefault)
			assert.True(t, gotAccessible, "ACCESSIBLE is passed to the form")
		})
	}

	formRunnerFactory = func(string, *bool, bool) FormRunner { return &MockFormRunner{err: huh.ErrUserAborted} }
	_, err := query(".gh-anchor.toml", false)
	assert.ErrorIs(t, err, huh.ErrUserAborted)
}

func TestHuhItemPicker(t *testing.T) {
	original := itemFormFactory
	t.Cleanup(func() { itemFormFactory = original })

	plan := scaffold.Detect([]scaffold.Source{{Path: "Dockerfile", Content: initDockerfile}})
	require.Len(t, plan.Suggestions, 2)

	var labels []string
	itemFormFactory = func(options []huh.Option[int], picked *[]int, _ bool) FormRunner {
		for _, o := range options {
			labels = append(labels, o.Key)
		}
		assert.Equal(t, []int{0, 1}, *picked, "everything starts selected")
		*picked = []int{1} // the user deselects the first item
		return &MockFormRunner{}
	}

	require.NoError(t, (&HuhItemPicker{}).PickItems(plan))
	assert.Equal(t, []string{
		"FROM ubuntu:24.04 (stage final) → LTS tags only when bumped",
		"ARG NODE_VERSION=22.23.4 → Node.js releases (LTS only when bumped)",
	}, labels)
	assert.False(t, plan.Suggestions[0].Selected)
	assert.True(t, plan.Suggestions[1].Selected)
}

func TestOptionLabels(t *testing.T) {
	plan := scaffold.Detect([]scaffold.Source{{Path: "Dockerfile", Content: `ARG NPM_VERSION=12.2.1
ARG LIB_SHA=0123456789abcdef0123456789abcdef01234567
RUN git clone https://github.com/owner/lib && git -C lib checkout ${LIB_SHA} && corepack prepare pnpm@12.16.1
COPY --from=ghcr.io/hadolint/hadolint:v2.15.1 /bin/hadolint /bin/
`}})
	var labels []string
	for _, s := range plan.Suggestions {
		labels = append(labels, optionLabel(s))
	}
	assert.Equal(t, []string{
		"ARG NPM_VERSION=12.2.1 → npm package npm",
		"ARG LIB_SHA=0123456789ab… → commits of owner/lib (track branch)",
		"RUN pnpm@12.16.1",
		"COPY ghcr.io/hadolint/hadolint:v2.15.1",
	}, labels)
}
