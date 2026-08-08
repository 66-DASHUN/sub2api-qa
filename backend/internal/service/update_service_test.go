//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type updateServiceCacheStub struct {
	data string
}

func (s *updateServiceCacheStub) GetUpdateInfo(context.Context) (string, error) {
	if s.data == "" {
		return "", errors.New("cache miss")
	}
	return s.data, nil
}

func (s *updateServiceCacheStub) SetUpdateInfo(_ context.Context, data string, _ time.Duration) error {
	s.data = data
	return nil
}

type updateServiceGitHubClientStub struct {
	release        *GitHubRelease
	recentReleases []*GitHubRelease
	recentErr      error
	latestCalls    int
	recentCalls    int
	recentRepo     string
}

type containerUpdateClientStub struct {
	staged     []string
	stageErr   error
	applyErr   error
	applyCalls int
}

func (s *containerUpdateClientStub) Stage(_ context.Context, version string) error {
	s.staged = append(s.staged, version)
	return s.stageErr
}

func (s *containerUpdateClientStub) Apply(context.Context) error {
	s.applyCalls++
	return s.applyErr
}

func (s *updateServiceGitHubClientStub) FetchLatestRelease(context.Context, string) (*GitHubRelease, error) {
	s.latestCalls++
	return s.release, nil
}

func (s *updateServiceGitHubClientStub) FetchRecentReleases(_ context.Context, repo string, _ int) ([]*GitHubRelease, error) {
	s.recentCalls++
	s.recentRepo = repo
	return s.recentReleases, s.recentErr
}

func (s *updateServiceGitHubClientStub) DownloadFile(context.Context, string, string, int64) error {
	panic("DownloadFile should not be called when no update is available")
}

func (s *updateServiceGitHubClientStub) FetchChecksumFile(context.Context, string) ([]byte, error) {
	panic("FetchChecksumFile should not be called when no update is available")
}

func TestUpdateServicePerformUpdateNoUpdateReturnsSentinel(t *testing.T) {
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{
			release: &GitHubRelease{
				TagName: "v0.1.132",
				Name:    "v0.1.132",
			},
		},
		"0.1.132",
		"release",
	)

	err := svc.PerformUpdate(context.Background())

	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNoUpdateAvailable))
	require.ErrorIs(t, err, ErrNoUpdateAvailable)
}

func TestUpdateInfoHasContainerMode(t *testing.T) {
	info := &UpdateInfo{UpdateMode: "container"}
	require.Equal(t, "container", info.UpdateMode)
}

func newContainerUpdateTestService(current string, releases []*GitHubRelease) *UpdateService {
	return newContainerUpdateTestServiceWithHelper(current, &containerUpdateClientStub{}, releases)
}

func newContainerUpdateTestServiceWithHelper(current string, helper ContainerUpdateClient, releases []*GitHubRelease) *UpdateService {
	return NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{recentReleases: releases},
		current,
		"release",
		UpdateServiceOptions{
			Mode:             UpdateModeContainer,
			ReleaseRepo:      "66-DASHUN/sub2api-qa",
			ReleaseTagPrefix: "qa-v",
			ContainerClient:  helper,
		},
	)
}

func TestContainerUpdateInfoFiltersQAReleases(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.199", Name: "official"},
		{TagName: "QA", Name: "mutable"},
		{TagName: "qa-v0.1.172", Name: "QA 0.1.172"},
		{TagName: "qa-v0.1.171", Name: "current"},
		{TagName: "qa-v0.1.173-rc1", Prerelease: true},
		{TagName: "qa-v0.1.170", Draft: true},
	}
	svc := newContainerUpdateTestService("0.1.171", releases)
	info, err := svc.CheckUpdate(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, UpdateModeContainer, info.UpdateMode)
	require.Equal(t, "66-DASHUN/sub2api-qa", info.UpdateSource)
	require.Equal(t, "0.1.172", info.LatestVersion)
	require.True(t, info.HasUpdate)
	require.NotNil(t, info.ReleaseInfo)
	require.Equal(t, "qa-v0.1.172", info.ReleaseInfo.TagName)
}

func TestContainerPerformUpdateStagesOnlyNormalizedVersion(t *testing.T) {
	helper := &containerUpdateClientStub{}
	svc := newContainerUpdateTestServiceWithHelper("0.1.171", helper, []*GitHubRelease{{TagName: "qa-v0.1.172"}})
	require.NoError(t, svc.PerformUpdate(context.Background()))
	require.Equal(t, []string{"0.1.172"}, helper.staged)
}

func TestContainerRestartDelegatesApply(t *testing.T) {
	helper := &containerUpdateClientStub{}
	svc := newContainerUpdateTestServiceWithHelper("0.1.171", helper, nil)
	handled, err := svc.Restart(context.Background())
	require.True(t, handled)
	require.NoError(t, err)
	require.Equal(t, 1, helper.applyCalls)
}

