package imageupdater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type commandCall struct {
	Name string
	Args []string
}

type commandResult struct {
	Output string
	Err    error
}

type fakeCommandRunner struct {
	mu      sync.Mutex
	calls   []commandCall
	results []commandResult
}

func (r *fakeCommandRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, commandCall{Name: name, Args: append([]string(nil), args...)})
	if len(r.results) == 0 {
		return "", nil
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result.Output, result.Err
}

type fakeReleaseVerifier struct {
	calls    []string
	err      error
	revision string
	entered  chan struct{}
	release  chan struct{}
}

func (v *fakeReleaseVerifier) Verify(_ context.Context, repo, prefix, version string) (string, error) {
	v.calls = append(v.calls, repo+"|"+prefix+"|"+version)
	if v.entered != nil {
		close(v.entered)
		<-v.release
	}
	if v.revision == "" {
		v.revision = "0123456789abcdef0123456789abcdef01234567"
	}
	return v.revision, v.err
}

type fakeHealthChecker struct {
	calls []string
	err   error
}

func (c *fakeHealthChecker) Wait(_ context.Context, url string) error {
	c.calls = append(c.calls, url)
	return c.err
}

func testEngineConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(envPath, []byte("DB_HOST=postgres\nSUB2API_VERSION=0.1.171\n"), 0600))
	return Config{
		ImageRepository:     "ghcr.io/66-dashun/sub2api",
		ExpectedSource:      "https://github.com/66-DASHUN/sub2api-qa",
		ComposeProjectDir:   dir,
		ComposeFile:         filepath.Join(dir, "docker-compose.yml"),
		ComposeOverrideFile: filepath.Join(dir, "docker-compose.qa-update.yml"),
		ComposeEnvFile:      envPath,
		ComposeService:      "sub2api",
		ReleaseRepo:         "66-DASHUN/sub2api-qa",
		ReleasePrefix:       "qa-v",
		StateFile:           filepath.Join(dir, "state.json"),
		HealthURL:           "http://127.0.0.1:8080/health",
	}
}

func newTestEngine(t *testing.T, runner *fakeCommandRunner, verifier *fakeReleaseVerifier, health *fakeHealthChecker) *Engine {
	t.Helper()
	engine, err := NewEngine(testEngineConfig(t), runner, verifier, health)
	require.NoError(t, err)
	return engine
}

func TestEngineStageUsesFixedCommandsAndWritesVerifiedState(t *testing.T) {
	runner := &fakeCommandRunner{results: []commandResult{
		{},
		{Output: "sha256:image-id\n"},
		{Output: "0.1.172\n"},
		{Output: "https://github.com/66-DASHUN/sub2api-qa\n"},
		{Output: "0123456789abcdef0123456789abcdef01234567\n"},
	}}
	verifier := &fakeReleaseVerifier{}
	engine := newTestEngine(t, runner, verifier, &fakeHealthChecker{})

	require.NoError(t, engine.Stage(context.Background(), "0.1.172"))

	require.Equal(t, []string{"66-DASHUN/sub2api-qa|qa-v|0.1.172"}, verifier.calls)
	image := "ghcr.io/66-dashun/sub2api:0.1.172"
	require.Equal(t, []commandCall{
		{Name: "docker", Args: []string{"pull", image}},
		{Name: "docker", Args: []string{"image", "inspect", "--format", "{{.Id}}", image}},
		{Name: "docker", Args: []string{"image", "inspect", "--format", `{{index .Config.Labels "org.opencontainers.image.version"}}`, image}},
		{Name: "docker", Args: []string{"image", "inspect", "--format", `{{index .Config.Labels "org.opencontainers.image.source"}}`, image}},
		{Name: "docker", Args: []string{"image", "inspect", "--format", `{{index .Config.Labels "org.opencontainers.image.revision"}}`, image}},
	}, runner.calls)

	state, err := NewStateStore(engine.Config().StateFile).Load()
	require.NoError(t, err)
	require.Equal(t, "0.1.172", state.Version)
	require.Equal(t, "0.1.171", state.PreviousVersion)
	require.Equal(t, "sha256:image-id", state.ImageID)
}

