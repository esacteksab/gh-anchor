// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/esacteksab/gh-anchor/config"
	"github.com/esacteksab/gh-anchor/datasource"
	"github.com/esacteksab/gh-anchor/registry"
	"github.com/esacteksab/gh-anchor/updater"
	"github.com/esacteksab/gh-anchor/utils"
)

// Variables to hold build information, populated at build time.
var (
	Version     string   // Application version
	Date        string   // Build date
	Commit      string   // Git commit hash
	BuiltBy     string   // Builder identifier
	Update      bool     // Whether to update within the current major version
	BumpKeys    []string // -b/--bump: "all", or the tables to bump across major versions
	Interactive bool     // -i/--interactive: pick the tables to bump
	Audit       bool     // --audit: report, write nothing
	Yes         bool     // -y/--yes: update without asking
	ConfigPath  string   // Path to .gh-anchor.toml
	Verbose     bool     // Whether to log debug output (-v/--verbose)
	Debug       bool     // Alias for Verbose (--debug)
	Logger      *log.Logger
)

const (
	anchorDebug     = "ANCHOR_DEBUG"
	defaultFile     = "Dockerfile"
	envNPMRegistry  = "ANCHOR_NPM_REGISTRY"
	envNodeDist     = "ANCHOR_NODE_DIST_URL"
	envGitHubAPIURL = "GITHUB_API_URL"
)

func init() {
	rootCmd.Version = utils.BuildVersion(Version, Commit, Date, BuiltBy)
	rootCmd.SetVersionTemplate(`{{printf "Version %s" .Version}}`)
	rootCmd.Flags().BoolVarP(&Update, "update", "u", false,
		"update versions within the current major version and refresh digests")
	rootCmd.Flags().StringSliceVarP(&BumpKeys, "bump", "b", nil,
		"like --update, and also move to new major versions: everything, or only the tables named "+
			"with = (-b=golang,pnpm)")
	rootCmd.Flags().Lookup("bump").NoOptDefVal = bumpAll
	rootCmd.Flags().BoolVarP(&Interactive, "interactive", "i", false,
		"with -b: pick which tables to bump")
	rootCmd.Flags().BoolVar(&Audit, "audit", false,
		"show what would change and how the config differs from the Dockerfile; write nothing")
	rootCmd.Flags().
		BoolVarP(&Yes, "yes", "y", false, "don't ask whether to update or audit; update")
	rootCmd.MarkFlagsMutuallyExclusive("audit", "yes")
	rootCmd.PersistentFlags().StringVarP(&ConfigPath, "config", "c", config.DefaultFile,
		"path to the gh-anchor config file")
	// Declared before cobra adds --version, so -v means verbose rather than version.
	rootCmd.PersistentFlags().BoolVarP(&Verbose, "verbose", "v", false,
		"log every reference checked, what was looked up and the outcome (same as "+anchorDebug+"=true)")
	rootCmd.PersistentFlags().BoolVar(&Debug, "debug", false, "same as --verbose")
}

// enableVerbose switches the logger to debug level when -v, --verbose or --debug is set.
func enableVerbose(*cobra.Command, []string) {
	if Verbose || Debug {
		utils.CreateLogger(true)
		Logger = utils.Logger
		Logger.Debugf("Verbose logging enabled by flag")
	}
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It exits with status 1 if an error occurs.
func Execute() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	initialVerbose, _ := strconv.ParseBool(os.Getenv(anchorDebug))
	utils.CreateLogger(initialVerbose)
	Logger = utils.Logger
	Logger.Debugf("Logger created. Verbose based on %s: %t", anchorDebug, initialVerbose)
	return rootCmd.Execute()
}

// rootCmd represents the base command when called without any subcommands.
var rootCmd = &cobra.Command{
	Use:   "anchor [Dockerfile...]",
	Short: "anchor pins and updates versions and digests in Dockerfiles.",
	Long: `anchor pins and updates the versions and digests in a Dockerfile:

  - images in FROM and COPY --from (tag and sha256 digest)
  - ARG defaults, using the datasources configured in .gh-anchor.toml
  - package@version arguments to npm, npx, pnpm, yarn and corepack in RUN
    (pinned as package@version+sha512.<hex> for corepack)

Only what .gh-anchor.toml lists is updated. In a terminal, a run without one offers to
run "gh anchor init" first; a run with one asks whether to update or only audit
(--yes updates without asking; --audit reports what would change, writing nothing).

The config records each image's tag and digest and each package's version and hash,
and anchor keeps those records current. The Dockerfile wins: a record it doesn't match
(say, after a hand edit) is overwritten with the Dockerfile's value, with a warning.

With no flags, anchor only pins floating references. --update refreshes digests and
moves ARGs, COPY --from images and packages to the newest release within their current
major version; FROM base images keep their tag.

Bumping also allows new major versions and new FROM tags (only LTS tags for images with
lts = true). --update bumps the tables with bump = true. A bare -b/--bump bumps
everything; -b=golang,pnpm bumps only those tables (and those with bump = true), and
updates the rest. -b -i picks the tables to bump in a form. Name a table by its key, or
by its full name (images.node) when the key is used in more than one section.

A table's target (target = 24, target = "1.26") moves it to the newest release in that
line and keeps it there; with lts = true, to the next LTS line at or above. An image
pinned by digest only gets the tag that has that digest.`,
	SilenceUsage:     true,
	Args:             cobra.ArbitraryArgs,
	PersistentPreRun: enableVerbose,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			args = []string{defaultFile}
		}
		terminal := isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
		return runAnchor(cmd.Context(), runOptions{
			files:          args,
			configPath:     ConfigPath,
			explicitConfig: cmd.Flags().Changed("config"),
			bump: bumpOptions{
				set:         cmd.Flags().Changed("bump"),
				keys:        BumpKeys,
				interactive: Interactive,
				terminal:    terminal,
				update:      Update,
			},
			audit:    Audit,
			yes:      Yes,
			terminal: terminal,
			sources:  func() (updater.Sources, error) { return newLiveSources() },
		})
	},
}

