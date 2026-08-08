package imageupdater

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateEnvVersionPreservesUnrelatedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	original := "DB_HOST=postgres\nSUB2API_VERSION=0.1.171\n# keep this comment\nREDIS_HOST=redis\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0600))

	require.NoError(t, UpdateEnvVersion(path, "0.1.172"))

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "DB_HOST=postgres\nSUB2API_VERSION=0.1.172\n# keep this comment\nREDIS_HOST=redis\n", string(body))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm()&0600)
}

func TestUpdateEnvVersionAppendsMissingKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	require.NoError(t, os.WriteFile(path, []byte("DB_HOST=postgres\n"), 0640))

	require.NoError(t, UpdateEnvVersion(path, "0.1.172"))

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "DB_HOST=postgres\nSUB2API_VERSION=0.1.172\n", string(body))
}

func TestUpdateEnvVersionRejectsDuplicateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	require.NoError(t, os.WriteFile(path, []byte("SUB2API_VERSION=0.1.170\nSUB2API_VERSION=0.1.171\n"), 0600))

	err := UpdateEnvVersion(path, "0.1.172")
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate")
}

func TestReadEnvVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	require.NoError(t, os.WriteFile(path, []byte("A=1\nSUB2API_VERSION=0.1.171\n"), 0600))

	version, err := ReadEnvVersion(path)
	require.NoError(t, err)
	require.Equal(t, "0.1.171", version)
}
