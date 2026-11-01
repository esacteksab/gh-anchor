// SPDX-License-Identifier: MIT

package cmd

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"

	"github.com/esacteksab/gh-anchor/config"
	"github.com/esacteksab/gh-anchor/scaffold"
	"github.com/esacteksab/gh-anchor/updater"
)

// Global variables used by the init prompts. Tests replace them with mocks.
var (
	accessible         bool                               // Flag to enable accessibility mode for UI interactions
	defaultFileChecker FileChecker = &RealFileChecker{}   // Default implementation of FileChecker interface
	defaultUserPrompt  UserPrompt  = &RealUserPrompt{}    // Default implementation of UserPrompt interface
	defaultItemPicker  ItemPicker  = &HuhItemPicker{}     // Default implementation of ItemPicker interface
	defaultRunPrompt   RunPrompt   = &HuhRunPrompt{}      // Asks what a run should do
	formRunnerFactory              = newHuhFormRunner     // Creates the create/overwrite confirmation form
	itemFormFactory                = newHuhItemFormRunner // Creates the item selection form
)

// FileChecker is an interface for checking file existence.
// This allows for dependency injection and easier testing.
type FileChecker interface {
	DoesExist(cfgFile string) bool
}

// UserPrompt is an interface for asking whether to create or overwrite the config file.
// This allows for dependency injection and easier testing.
type UserPrompt interface {
	AskOverwrite(cfgFile string, configExists bool) (createFile bool, err error)
}

// ItemPicker is an interface for choosing which detected items gh-anchor manages.
// This allows for dependency injection and easier testing.
type ItemPicker interface {
	PickItems(plan *scaffold.Plan) error
}

// RunPrompt asks what a `gh anchor` run should do.
// This allows for dependency injection and easier testing.
type RunPrompt interface {
	// AskAction asks whether to update or only audit: actionUpdate or actionAudit.
	AskAction(files []string, cfgFile string) (string, error)
	// AskUpdateNow asks, after init wrote the config, whether to update right away.
	AskUpdateNow(cfgFile string) (bool, error)
}

// FormRunner is an interface for running UI forms.
// This allows for dependency injection and easier testing of UI components.
type FormRunner interface {
	Run() error
}

// RealFileChecker implements the FileChecker interface for production use.
type RealFileChecker struct{}

// DoesExist checks if a file exists at the specified path.
func (r *RealFileChecker) DoesExist(cfgFile string) bool {
	return doesExist(cfgFile)
}

func doesExist(path string) bool {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return false
	}
	return true
}

// RealUserPrompt implements the UserPrompt interface for production use.
type RealUserPrompt struct{}

// AskOverwrite prompts the user about creating or overwriting the configuration file.
func (r *RealUserPrompt) AskOverwrite(cfgFile string, configExists bool) (bool, error) {
	return query(cfgFile, configExists)
}

// createOrOverwrite determines if the config file exists and asks the user whether
// to create or overwrite it.
//
// Returns:
//
//	configExists - Whether the configuration file already exists
//	createFile - User's decision (true to create/overwrite, false to print instead)
//	err - Any error encountered during the process
func createOrOverwrite(
	cfgFile string,
	fileChecker FileChecker,
	userPrompt UserPrompt,
) (configExists, createFile bool, err error) {
	configExists = fileChecker.DoesExist(cfgFile)
	Logger.Debugf("Using config: %s (exists: %t)", cfgFile, configExists)
	createFile, err = userPrompt.AskOverwrite(cfgFile, configExists)
	if err != nil {
		return false, false, err
	}
	return configExists, createFile, nil
}

// query prompts the user whether to create or overwrite the configuration file.
//
// It reads ACCESSIBLE to decide whether to run in accessible mode, and words the
// prompt based on whether the file already exists.
func query(cfgFile string, configExists bool) (createFile bool, err error) {
	accessible, _ = strconv.ParseBool(os.Getenv("ACCESSIBLE"))

	// Default to Yes for a new file and No when it would overwrite one.
	createFile = !configExists
	title := "Create new file " + cfgFile + "?"
	if configExists {
		title = "Overwrite existing config file " + cfgFile + "? (a backup is kept)"
	}

	formRunner := formRunnerFactory(title, &createFile, accessible)
	if err := formRunner.Run(); err != nil {
		return false, err
	}
	return createFile, nil
}

// HuhFormRunner implements the FormRunner interface with a huh confirmation.
type HuhFormRunner struct {
	title      string // Title displayed in the form
	createFile *bool  // Pointer to store user's selection
	accessible bool   // Whether to enable accessibility features
}

func newHuhFormRunner(title string, createFile *bool, accessible bool) FormRunner {
	return &HuhFormRunner{title: title, createFile: createFile, accessible: accessible}
}

// Run displays a confirmation form to the user and captures their response.
func (h *HuhFormRunner) Run() error {
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(h.title).
				Description("Choose No to print the config instead.").
				Affirmative("Yes").
				Negative("No").
				Value(h.createFile),
		),
	).WithTheme(huh.ThemeBase16()).
		WithKeyMap(keyMap()).
		WithAccessible(h.accessible)
	return form.Run()
}