func TestContainerRestartSurfacesNoStagedUpdate(t *testing.T) {
	helper := &containerUpdateClientStub{applyErr: ErrNoStagedContainerUpdate}
	svc := newContainerUpdateTestServiceWithHelper("0.1.171", helper, nil)
	handled, err := svc.Restart(context.Background())
	require.True(t, handled)
	require.ErrorIs(t, err, ErrNoStagedContainerUpdate)
}

func TestContainerCacheDoesNotReuseBinaryRelease(t *testing.T) {
	cache := &updateServiceCacheStub{}
	binary := NewUpdateService(
		cache,
		&updateServiceGitHubClientStub{release: &GitHubRelease{TagName: "v0.1.180"}},
		"0.1.171",
		"release",
	)
	_, err := binary.CheckUpdate(context.Background(), true)
	require.NoError(t, err)

	github := &updateServiceGitHubClientStub{recentReleases: []*GitHubRelease{{TagName: "qa-v0.1.172"}}}
	container := NewUpdateService(
		cache,
		github,
		"0.1.171",
		"release",
		UpdateServiceOptions{
			Mode:             UpdateModeContainer,
			ReleaseRepo:      "66-DASHUN/sub2api-qa",
			ReleaseTagPrefix: "qa-v",
			ContainerClient:  &containerUpdateClientStub{},
		},
	)
	info, err := container.CheckUpdate(context.Background(), false)
	require.NoError(t, err)
	require.Equal(t, "0.1.172", info.LatestVersion)
	require.Equal(t, 1, github.recentCalls)
	require.Equal(t, "66-DASHUN/sub2api-qa", github.recentRepo)
}

func newRollbackTestService(current string, releases []*GitHubRelease) *UpdateService {
	return NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{recentReleases: releases},
		current,
		"release",
	)
}

func TestUpdateServiceListRollbackVersionsFiltersAndCaps(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.148", PublishedAt: "2026-07-09T00:00:00Z"},                       // newer than current: excluded
		{TagName: "v0.1.147", PublishedAt: "2026-07-08T00:00:00Z"},                       // current: excluded
		{TagName: "v0.1.146-rc1", PublishedAt: "2026-07-07T12:00:00Z", Prerelease: true}, // prerelease: excluded
		{TagName: "v0.1.146", PublishedAt: "2026-07-07T00:00:00Z"},
		{TagName: "v0.1.145", PublishedAt: "2026-07-06T00:00:00Z", Draft: true}, // draft: excluded
		{TagName: "v0.1.144", PublishedAt: "2026-07-05T00:00:00Z"},
		{TagName: "v0.1.144", PublishedAt: "2026-07-05T00:00:00Z"}, // duplicate: excluded
		{TagName: "v0.1.143", PublishedAt: "2026-07-04T00:00:00Z"},
		{TagName: "v0.1.142", PublishedAt: "2026-07-03T00:00:00Z"}, // beyond cap of 3: excluded
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Len(t, versions, 3)
	require.Equal(t, "0.1.146", versions[0].Version)
	require.Equal(t, "0.1.144", versions[1].Version)
	require.Equal(t, "0.1.143", versions[2].Version)
}

func TestUpdateServiceListRollbackVersionsSortsUnorderedInput(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.144"},
		{TagName: "v0.1.146"},
		{TagName: "v0.1.145"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Len(t, versions, 3)
	require.Equal(t, "0.1.146", versions[0].Version)
	require.Equal(t, "0.1.145", versions[1].Version)
	require.Equal(t, "0.1.144", versions[2].Version)
}

func TestUpdateServiceListRollbackVersionsEmptyWhenNoneOlder(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.147"},
		{TagName: "v0.1.148"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Empty(t, versions)
}

func TestUpdateServiceListRollbackVersionsPropagatesFetchError(t *testing.T) {
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{recentErr: errors.New("github unavailable")},
		"0.1.147",
		"release",
	)

	_, err := svc.ListRollbackVersions(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "github unavailable")
}

func TestUpdateServiceRollbackToVersionRejectsDisallowedTargets(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.148"},
		{TagName: "v0.1.147"},
		{TagName: "v0.1.146"},
		{TagName: "v0.1.145"},
		{TagName: "v0.1.144"},
		{TagName: "v0.1.143"},
		{TagName: "v0.1.142"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	for _, target := range []string{
		"",         // empty
		"0.1.147",  // current version
		"v0.1.147", // current version with prefix
		"0.1.148",  // newer than current
		"0.1.142",  // older than the 3 most recent
		"9.9.9",    // nonexistent
	} {
		err := svc.RollbackToVersion(context.Background(), target)
		require.ErrorIs(t, err, ErrRollbackVersionNotAllowed, "target %q should be rejected", target)
	}
}

func TestUpdateServiceRollbackToVersionAcceptsVPrefix(t *testing.T) {
	// No platform asset in the release: the target passes the allowlist check
	// and fails later at asset lookup, proving the version itself was accepted.
	releases := []*GitHubRelease{
		{TagName: "v0.1.147"},
		{TagName: "v0.1.146"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	err := svc.RollbackToVersion(context.Background(), "v0.1.146")

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRollbackVersionNotAllowed)
	require.Contains(t, err.Error(), "no compatible release found")
}