// logBumps says what the run bumps.
func logBumps(plan bumpPlan, cfg *config.Config) {
	Logger.Debugf("Running in %s mode", plan.mode)
	switch {
	case plan.mode == updater.ModeBump:
		Logger.Print("Bumping everything (-b): new major versions and FROM tags are allowed")
	case plan.mode != updater.ModeUpdate:
	case len(plan.bumping(cfg)) > 0:
		Logger.Printf(
			"Bumping %s; updating the rest within its major version",
			strings.Join(plan.bumping(cfg), ", "),
		)
	case plan.only:
		Logger.Print("Nothing picked to bump; updating within major versions")
	}
}

// logConfig describes the loaded configuration at debug level.
func logConfig(cfg *config.Config) {
	if !cfg.Found {
		Logger.Debugf("No %s found", ConfigPath)
		return
	}
	Logger.Debugf("Loaded %s: args [%s], images [%s], packages [%s]", cfg.Path,
		strings.Join(sortedKeys(cfg.Args), ", "), strings.Join(sortedKeys(cfg.Images), ", "),
		strings.Join(sortedKeys(cfg.Packages), ", "))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// errConflict reports a table used with two different values.
var errConflict = errors.New("a table is used with two different values")

// fileResult is a processed Dockerfile, ready to write.
type fileResult struct {
	path string
	perm os.FileMode
	res  updater.Result
}

// updateFiles processes every Dockerfile, then writes them and the recorded values in
// the config. Recorded values a Dockerfile doesn't match are overwritten with the
// Dockerfile's. Nothing is written if a table is used with two different values.
func updateFiles(
	ctx context.Context,
	u *updater.Updater,
	cfg *config.Config,
	paths []string,
) error {
	results, failed, mismatched := processAll(ctx, u, paths)
	if mismatched {
		Logger.Errorf("❌  No changes made: one table in %s can only record one image or version. "+
			"Name the stages (FROM image AS name), or use one version.", cfg.Path)
		return errors.Join(append(failed, errConflict)...)
	}
	newConfig, recorded, err := recordValues(cfg, results)
	if err != nil {
		Logger.Errorf("❌  No changes made: %v", err)
		return errors.Join(append(failed, err)...)
	}

	total := 0
	for _, fr := range results {
		if err := writeDockerfile(fr); err != nil {
			Logger.Errorf("❌  %v", err)
			failed = append(failed, err)
			continue
		}
		total += len(fr.res.Changes)
	}
	if recorded > 0 {
		if err := writeFileKeepMode(cfg.Path, newConfig); err != nil {
			Logger.Errorf("❌  %v", err)
			return errors.Join(append(failed, err)...)
		}
		Logger.Printf("✅  Recorded %d value(s) in %s", recorded, cfg.Path)
	}
	Logger.Printf("Finished processing. Total references updated across all files: %d", total)
	return errors.Join(failed...)
}

// processAll computes the updates for every Dockerfile. It returns the results, the
// files that couldn't be read, and whether any table is used with two different values.
func processAll(
	ctx context.Context,
	u *updater.Updater,
	paths []string,
) ([]fileResult, []error, bool) {
	var results []fileResult
	var failed []error
	mismatched := false
	for _, path := range paths {
		fr, err := processFile(ctx, u, path)
		if err != nil {
			Logger.Errorf("❌  Failed to process %s: %v", path, err)
			failed = append(failed, err)
			continue
		}
		mismatched = mismatched || len(fr.res.Mismatches) > 0
		results = append(results, fr)
	}
	return results, failed, mismatched
}

// processFile computes the updates for one Dockerfile and reports its errors and
// mismatches.
func processFile(ctx context.Context, u *updater.Updater, path string) (fileResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileResult{}, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // validated by ValidateProjectFilePath
	if err != nil {
		return fileResult{}, err
	}
	Logger.Printf("Processing Dockerfile: %s", path)

	u.File = path
	res := u.Process(ctx, string(data))
	for _, m := range res.Drift {
		Logger.Warnf("⚠️  %v", m)
	}
	for _, m := range res.Mismatches {
		Logger.Errorf("❌  %v", m)
	}
	for _, e := range res.Errors {
		Logger.Errorf("❌  %v", e)
	}
	return fileResult{path: path, perm: info.Mode().Perm(), res: res}, nil
}

// writeDockerfile writes a processed Dockerfile if it changed and logs its summary.
func writeDockerfile(fr fileResult) error {
	if len(fr.res.Changes) > 0 {
		Logger.Printf("Changes in %s:", fr.path)
	}
	for _, c := range fr.res.Changes {
		Logger.Printf(
			"  line %d: %s %s → %s",
			c.Line,
			c.Label,
			updater.Shorten(c.From),
			updater.Shorten(c.To),
		)
	}
	if len(fr.res.Changes) == 0 {
		Logger.Printf("ℹ️  No references needed updating in %s", fr.path)
	} else {
		if err := os.WriteFile(fr.path, []byte(fr.res.Output), fr.perm); err != nil {
			return fmt.Errorf("writing %s: %w", fr.path, err)
		}
		Logger.Printf("✅  Updated %d reference(s) in %s", len(fr.res.Changes), fr.path)
	}
	Logger.Printf("Summary for %s: %s", fr.path, fr.res.Summary)
	return nil
}

// recordValues applies the values recorded while processing to the config text and
// returns it with the number of values that changed. The edited config is parsed
// again to make sure it holds exactly the recorded values.
func recordValues(cfg *config.Config, results []fileResult) (string, int, error) {
	type field struct{ table, name string }
	values := map[field]updater.Record{}
	var order []field
	for _, fr := range results {
		for _, r := range fr.res.Records {
			k := field{r.Table(), r.Field}
			prev, ok := values[k]
			switch {
			case !ok:
				values[k] = r
				order = append(order, k)
			case prev.Value != r.Value:
				return "", 0, fmt.Errorf(
					"[%s] would record %s = %q and %q; give each Dockerfile its own table",
					k.table,
					k.name,
					updater.Shorten(prev.Value),
					updater.Shorten(r.Value),
				)
			}
		}
	}
	if len(order) == 0 {
		return "", 0, nil
	}
	data, err := os.ReadFile(cfg.Path)
	if err != nil {
		return "", 0, err
	}
	text := string(data)
	for _, k := range order {
		r := values[k]
		if text, err = config.Set(text, []string{r.Section, r.Key}, r.Field, r.Value); err != nil {
			return "", 0, fmt.Errorf("%s: %w", cfg.Path, err)
		}
	}
	parsed, err := config.Parse([]byte(text))
	if err != nil {
		return "", 0, fmt.Errorf("%s: recording values made the file invalid: %w", cfg.Path, err)
	}
	for _, k := range order {
		r := values[k]
		if got, _ := parsed.Recorded(r.Section, r.Key, r.Field); got != r.Value {
			return "", 0, fmt.Errorf(
				"%s: couldn't record %s in [%s] (is the table written more than once?)",
				cfg.Path,
				r.Field,
				k.table,
			)
		}
	}
	return text, len(order), nil
}

// writeFileKeepMode replaces the contents of an existing file, keeping its permissions.
func writeFileKeepMode(path, content string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), info.Mode().Perm()); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// newLiveSources wires the real registry, npm, Node.js and GitHub clients.
func newLiveSources() (*updater.LiveSources, error) {
	httpClient, err := datasource.NewCachedHTTPClient()
	if err != nil {
		return nil, err
	}
	userAgent := datasource.AppName
	if Version != "" {
		userAgent += "/" + Version
	}

	reg := registry.NewClient(registry.LoadCredentials(os.Getenv), userAgent)
	reg.Warn = Logger.Warnf
	return &updater.LiveSources{
		Registry: reg,
		NPM: &datasource.NPM{
			BaseURL:   os.Getenv(envNPMRegistry),
			HTTP:      httpClient,
			UserAgent: userAgent,
		},
		Node: &datasource.Node{
			BaseURL:   os.Getenv(envNodeDist),
			HTTP:      httpClient,
			UserAgent: userAgent,
		},
		NewGitHub: func(ctx context.Context) (*datasource.GitHub, error) {
			token := os.Getenv("GITHUB_TOKEN")
			if token == "" {
				token = os.Getenv("GH_TOKEN")
			}
			gh, err := datasource.NewGitHub(
				httpClient,
				token,
				os.Getenv(envGitHubAPIURL),
				userAgent,
			)
			if err != nil {
				return nil, err
			}
			utils.LogRateLimitStatus(gh.AuthStatus(ctx))
			return gh, nil
		},
	}, nil
}
