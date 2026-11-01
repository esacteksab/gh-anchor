// SPDX-License-Identifier: MIT

package utils

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateProjectFilePath(t *testing.T) {
	t.Chdir(t.TempDir())
	CreateLogger(false)

	for _, ok := range []string{"Dockerfile", "./Dockerfile", "build/Dockerfile", ".gh-anchor.toml", "Dockerfile.x"} {
		require.NoError(t, ValidateProjectFilePath(ok), ok)
	}
	for _, bad := range []string{"../Dockerfile", "build/../../Dockerfile", "/etc/passwd"} {
		assert.Error(t, ValidateProjectFilePath(bad), bad)
	}
}

func TestBuildVersion(t *testing.T) {
	v := BuildVersion("v1.2.3", "abc123", "2026-10-31", "goreleaser")
	assert.Contains(t, v, "v1.2.3")
	assert.Contains(t, v, "Commit: abc123")
	assert.Contains(t, v, "Built by: goreleaser")
	assert.Contains(t, v, "GOOS:")
}

func TestBackupFile(t *testing.T) {
	CreateLogger(false)
	dir := t.TempDir()
	src := dir + "/.gh-anchor.toml"
	require.NoError(t, os.WriteFile(src, []byte("# config\n"), 0o600))

	dest := src + "-202610310941"
	require.NoError(t, BackupFile(src, dest))
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "# config\n", string(got))

	require.ErrorIs(t, BackupFile(dir+"/missing", dest), os.ErrNotExist)
	require.Error(t, BackupFile(dir, dest), "a directory can't be backed up")
}
