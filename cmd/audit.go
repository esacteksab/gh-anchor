// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/esacteksab/gh-anchor/config"
	"github.com/esacteksab/gh-anchor/updater"
)

// errAuditFailed reports an audit that couldn't check everything (a lookup failed).
var errAuditFailed = errors.New("the audit couldn't check everything")

// auditTally counts what an audit found.
type auditTally struct {
	changes, recorded, drift, conflicts, unlisted, unused, failed int
}

func (t auditTally) String() string {
	return fmt.Sprintf(
		"%d change(s) pending, %d value(s) to record (%d out of date), %d conflict(s), "+
			"%d not listed, %d unused table(s), %d failed",
		t.changes,
		t.recorded,
		t.drift,
		t.conflicts,
		t.unlisted,
		t.unused,
		t.failed,
	)
}

func (t auditTally) clean() bool { return t == auditTally{} }

// findings reports whether the audit found anything to act on, failures aside.
func (t auditTally) findings() bool {
	t.failed = 0
	return !t.clean()
}

// auditFiles processes the Dockerfiles like an update, then reports what it would
// change and how the config differs from the Dockerfiles. It writes nothing. Findings
// are information, not errors: only a failed lookup or file makes it fail.
func auditFiles(ctx context.Context, u *updater.Updater, cfg *config.Config, paths []string) error {
	results, failed, mismatched := processAll(ctx, u, paths)
	tally := auditTally{failed: len(failed)}
	used := map[string]bool{}
	for _, fr := range results {
		auditFile(fr, cfg, &tally)
		for _, t := range fr.res.Tables {
			used[t] = true
		}
	}
	if !mismatched {
		if _, n, err := recordValues(cfg, results); err != nil {
			Logger.Errorf("❌  %v", err)
			tally.failed++
		} else {
			tally.recorded = n
		}
	}
	for _, t := range cfg.Tables() {
		if !used[t.Name()] {
			Logger.Printf("Unused: [%s] matches nothing in %v", t.Name(), paths)
			tally.unused++
		}
	}
	Logger.Printf("Audit: %s. Nothing was written.", tally)
	switch {
	case tally.clean():
		Logger.Print("✅  Everything is current and listed.")
	case tally.findings():
		Logger.Info(
			"ℹ️  Run gh anchor again and choose Update (or pass --yes) to apply the changes.",
		)
	}
	if tally.failed > 0 {
		return errors.Join(append(failed, errAuditFailed)...)
	}
	return nil
}

// auditFile reports one Dockerfile's pending changes, unlisted references and summary.
func auditFile(fr fileResult, cfg *config.Config, tally *auditTally) {
	tally.conflicts += len(fr.res.Mismatches)
	tally.drift += len(fr.res.Drift)
	tally.failed += len(fr.res.Errors)
	if len(fr.res.Changes) > 0 {
		Logger.Printf("Would change in %s:", fr.path)
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
	tally.changes += len(fr.res.Changes)
	if len(fr.res.Unlisted) > 0 {
		Logger.Printf("Not in %s (run gh anchor init to add them):", cfg.Path)
	}
	for _, un := range fr.res.Unlisted {
		Logger.Printf(
			"  line %d: %s %s: no [%s] table",
			un.Line,
			un.Label,
			updater.Shorten(un.Value),
			un.Table,
		)
	}
	tally.unlisted += len(fr.res.Unlisted)
	if len(fr.res.Records) > 0 {
		Logger.Printf("Would record in %s:", cfg.Path)
	}
	for _, r := range fr.res.Records {
		Logger.Printf("  [%s] %s = %q", r.Table(), r.Field, updater.Shorten(r.Value))
	}
	if len(fr.res.Mismatches) == 0 {
		Logger.Printf("Summary for %s: %s", fr.path, fr.res.Summary)
	}
}
