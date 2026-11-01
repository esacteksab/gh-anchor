// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/esacteksab/gh-anchor/config"
	"github.com/esacteksab/gh-anchor/scaffold"
	"github.com/esacteksab/gh-anchor/updater"
	"github.com/esacteksab/gh-anchor/utils"
)

// Flags for `gh anchor init`.
var (
	initYes     bool
	initStdout  bool
	initOffline bool
)

const (
	configPerm   = 0o644
	backupLayout = "200601021504"
)

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().BoolVarP(&initYes, "yes", "y", false,
		"write the config without prompting (an existing file is backed up first)")
	initCmd.Flags().BoolVar(&initStdout, "stdout", false, "print the config instead of writing it")
	initCmd.Flags().BoolVar(&initOffline, "offline", false,
		"don't look anything up; decide from the Dockerfile alone")
	initCmd.MarkFlagsMutuallyExclusive("yes", "stdout")
}

// initCmd represents the init command.
var initCmd = &cobra.Command{
	Use:     "init [Dockerfile...]",
	Aliases: []string{"i"},
	Short:   "Generate a .gh-anchor.toml from your Dockerfile",
	Long: heredoc.Doc(
		`
		init scans the Dockerfile (or the Dockerfiles given) and generates a config file.
		gh-anchor only updates what the config lists, so init writes a table for:
		  - every image: FROM as [images.<stage name>] ("final" for an unnamed last stage,
		    "stage-N" for other unnamed stages), COPY --from as [images."<repository>"],
		    recording its image, tag and digest; Ubuntu and Node.js images get lts = true
		  - every package@version passed to npm, pnpm, yarn, corepack, ...: [packages.<name>],
		    recording its version and sha512 hash
		  - every ARG whose name contains VERSION or ends in _SHA or _COMMIT and whose
		    datasource it can tell from the name (NODE_VERSION, ...) or from where it is used
		Every setting is written out, defaults included. Init stops if two references would
		share a table with different values (e.g. two unnamed final stages).

		Unless --offline is given, each item is checked against npm, Node.js, GitHub and the
		image registries, and package hashes are looked up; if a lookup fails, the offline
		suggestion is kept with a note.

		In a terminal, an interactive prompt-based form lets you pick which detected items
		gh-anchor should manage (the rest get skip = true), then asks before creating or overwriting the file (an
		existing file is backed up as <file>-YYYYMMDDHHMM). Set ACCESSIBLE=1 for accessible
		mode. Without a terminal, the config is printed unless --yes is given.

		View docs at https://github.com/esacteksab/gh-anchor for more information.`,
	),
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			args = []string{defaultFile}
		}
		opts := initOptions{
			files:       args,
			configPath:  ConfigPath,
			yes:         initYes,
			stdout:      initStdout,
			offline:     initOffline,
			interactive: isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd()),
			out:         os.Stdout,
			now:         time.Now,
			sources:     func() (updater.Sources, error) { return newLiveSources() },
		}
		if err := runInit(cmd.Context(), opts); err != nil {
			Logger.Errorf("❌  %v", err)
			return err
		}
		return nil
	},
}

type initOptions struct {
	files       []string
	configPath  string
	yes         bool
	stdout      bool
	offline     bool
	interactive bool
	out         io.Writer // where the config is printed
	now         func() time.Time
	sources     func() (updater.Sources, error)
}

// runInit detects, verifies, asks (when interactive) and writes or prints the config.
func runInit(ctx context.Context, o initOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	plan, err := detectPlan(ctx, o)
	if err != nil {
		return err
	}

	switch {
	case o.stdout:
		return printConfig(o.out, plan)
	case o.yes:
		return writeConfig(o, plan, defaultFileChecker.DoesExist(o.configPath))
	case !o.interactive:
		Logger.Printf(
			"ℹ️  No terminal to prompt in: printing the config. Use --yes to write %s.",
			o.configPath,
		)
		return printConfig(o.out, plan)
	}

	if err := defaultItemPicker.PickItems(plan); err != nil {
		if isCancelled(err) {
			Logger.Error("Configuration cancelled by user.")
			return nil // Exit without error code
		}
		return err
	}
	configExists, createFile, err := createOrOverwrite(
		o.configPath,
		defaultFileChecker,
		defaultUserPrompt,
	)
	if err != nil {
		if isCancelled(err) {
			Logger.Error("Configuration cancelled by user.")
			return nil
		}
		return err
	}
	if !createFile {
		// If the user chose not to create the file, just display the config.
		return printConfig(o.out, plan)
	}
	return writeConfig(o, plan, configExists)
}

