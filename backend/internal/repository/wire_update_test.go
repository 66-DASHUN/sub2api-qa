package repository

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProvideImageUpdaterClientSkipsHelperInBinaryMode(t *testing.T) {
	client, err := ProvideImageUpdaterClient(&config.Config{Update: config.UpdateConfig{Mode: service.UpdateModeBinary, HelperTokenFile: filepath.Join(t.TempDir(), "missing")}})
	require.NoError(t, err)
	require.Nil(t, client)
}

func TestProvideImageUpdaterClientReadsTokenOnlyInContainerMode(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "client-token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("helper-secret\n"), 0600))

	client, err := ProvideImageUpdaterClient(&config.Config{Update: config.UpdateConfig{
		Mode:            service.UpdateModeContainer,
		HelperSocket:    filepath.Join(t.TempDir(), "updater.sock"),
		HelperTokenFile: tokenPath,
	}})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestProvideImageUpdaterClientRejectsMissingContainerToken(t *testing.T) {
	_, err := ProvideImageUpdaterClient(&config.Config{Update: config.UpdateConfig{
		Mode:            service.UpdateModeContainer,
		HelperSocket:    "/run/sub2api-image-updater/updater.sock",
		HelperTokenFile: filepath.Join(t.TempDir(), "missing"),
	}})
	require.Error(t, err)
}