// HuhItemPicker implements the ItemPicker interface with a huh multi-select.
type HuhItemPicker struct{}

// PickItems shows every detected item, pre-selected, and records the user's choice
// in each suggestion's Selected field.
func (h *HuhItemPicker) PickItems(plan *scaffold.Plan) error {
	if len(plan.Suggestions) == 0 {
		return nil
	}
	accessible, _ = strconv.ParseBool(os.Getenv("ACCESSIBLE"))

	options := make([]huh.Option[int], 0, len(plan.Suggestions))
	picked := make([]int, 0, len(plan.Suggestions))
	for i, s := range plan.Suggestions {
		options = append(options, huh.NewOption(optionLabel(s), i).Selected(s.Selected))
		if s.Selected {
			picked = append(picked, i)
		}
	}

	if err := itemFormFactory(options, &picked, accessible).Run(); err != nil {
		return err
	}

	chosen := make(map[int]bool, len(picked))
	for _, i := range picked {
		chosen[i] = true
	}
	for i := range plan.Suggestions {
		plan.Suggestions[i].Selected = chosen[i]
	}
	return nil
}

// formChromeLines is the room the multi-select needs above its options (title,
// description and padding), so every option is visible without scrolling.
const formChromeLines = 4

// HuhItemFormRunner implements the FormRunner interface with the item multi-select.
type HuhItemFormRunner struct {
	options    []huh.Option[int]
	picked     *[]int
	accessible bool
}

func newHuhItemFormRunner(options []huh.Option[int], picked *[]int, accessible bool) FormRunner {
	return &HuhItemFormRunner{options: options, picked: picked, accessible: accessible}
}

// Run displays the multi-select form.
func (h *HuhItemFormRunner) Run() error {
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[int]().
				Title("Which items should gh-anchor manage?").
				Description("Deselected items are written with skip = true, so gh-anchor leaves them alone.").
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

// keyMap is huh's default key map, quitting on q or esc as well as ctrl+c.
func keyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("q", "esc", "ctrl+c"), key.WithHelp("q", "quit"))
	return km
}

// optionLabel describes a suggestion in the form, e.g.
// "ARG NPM_VERSION=12.2.1 → npm package npm".
func optionLabel(s scaffold.Suggestion) string {
	switch s.Section {
	case scaffold.SectionArgs:
		label := "ARG " + s.Name + "=" + updater.Shorten(s.Current) + " → " + describeArg(s.Arg)
		if s.Rejected {
			label += " (lookup failed)"
		}
		return label
	case scaffold.SectionImages:
		label := s.Instr + " " + updater.Shorten(s.Current)
		if s.Instr == "FROM" {
			label += " (stage " + s.Name + ")"
		}
		if s.Image.LTS {
			label += " → LTS tags only when bumped"
		}
		return label
	default:
		return "RUN " + s.Name + "@" + updater.Shorten(s.Current)
	}
}

// describeArg describes where an ARG's versions come from.
func describeArg(a config.Arg) string {
	switch a.Datasource {
	case config.DatasourceNPM:
		return "npm package " + a.Package
	case config.DatasourceNodeVersion:
		if a.LTS {
			return "Node.js releases (LTS only when bumped)"
		}
		return "Node.js releases"
	case config.DatasourceDocker:
		return "tags of image " + a.Image
	case config.DatasourceGitHubTags:
		return "tags of " + a.Repo
	case config.DatasourceGitHubReleases:
		return "releases of " + a.Repo
	case config.DatasourceGitHubCommits:
		return "commits of " + a.Repo + " (track " + a.Track + ")"
	}
	return strings.TrimSpace(a.Datasource)
}

// HuhRunPrompt implements the RunPrompt interface with huh forms.
type HuhRunPrompt struct{}

// AskAction asks whether to update or audit; Update is the default.
func (h *HuhRunPrompt) AskAction(files []string, cfgFile string) (string, error) {
	accessible, _ = strconv.ParseBool(os.Getenv("ACCESSIBLE"))
	action := actionUpdate
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Update or audit "+strings.Join(files, ", ")+"?").
				Options(
					huh.NewOption("Update: write the changes to the Dockerfile and "+cfgFile, actionUpdate),
					huh.NewOption("Audit: show what would change and what isn't listed; write nothing", actionAudit),
				).
				Value(&action),
		),
	).WithTheme(huh.ThemeBase16()).
		WithKeyMap(keyMap()).
		WithAccessible(accessible)
	if err := form.Run(); err != nil {
		return "", err
	}
	return action, nil
}

// AskUpdateNow asks whether to update right after init; Yes is the default.
func (h *HuhRunPrompt) AskUpdateNow(cfgFile string) (bool, error) {
	accessible, _ = strconv.ParseBool(os.Getenv("ACCESSIBLE"))
	now := true
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Update the Dockerfile now, using " + cfgFile + "?").
				Description("Choose No to review it first (bump, target, skip).").
				Affirmative("Yes").
				Negative("No").
				Value(&now),
		),
	).WithTheme(huh.ThemeBase16()).
		WithKeyMap(keyMap()).
		WithAccessible(accessible)
	if err := form.Run(); err != nil {
		return false, err
	}
	return now, nil
}
