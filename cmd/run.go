// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/esacteksab/gh-anchor/config"
	"github.com/esacteksab/gh-anchor/updater"
	"github.com/esacteksab/gh-anchor/utils"
)

// runOptions are the inputs of a `gh anchor` run.
type runOptions struct {
	files          []string
	configPath     string
	explicitConfig bool // -c/--config was given, so the file must exist
	bump           bumpOptions
	audit          bool // --audit
	yes            bool // -y/--yes
	terminal       bool // stdin and stdout are a terminal
	sources        func() (updater.Sources, error)
}

// What a run does with what it found.
const (
	actionUpdate = "update"
	actionAudit  = "audit"
)

// errNoConfig is the --audit failure when there is no config to audit.
var errNoConfig = errors.New("no config to audit")

// runAnchor runs `gh anchor`: without a config it offers to run init (in a terminal);
// with one it asks whether to update or audit (in a terminal, unless --yes or --audit),
// then updates or audits the Dockerfiles.
func runAnchor(ctx context.Context, o runOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, err := loadForRun(o)
	fresh := false // init just wrote the config and the user chose to update now
	if err == nil && !cfg.Found {
		cfg, err = initFirst(ctx, o)
		fresh = true
	}
	if err != nil || cfg == nil {
		return logged(err)
	}

	plan, err := planBumps(o.bump, cfg, defaultBumpPicker)
	switch {
	case errors.Is(err, errCancelled):
		Logger.Print("Cancelled: nothing was changed.")
		return nil
	case err != nil:
		return logged(err)
	}

	action, err := chooseAction(o, cfg, fresh)
	switch {
	case errors.Is(err, errCancelled):
		Logger.Print("Cancelled: nothing was changed.")
		return nil
	case err != nil:
		return logged(err)
	}
	logBumps(plan, cfg)

	sources, err := o.sources()
	if err != nil {
		return logged(err)
	}
	u := &updater.Updater{
		Mode: plan.mode, Config: cfg, Sources: sources, Log: Logger,
		Bump: plan.tables, BumpOnly: plan.only,
	}
	if action == actionAudit {
		return auditFiles(ctx, u, cfg, o.files)
	}
	return updateFiles(ctx, u, cfg, o.files)
}

// chooseAction decides whether to update or audit: --audit audits; in a terminal the
// user is asked, unless --yes was given or they just chose to update after init.
func chooseAction(o runOptions, cfg *config.Config, fresh bool) (string, error) {
	switch {
	case o.audit:
		return actionAudit, nil
	case !o.terminal || o.yes || fresh:
		return actionUpdate, nil
	}
	action, err := defaultRunPrompt.AskAction(o.files, cfg.Path)
	if err != nil && isCancelled(err) {
		return "", errCancelled
	}
	return action, err
}

// logged logs err (if any, and not logged already) and returns it.
func logged(err error) error {
	var done alreadyLogged
	if err != nil && !errors.As(err, &done) {
		Logger.Errorf("❌  %v", err)
	}
	return err
}

// alreadyLogged wraps an error whose message was already logged.
type alreadyLogged struct{ error }

func (e alreadyLogged) Unwrap() error { return e.error }

// loadForRun checks the flags and paths and loads the config. The Dockerfiles must exist.
func loadForRun(o runOptions) (*config.Config, error) {
	if err := checkBumpFlags(o.bump); err != nil {
		return nil, err
	}
	for _, f := range append([]string{o.configPath}, o.files...) {
		if err := utils.ValidateProjectFilePath(f); err != nil {
			return nil, err
		}
	}
	cfg, err := config.Load(o.configPath, o.explicitConfig)
	if err != nil {
		return nil, err
	}
	logConfig(cfg)
	Logger.Debugf("Dockerfiles to process: %v", o.files)
	for _, path := range o.files {
		if _, err := os.Stat(path); err != nil {
			Logger.Errorf("❌  Failed to process %s: %v", path, err)
			if _, notTable := cfg.ResolveTable(path); notTable == nil && o.bump.set {
				Logger.Errorf("❌  To bump %s, attach it to the flag: -b=%s", path, path)
			}
			return nil, alreadyLogged{err}
		}
	}
	return cfg, nil
}

// initFirst runs init when there is no config, in a terminal, then asks whether to
// update right away. It returns the new config, or nil when the run should stop.
func initFirst(ctx context.Context, o runOptions) (*config.Config, error) {
	if !o.terminal || o.audit || o.yes {
		Logger.Warnf("⚠️  No %s found, so nothing was updated. Run `gh anchor init` to create one "+
			"that lists what to update.", o.configPath)
		if o.audit {
			return nil, errNoConfig
		}
		return nil, nil
	}
	Logger.Printf("No %s found: running gh anchor init first.", o.configPath)
	err := runInit(ctx, initOptions{
		files: o.files, configPath: o.configPath, interactive: true,
		out: os.Stdout, now: time.Now, sources: o.sources,
	})
	if err != nil || !doesExist(o.configPath) {
		return nil, err // init failed, was cancelled, or only printed the config
	}
	now, err := defaultRunPrompt.AskUpdateNow(o.configPath)
	if err != nil && !isCancelled(err) {
		return nil, err
	}
	if err != nil || !now {
		Logger.Printf("Review %s (bump, target, skip), then run gh anchor -u.", o.configPath)
		return nil, nil
	}
	return config.Load(o.configPath, true)
}
