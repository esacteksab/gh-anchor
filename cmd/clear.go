// SPDX-License-Identifier: MIT

package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/esacteksab/gh-anchor/datasource"
)

var force bool // Flag variable

func init() {
	rootCmd.AddCommand(clearCmd)
	// Define the --force / -f flag
	clearCmd.Flags().
		BoolVarP(&force,
			"force",
			"f",
			false,
			"force deletion without confirmation")
}

var clearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear local application cache",
	Long: `Deletes the gh-anchor cache directory located within the user's
standard cache location (e.g., $XDG_CACHE_HOME/gh-anchor on Linux).
Requires the --force flag to proceed.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cachePath, err := datasource.CacheDir()
		if err != nil {
			return err
		}

		// 1. Check if the target path exists and is accessible
		_, err = os.Stat(cachePath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Directory doesn't exist - this is a successful 'clear' state.
				fmt.Printf("Cache directory '%s' does not exist. Nothing to clear.\n", cachePath)
				return nil
			}
			return fmt.Errorf("failed to check status of cache directory '%s': %w", cachePath, err)
		}

		// 2. If it exists, require the force flag
		if !force {
			return fmt.Errorf(
				"cache directory '%s' exists. Use the -f or --force flag to confirm deletion",
				cachePath,
			)
		}

		// 3. Force flag provided, proceed with deletion
		fmt.Printf("Removing cache directory '%s'...\n", cachePath)
		if err := os.RemoveAll(cachePath); err != nil {
			return fmt.Errorf("failed removing cache directory '%s': %w", cachePath, err)
		}

		// 4. Verify deletion
		_, err = os.Stat(cachePath)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			fmt.Printf("Cache directory '%s' removed successfully.\n", cachePath)
		case err != nil:
			return fmt.Errorf(
				"removed '%s', but failed to verify removal status: %w",
				cachePath,
				err,
			)
		default:
			return fmt.Errorf(
				"attempted to remove cache directory '%s', but it still exists",
				cachePath,
			)
		}
		return nil
	},
}
