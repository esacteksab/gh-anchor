// SPDX-License-Identifier: MIT

package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/log"
)

// Logger is the application-wide logger, configured by CreateLogger.
var Logger *log.Logger

// ValidateProjectFilePath checks that filePath stays inside the current working
// directory (the project root) and contains no '..' components.
//
// -filePath: The path to a Dockerfile or config file.
// Returns: An error if the path is invalid, traverses outside project root, or cannot be resolved.
func ValidateProjectFilePath(filePath string) error {
	// Reject explicit '..' components as defense in depth against path traversal.
	for part := range strings.SplitSeq(filepath.ToSlash(filePath), "/") {
		if part == ".." {
			return fmt.Errorf("path %q contains '..'", filePath)
		}
	}

	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("could not get absolute path for %q: %w", filePath, err)
	}

	// The current working directory is assumed to be the project root.
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("could not get working directory: %w", err)
	}

	if !strings.HasPrefix(absPath, wd+string(filepath.Separator)) && absPath != wd {
		return fmt.Errorf("path %q resolves outside project root %q", filePath, wd)
	}
	return nil
}

// LogRateLimitStatus reports which GitHub API rate limit applies.
func LogRateLimitStatus(limitType string) {
	switch limitType {
	case "authenticated":
		Logger.Print("🔧  Authenticated GitHub API access in effect.")
	case "unauthenticated":
		Logger.Print(
			"⚠️  Unauthenticated GitHub API access in effect (lower rate limit).",
		)
	default:
		Logger.Print("ℹ️  Could not determine GitHub API authentication status.")
	}
}
