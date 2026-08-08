package imageupdater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	ErrOperationInProgress = errors.New("image update operation is already in progress")
	ErrNoStagedUpdate      = errors.New("no staged image update")
)

const (
	versionLabelFormat = `{{index .Config.Labels "org.opencontainers.image.version"}}`
	sourceLabelFormat  = `{{index .Config.Labels "org.opencontainers.image.source"}}`
)

type Config struct {
	ImageRepository   string
	ExpectedSource    string
	ComposeProjectDir string
	ComposeFile       string
	ComposeEnvFile    string
	ComposeService    string
	ReleaseRepo       string
	ReleasePrefix     string
	StateFile         string
	HealthURL         string
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

type ReleaseVerifier interface {
	Verify(ctx context.Context, repo, prefix, version string) error
}

type HealthChecker interface {
	Wait(ctx context.Context, url string) error
}

type Engine struct {
	config   Config
	runner   CommandRunner
	releases ReleaseVerifier
	health   HealthChecker
	state    *StateStore
	mu       sync.Mutex
}

func NewEngine(config Config, runner CommandRunner, releases ReleaseVerifier, health HealthChecker) (*Engine, error) {
	if runner == nil || releases == nil || health == nil {
		return nil, fmt.Errorf("image updater dependencies are required")
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	return &Engine{
		config: config, runner: runner, releases: releases, health: health,
		state: NewStateStore(config.StateFile),
	}, nil
}

func (e *Engine) Config() Config {
	return e.config
}

func (e *Engine) HasStaged() (bool, error) {
	_, err := e.state.Load()
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (e *Engine) Stage(ctx context.Context, version string) error {
	if !e.mu.TryLock() {
		return ErrOperationInProgress
	}
	defer e.mu.Unlock()
	normalized, err := NormalizeVersion(version)
	if err != nil {
		return err
	}
	if err := e.releases.Verify(ctx, e.config.ReleaseRepo, e.config.ReleasePrefix, normalized); err != nil {
		return fmt.Errorf("verify QA release: %w", err)
	}
	image, err := ImageReference(e.config.ImageRepository, normalized)
	if err != nil {
		return err
	}
	if _, err := e.run(ctx, "docker", "pull", image); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	imageID, err := e.run(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image)
	if err != nil {
		return fmt.Errorf("inspect image ID: %w", err)
	}
	imageID = strings.TrimSpace(imageID)
	if !strings.HasPrefix(imageID, "sha256:") {
		return fmt.Errorf("invalid image ID")
	}
	versionLabel, err := e.run(ctx, "docker", "image", "inspect", "--format", versionLabelFormat, image)
	if err != nil {
		return fmt.Errorf("inspect image version label: %w", err)
	}
	if strings.TrimSpace(versionLabel) != normalized {
		return fmt.Errorf("image version label mismatch")
	}
	sourceLabel, err := e.run(ctx, "docker", "image", "inspect", "--format", sourceLabelFormat, image)
	if err != nil {
		return fmt.Errorf("inspect image source label: %w", err)
	}
	if strings.TrimSpace(sourceLabel) != e.config.ExpectedSource {
		return fmt.Errorf("image source label mismatch")
	}
	previousVersion, err := ReadEnvVersion(e.config.ComposeEnvFile)
	if err != nil {
		return fmt.Errorf("read current version: %w", err)
	}
	return e.state.Save(State{Version: normalized, PreviousVersion: previousVersion, ImageID: imageID})
}

func (e *Engine) Apply(ctx context.Context) error {
	if !e.mu.TryLock() {
		return ErrOperationInProgress
	}
	defer e.mu.Unlock()
	state, err := e.state.Load()
	if errors.Is(err, os.ErrNotExist) {
		return ErrNoStagedUpdate
	}
	if err != nil {
		return fmt.Errorf("load staged update: %w", err)
	}
	if err := e.applyState(ctx, state); err == nil {
		return e.state.Clear()
	} else {
		primaryErr := err
		recoveryErr := e.recoverState(ctx, state)
		if recoveryErr != nil {
			state.LastError = fmt.Sprintf("apply failed: %v; recovery failed: %v", primaryErr, recoveryErr)
		} else {
			state.LastError = primaryErr.Error()
		}
		_ = e.state.Save(state)
		if recoveryErr != nil {
			return fmt.Errorf("apply failed: %v; recovery failed: %w", primaryErr, recoveryErr)
		}
		return primaryErr
	}
}

func (e *Engine) applyState(ctx context.Context, state State) error {
	if err := UpdateEnvVersion(e.config.ComposeEnvFile, state.Version); err != nil {
		return fmt.Errorf("set staged version: %w", err)
	}
	if err := e.recreateApplication(ctx); err != nil {
		return err
	}
	if err := e.health.Wait(ctx, e.config.HealthURL); err != nil {
		return fmt.Errorf("health check: %w", err)
	}
	containerID, err := e.run(ctx, "docker", append(e.composePrefix(), "ps", "-q", e.config.ComposeService)...)
	if err != nil {
		return fmt.Errorf("resolve application container: %w", err)
	}
	containerID = strings.TrimSpace(containerID)
	if containerID == "" {
		return fmt.Errorf("application container ID is empty")
	}
	runningImageID, err := e.run(ctx, "docker", "inspect", "--format", "{{.Image}}", containerID)
	if err != nil {
		return fmt.Errorf("inspect running image: %w", err)
	}
	if strings.TrimSpace(runningImageID) != state.ImageID {
		return fmt.Errorf("running image ID mismatch")
	}
	return nil
}

func (e *Engine) recoverState(ctx context.Context, state State) error {
	if err := UpdateEnvVersion(e.config.ComposeEnvFile, state.PreviousVersion); err != nil {
		return fmt.Errorf("restore previous version: %w", err)
	}
	if err := e.recreateApplication(ctx); err != nil {
		return err
	}
	if err := e.health.Wait(ctx, e.config.HealthURL); err != nil {
		return fmt.Errorf("recovery health check: %w", err)
	}
	return nil
}

func (e *Engine) recreateApplication(ctx context.Context) error {
	args := append(e.composePrefix(), "up", "-d", "--no-deps", "--force-recreate", e.config.ComposeService)
	if _, err := e.run(ctx, "docker", args...); err != nil {
		return fmt.Errorf("recreate application service: %w", err)
	}
	return nil
}

func (e *Engine) composePrefix() []string {
	return []string{
		"compose",
		"--project-directory", e.config.ComposeProjectDir,
		"--env-file", e.config.ComposeEnvFile,
		"-f", e.config.ComposeFile,
	}
}

func (e *Engine) run(ctx context.Context, name string, args ...string) (string, error) {
	return e.runner.Run(ctx, name, append([]string(nil), args...)...)
}

func validateConfig(config Config) error {
	required := map[string]string{
		"image_repository": config.ImageRepository,
		"expected_source":  config.ExpectedSource,
		"compose_service":  config.ComposeService,
		"release_repo":     config.ReleaseRepo,
		"release_prefix":   config.ReleasePrefix,
		"health_url":       config.HealthURL,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	for name, value := range map[string]string{
		"compose_project_dir": config.ComposeProjectDir,
		"compose_file":        config.ComposeFile,
		"compose_env_file":    config.ComposeEnvFile,
		"state_file":          config.StateFile,
	} {
		if !filepath.IsAbs(value) {
			return fmt.Errorf("%s must be absolute", name)
		}
	}
	if strings.ContainsAny(config.ComposeService, " \t\r\n/") {
		return fmt.Errorf("invalid compose service")
	}
	return nil
}