// errConflicts reports tables that two references would share with different values.
var errConflicts = errors.New("no config written: some tables would record two different values")

// detectPlan reads the Dockerfiles, detects suggestions and verifies them.
func detectPlan(ctx context.Context, o initOptions) (*scaffold.Plan, error) {
	for _, f := range append([]string{o.configPath}, o.files...) {
		if err := utils.ValidateProjectFilePath(f); err != nil {
			return nil, err
		}
	}
	sources := make([]scaffold.Source, 0, len(o.files))
	for _, f := range o.files {
		data, err := os.ReadFile(f) //nolint:gosec // validated by ValidateProjectFilePath
		if err != nil {
			return nil, err
		}
		sources = append(sources, scaffold.Source{Path: f, Content: string(data)})
	}

	plan := scaffold.Detect(sources)
	Logger.Printf("Scanned %s: %s", strings.Join(o.files, ", "), describePlan(plan))
	if !o.offline {
		src, err := o.sources()
		if err != nil {
			Logger.Warnf("⚠️  Lookups unavailable (%v); using offline defaults", err)
		} else {
			Logger.Printf("Checking %d item(s) against npm, Node.js, GitHub and the image registries…", len(plan.Suggestions))
			plan.Verify(ctx, src)
		}
	}
	for _, s := range plan.Suggestions {
		Logger.Debugf("%s: %s", s.Key(), strings.Join(append([]string{s.Reason}, s.Notes...), "; "))
	}
	if len(plan.Conflicts) > 0 {
		for _, c := range plan.Conflicts {
			Logger.Errorf("❌  %v", c)
		}
		return nil, errConflicts
	}
	return plan, nil
}

// isCancelled reports whether the user quit a form (q, esc or ctrl+c).
func isCancelled(err error) bool {
	return errors.Is(err, huh.ErrUserAborted) ||
		strings.Contains(err.Error(), "canceled") || strings.Contains(err.Error(), "quit")
}

func describePlan(p *scaffold.Plan) string {
	counts := map[string]int{}
	for _, s := range p.Suggestions {
		counts[s.Section]++
	}
	return fmt.Sprintf(
		"%d ARG(s) with a datasource, %d image(s), %d package(s), %d ARG(s) without a detected datasource",
		counts[scaffold.SectionArgs],
		counts[scaffold.SectionImages],
		counts[scaffold.SectionPackages],
		len(p.Unmatched),
	)
}

// render renders the plan and checks that gh-anchor can load the result.
func render(p *scaffold.Plan) (string, error) {
	out := scaffold.Render(p)
	if _, err := config.Parse([]byte(out)); err != nil {
		return "", fmt.Errorf("generated config is invalid (please report this): %w", err)
	}
	return out, nil
}

func printConfig(w io.Writer, p *scaffold.Plan) error {
	out, err := render(p)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, out)
	return err
}

// writeConfig writes the config, backing up an existing file as <path>-YYYYMMDDHHMM.
func writeConfig(o initOptions, p *scaffold.Plan, configExists bool) error {
	out, err := render(p)
	if err != nil {
		return err
	}
	if configExists {
		// When overwriting an existing config, create a backup first.
		backup := o.configPath + "-" + o.now().Local().Format(backupLayout)
		if err := utils.BackupFile(o.configPath, backup); err != nil {
			return err
		}
		Logger.Infof("Backup file %s created", backup)
	}
	if err := os.WriteFile(o.configPath, []byte(out), configPerm); err != nil {
		return fmt.Errorf("error writing config file: %w", err)
	}
	Logger.Infof("Config file %s created", o.configPath)
	return nil
}