func TestEngineStageRejectsRevisionNotBoundToRelease(t *testing.T) {
	runner := &fakeCommandRunner{results: []commandResult{
		{},
		{Output: "sha256:image-id"},
		{Output: "0.1.172"},
		{Output: "https://github.com/66-DASHUN/sub2api-qa"},
		{Output: "ffffffffffffffffffffffffffffffffffffffff"},
	}}
	engine := newTestEngine(t, runner, &fakeReleaseVerifier{revision: "0123456789abcdef0123456789abcdef01234567"}, &fakeHealthChecker{})

	err := engine.Stage(context.Background(), "0.1.172")
	require.Error(t, err)
	require.Contains(t, err.Error(), "revision label")
	_, err = NewStateStore(engine.Config().StateFile).Load()
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestEngineStageRejectsMismatchedImageLabels(t *testing.T) {
	runner := &fakeCommandRunner{results: []commandResult{
		{},
		{Output: "sha256:image-id"},
		{Output: "0.1.999"},
		{Output: "https://github.com/66-DASHUN/sub2api-qa"},
	}}
	engine := newTestEngine(t, runner, &fakeReleaseVerifier{}, &fakeHealthChecker{})

	err := engine.Stage(context.Background(), "0.1.172")
	require.Error(t, err)
	require.Contains(t, err.Error(), "version label")
	_, err = NewStateStore(engine.Config().StateFile).Load()
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestEngineApplyRecreatesOnlyApplicationAndClearsState(t *testing.T) {
	runner := &fakeCommandRunner{results: []commandResult{
		{},
		{Output: "container-id\n"},
		{Output: "sha256:image-id\n"},
	}}
	health := &fakeHealthChecker{}
	engine := newTestEngine(t, runner, &fakeReleaseVerifier{}, health)
	store := NewStateStore(engine.Config().StateFile)
	require.NoError(t, store.Save(State{Version: "0.1.172", PreviousVersion: "0.1.171", ImageID: "sha256:image-id"}))

	require.NoError(t, engine.Apply(context.Background()))

	version, err := ReadEnvVersion(engine.Config().ComposeEnvFile)
	require.NoError(t, err)
	require.Equal(t, "0.1.172", version)
	composePrefix := []string{
		"compose", "--project-directory", engine.Config().ComposeProjectDir,
		"--env-file", engine.Config().ComposeEnvFile,
		"-f", engine.Config().ComposeFile,
		"-f", engine.Config().ComposeOverrideFile,
	}
	require.Equal(t, commandCall{Name: "docker", Args: append(append([]string{}, composePrefix...), "up", "-d", "--no-deps", "--force-recreate", "sub2api")}, runner.calls[0])
	require.Equal(t, commandCall{Name: "docker", Args: append(append([]string{}, composePrefix...), "ps", "-q", "sub2api")}, runner.calls[1])
	require.Equal(t, commandCall{Name: "docker", Args: []string{"inspect", "--format", "{{.Image}}", "container-id"}}, runner.calls[2])
	require.Equal(t, []string{"http://127.0.0.1:8080/health"}, health.calls)
	_, err = store.Load()
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestEngineApplyHealthFailureRestoresPreviousVersion(t *testing.T) {
	runner := &fakeCommandRunner{results: []commandResult{{}, {}}}
	health := &fakeHealthChecker{err: errors.New("health timeout")}
	engine := newTestEngine(t, runner, &fakeReleaseVerifier{}, health)
	store := NewStateStore(engine.Config().StateFile)
	require.NoError(t, store.Save(State{Version: "0.1.172", PreviousVersion: "0.1.171", ImageID: "sha256:image-id"}))

	err := engine.Apply(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "health timeout")
	version, readErr := ReadEnvVersion(engine.Config().ComposeEnvFile)
	require.NoError(t, readErr)
	require.Equal(t, "0.1.171", version)
	require.Len(t, runner.calls, 2)
	state, stateErr := store.Load()
	require.NoError(t, stateErr)
	require.Contains(t, state.LastError, "health timeout")
}

func TestEngineRejectsConcurrentOperations(t *testing.T) {
	verifier := &fakeReleaseVerifier{entered: make(chan struct{}), release: make(chan struct{})}
	runner := &fakeCommandRunner{results: []commandResult{
		{},
		{Output: "sha256:image-id"},
		{Output: "0.1.172"},
		{Output: "https://github.com/66-DASHUN/sub2api-qa"},
		{Output: "0123456789abcdef0123456789abcdef01234567"},
	}}
	engine := newTestEngine(t, runner, verifier, &fakeHealthChecker{})
	done := make(chan error, 1)
	go func() { done <- engine.Stage(context.Background(), "0.1.172") }()
	select {
	case <-verifier.entered:
	case <-time.After(time.Second):
		t.Fatal("stage did not enter verifier")
	}

	err := engine.Stage(context.Background(), "0.1.173")
	require.ErrorIs(t, err, ErrOperationInProgress)
	close(verifier.release)
	require.NoError(t, <-done)
}

func TestEngineApplyWithoutStateReturnsSentinel(t *testing.T) {
	engine := newTestEngine(t, &fakeCommandRunner{}, &fakeReleaseVerifier{}, &fakeHealthChecker{})
	err := engine.Apply(context.Background())
	require.ErrorIs(t, err, ErrNoStagedUpdate)
}

func TestEngineHasStagedReportsStatePresence(t *testing.T) {
	engine := newTestEngine(t, &fakeCommandRunner{}, &fakeReleaseVerifier{}, &fakeHealthChecker{})
	available, err := engine.HasStaged()
	require.NoError(t, err)
	require.False(t, available)
	require.NoError(t, NewStateStore(engine.Config().StateFile).Save(State{
		Version: "0.1.172", PreviousVersion: "0.1.171", ImageID: "sha256:image-id",
	}))
	available, err = engine.HasStaged()
	require.NoError(t, err)
	require.True(t, available)
}
