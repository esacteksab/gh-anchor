// SPDX-License-Identifier: MIT

package utils

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// BackupFile copies source to dest, e.g. before overwriting a config file.
func BackupFile(source, dest string) error {
	sourceInfo, statErr := os.Stat(source)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return fmt.Errorf("backup source file %q does not exist: %w", source, os.ErrNotExist)
		}
		return fmt.Errorf("cannot access source file %q: %w", source, statErr)
	}
	if sourceInfo.IsDir() {
		return fmt.Errorf("backup source %q is a directory, expected a file", source)
	}

	srcFile, err := os.Open(filepath.Clean(source))
	if err != nil {
		return fmt.Errorf("failed to open source file %q: %w", source, err)
	}
	defer func() {
		if err := srcFile.Close(); err != nil {
			Logger.Errorf("Error closing source file %q: %v", source, err)
		}
	}()

	destFile, err := os.Create(filepath.Clean(dest))
	if err != nil {
		return fmt.Errorf("failed to create destination file %q: %w", dest, err)
	}
	defer func() {
		if err := destFile.Close(); err != nil {
			Logger.Errorf("Error closing destination file %q: %v", dest, err)
		}
	}()

	bytesCopied, err := io.Copy(destFile, srcFile)
	if err != nil {
		_ = os.Remove(dest) // Attempt cleanup
		return fmt.Errorf("failed to copy content from %q to %q: %w", source, dest, err)
	}
	Logger.Debugf("Copied %d bytes from %s to %s", bytesCopied, source, dest)

	if err = destFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync destination file %q: %w", dest, err)
	}
	Logger.Debugf("Successfully backed up %s to %s", source, dest)
	return nil
}
