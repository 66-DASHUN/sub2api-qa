package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/imageupdater"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type fakeHelperEngine struct {
	staged      bool
	stageCalls  []string
	applyCalled chan struct{}
	applyBlock  chan struct{}
}

type fakeReleaseClient struct {
	releases []*service.GitHubRelease
	err      error
}

func (c *fakeReleaseClient) FetchLatestRelease(context.Context, string) (*service.GitHubRelease, error) {
	return nil, errors.New("not implemented")
}

func (c *fakeReleaseClient) FetchRecentReleases(context.Context, string, int) ([]*service.GitHubRelease, error) {
	return c.releases, c.err
}

func (c *fakeReleaseClient) DownloadFile(context.Context, string, string, int64) error {
	return errors.New("not implemented")
}

func (c *fakeReleaseClient) FetchChecksumFile(context.Context, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (e *fakeHelperEngine) Stage(_ context.Context, version string) error {
	e.stageCalls = append(e.stageCalls, version)
	e.staged = true
	return nil
}

func (e *fakeHelperEngine) HasStaged() (bool, error) {
	return e.staged, nil
}

func (e *fakeHelperEngine) Apply(context.Context) error {
	if e.applyCalled != nil {
		close(e.applyCalled)
	}
	if e.applyBlock != nil {
		<-e.applyBlock
	}
	return nil
}

func TestHelperRejectsMissingOrWrongToken(t *testing.T) {
	engine := &fakeHelperEngine{}
	handler := NewHTTPHandler(engine, "expected-token")
	for _, token := range []string{"", "wrong-token"} {
		t.Run(token, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/stage", strings.NewReader(`{"version":"0.1.172"}`))
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			require.Equal(t, http.StatusUnauthorized, rec.Code)
		})
	}
}

func TestHelperStageRejectsUnknownJSONFields(t *testing.T) {
	engine := &fakeHelperEngine{}
	handler := NewHTTPHandler(engine, "expected-token")
	req := httptest.NewRequest(http.MethodPost, "/v1/stage", strings.NewReader(`{"version":"0.1.172","image":"evil"}`))
	req.Header.Set("Authorization", "Bearer expected-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, engine.stageCalls)
}

func TestHelperApplyAcknowledgesBeforeBackgroundApply(t *testing.T) {
	engine := &fakeHelperEngine{staged: true, applyCalled: make(chan struct{}), applyBlock: make(chan struct{})}
	handler := NewHTTPHandler(engine, "expected-token")
	req := httptest.NewRequest(http.MethodPost, "/v1/apply", nil)
	req.Header.Set("Authorization", "Bearer expected-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code)
	select {
	case <-engine.applyCalled:
	case <-time.After(time.Second):
		t.Fatal("background apply was not started")
	}
	close(engine.applyBlock)
}

func TestHelperApplyWithoutStagedReturnsConflict(t *testing.T) {
	engine := &fakeHelperEngine{}
	handler := NewHTTPHandler(engine, "expected-token")
	req := httptest.NewRequest(http.MethodPost, "/v1/apply", nil)
	req.Header.Set("Authorization", "Bearer expected-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusConflict, rec.Code)
}

func TestGitHubReleaseVerifierReturnsBoundCommit(t *testing.T) {
	const revision = "0123456789abcdef0123456789abcdef01234567"
	verifier := githubReleaseVerifier{client: &fakeReleaseClient{releases: []*service.GitHubRelease{{
		TagName: "qa-v0.1.172", TargetCommitish: revision,
	}}}}

	actual, err := verifier.Verify(context.Background(), "66-DASHUN/sub2api-qa", "qa-v", "0.1.172")
	require.NoError(t, err)
	require.Equal(t, revision, actual)
}

func TestGitHubReleaseVerifierRejectsBranchTarget(t *testing.T) {
	verifier := githubReleaseVerifier{client: &fakeReleaseClient{releases: []*service.GitHubRelease{{
		TagName: "qa-v0.1.172", TargetCommitish: "qa/grok-video-1080p",
	}}}}

	_, err := verifier.Verify(context.Background(), "66-DASHUN/sub2api-qa", "qa-v", "0.1.172")
	require.Error(t, err)
}

var _ imageupdater.CommandRunner
