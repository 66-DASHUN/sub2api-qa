package imageupdater

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeVersionRejectsImageInjection(t *testing.T) {
	for _, input := range []string{
		"",
		"latest",
		"v0.1.172",
		"0.1.172:bad",
		"../../x",
		"0.1.172; rm -rf /",
		"0.1",
		"0.1.172-rc1",
	} {
		_, err := NormalizeVersion(input)
		require.Error(t, err, input)
	}

	version, err := NormalizeVersion("0.1.172")
	require.NoError(t, err)
	require.Equal(t, "0.1.172", version)
}

func TestImageReferenceUsesFixedRepository(t *testing.T) {
	ref, err := ImageReference("ghcr.io/66-dashun/sub2api", "0.1.172")
	require.NoError(t, err)
	require.Equal(t, "ghcr.io/66-dashun/sub2api:0.1.172", ref)

	_, err = ImageReference("ghcr.io/66-dashun/sub2api", "latest")
	require.Error(t, err)
}
