package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/imageupdater"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"gopkg.in/yaml.v3"
)

const (
	defaultConfigPath = "/etc/sub2api-image-updater/config.yaml"
	maxRequestBytes   = 1 << 20
	defaultStageTTL   = 30 * time.Minute
	defaultApplyTTL   = 30 * time.Minute
)

// helperEngine is deliberately narrower than imageupdater.Engine so the HTTP
// boundary can be tested without giving it access to arbitrary host commands.
type helperEngine interface {
	Stage(context.Context, string) error
	Apply(context.Context) error
	HasStaged() (bool, error)
}

type handlerOptions struct {
	StageTimeout time.Duration
	ApplyTimeout time.Duration
	Logger       *log.Logger
}

type stageRequest struct {
	Version string `json:"version"`
}

type helperHandler struct {
	engine       helperEngine
	token        []byte
	stageTimeout time.Duration
	applyTimeout time.Duration
	logger       *log.Logger

	operationMu sync.Mutex
	operation   bool
}

// NewHTTPHandler returns the authenticated helper API used by the Sub2API
// container. The token is kept only in memory and is never written to a
// response or log.
func NewHTTPHandler(engine helperEngine, token string) http.Handler {
	return NewHTTPHandlerWithOptions(engine, token, handlerOptions{})
}

func NewHTTPHandlerWithOptions(engine helperEngine, token string, options handlerOptions) http.Handler {
	if options.StageTimeout <= 0 {
		options.StageTimeout = defaultStageTTL
	}
	if options.ApplyTimeout <= 0 {
		options.ApplyTimeout = defaultApplyTTL
	}
	if options.Logger == nil {
		options.Logger = log.Default()
	}
	return &helperHandler{
		engine:       engine,
		token:        []byte(strings.TrimSpace(token)),
		stageTimeout: options.StageTimeout,
		applyTimeout: options.ApplyTimeout,
		logger:       options.Logger,
	}
}

func (h *helperHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	switch r.URL.Path {
	case "/v1/stage":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		h.handleStage(w, r)
	case "/v1/apply":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		h.handleApply(w, r)
	default:
		writeJSONError(w, http.StatusNotFound, "not found")
	}
}

func (h *helperHandler) authorized(r *http.Request) bool {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	provided := header[len(prefix):]
	if strings.TrimSpace(provided) != provided || provided == "" || len(h.token) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), h.token) == 1
}

func (h *helperHandler) handleStage(w http.ResponseWriter, r *http.Request) {
	if !h.beginOperation() {
		writeJSONError(w, http.StatusConflict, "another update operation is in progress")
		return
	}
	defer h.endOperation()

	var request stageRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeJSONError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}
	version, err := imageupdater.NormalizeVersion(request.Version)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid version")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.stageTimeout)
	defer cancel()
	if err := h.engine.Stage(ctx, version); err != nil {
		h.writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "staged", "version": version})
}

func (h *helperHandler) handleApply(w http.ResponseWriter, r *http.Request) {
	if !h.beginOperation() {
		writeJSONError(w, http.StatusConflict, "another update operation is in progress")
		return
	}
	staged, err := h.engine.HasStaged()
	if err != nil {
		h.endOperation()
		writeJSONError(w, http.StatusInternalServerError, "cannot inspect staged update")
		return
	}
	if !staged {
		h.endOperation()
		writeJSONError(w, http.StatusConflict, "no staged image update")
		return
	}

	// The request must be acknowledged before the application container is
	// recreated; otherwise the caller can lose its socket while waiting.
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "applying"})
	go func() {
		defer h.endOperation()
		ctx, cancel := context.WithTimeout(context.Background(), h.applyTimeout)
		defer cancel()
		if err := h.engine.Apply(ctx); err != nil {
			h.logger.Printf("image apply failed: %v", err)
		}
	}()
}

func (h *helperHandler) beginOperation() bool {
	h.operationMu.Lock()
	defer h.operationMu.Unlock()
	if h.operation {
		return false
	}
	h.operation = true
	return true
}

func (h *helperHandler) endOperation() {
	h.operationMu.Lock()
	h.operation = false
	h.operationMu.Unlock()
}

func (h *helperHandler) writeEngineError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, imageupdater.ErrOperationInProgress):
		writeJSONError(w, http.StatusConflict, "another update operation is in progress")
	case errors.Is(err, imageupdater.ErrNoStagedUpdate):
		writeJSONError(w, http.StatusConflict, "no staged image update")
	default:
		writeJSONError(w, http.StatusInternalServerError, "image update failed")
	}
}

func writeMethodNotAllowed(w http.ResponseWriter, method string) {
	w.Header().Set("Allow", method)
	writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type helperConfig struct {
	Socket              string        `yaml:"socket"`
	TokenFile           string        `yaml:"token_file"`
	GitHubTokenFile     string        `yaml:"github_token_file"`
	ProxyURL            string        `yaml:"proxy_url"`
	ImageRepository     string        `yaml:"image_repository"`
	ExpectedSource      string        `yaml:"expected_source"`
	ComposeProjectDir   string        `yaml:"compose_project_dir"`
	ComposeFile         string        `yaml:"compose_file"`
	ComposeOverrideFile string        `yaml:"compose_override_file"`
	ComposeEnvFile      string        `yaml:"compose_env_file"`
	ComposeService      string        `yaml:"compose_service"`
	ReleaseRepo         string        `yaml:"release_repo"`
	ReleasePrefix       string        `yaml:"release_prefix"`
	StateFile           string        `yaml:"state_file"`
	HealthURL           string        `yaml:"health_url"`
	StageTimeout        time.Duration `yaml:"-"`
	ApplyTimeout        time.Duration `yaml:"-"`
	HealthTimeout       time.Duration `yaml:"-"`
	StageTimeoutSecs    int           `yaml:"stage_timeout_seconds"`
	ApplyTimeoutSecs    int           `yaml:"apply_timeout_seconds"`
	HealthTimeoutSecs   int           `yaml:"health_timeout_seconds"`
	HealthIntervalMS    int           `yaml:"health_interval_milliseconds"`
}

func loadHelperConfig(path string) (helperConfig, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return helperConfig{}, fmt.Errorf("read helper config: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	var cfg helperConfig
	if err := decoder.Decode(&cfg); err != nil {
		return helperConfig{}, fmt.Errorf("parse helper config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return helperConfig{}, err
	}
	return cfg, nil
}

func (c *helperConfig) validate() error {
	for name, value := range map[string]string{
		"socket": c.Socket, "token_file": c.TokenFile, "image_repository": c.ImageRepository,
		"expected_source": c.ExpectedSource, "compose_project_dir": c.ComposeProjectDir,
		"compose_file": c.ComposeFile, "compose_override_file": c.ComposeOverrideFile,
		"compose_env_file": c.ComposeEnvFile,
		"compose_service":  c.ComposeService, "release_repo": c.ReleaseRepo,
		"release_prefix": c.ReleasePrefix, "state_file": c.StateFile, "health_url": c.HealthURL,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	for name, value := range map[string]string{
		"socket": c.Socket, "token_file": c.TokenFile, "compose_project_dir": c.ComposeProjectDir,
		"compose_file": c.ComposeFile, "compose_override_file": c.ComposeOverrideFile,
		"compose_env_file": c.ComposeEnvFile, "state_file": c.StateFile,
	} {
		if !filepath.IsAbs(value) {
			return fmt.Errorf("%s must be absolute", name)
		}
	}
	if strings.ContainsAny(c.ComposeService, " \t\r\n/") {
		return fmt.Errorf("invalid compose_service")
	}
	parsed, err := url.Parse(c.HealthURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("health_url must be an absolute http(s) URL")
	}
	if c.StageTimeoutSecs <= 0 {
		c.StageTimeoutSecs = int(defaultStageTTL / time.Second)
	}
	if c.ApplyTimeoutSecs <= 0 {
		c.ApplyTimeoutSecs = int(defaultApplyTTL / time.Second)
	}
	if c.HealthTimeoutSecs <= 0 {
		c.HealthTimeoutSecs = 300
	}
	if c.HealthIntervalMS <= 0 {
		c.HealthIntervalMS = 2000
	}
	c.StageTimeout = time.Duration(c.StageTimeoutSecs) * time.Second
	c.ApplyTimeout = time.Duration(c.ApplyTimeoutSecs) * time.Second
	c.HealthTimeout = time.Duration(c.HealthTimeoutSecs) * time.Second
	return nil
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > 4096 {
			message = message[:4096]
		}
		if message != "" {
			return "", fmt.Errorf("%s: %w: %s", name, err, message)
		}
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return string(output), nil
}

type githubReleaseVerifier struct {
	client service.GitHubReleaseClient
}

func (v githubReleaseVerifier) Verify(ctx context.Context, repo, prefix, version string) (string, error) {
	target := prefix + version
	releases, err := v.client.FetchRecentReleases(ctx, repo, 100)
	if err != nil {
		return "", err
	}
	for _, release := range releases {
		if release != nil && !release.Draft && !release.Prerelease && release.TagName == target {
			revision := strings.TrimSpace(release.TargetCommitish)
			if !isFullGitCommit(revision) {
				return "", fmt.Errorf("release %s does not target a full commit SHA", target)
			}
			return revision, nil
		}
	}
	return "", fmt.Errorf("release %s was not found", target)
}

func isFullGitCommit(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

type httpHealthChecker struct {
	client   *http.Client
	timeout  time.Duration
	interval time.Duration
}

func (c httpHealthChecker) Wait(ctx context.Context, healthURL string) error {
	deadlineCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(deadlineCtx, http.MethodGet, healthURL, nil)
		if err == nil {
			resp, requestErr := c.client.Do(req)
			if requestErr == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
				_ = resp.Body.Close()
				if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
					return nil
				}
				lastErr = fmt.Errorf("health endpoint returned %d", resp.StatusCode)
			} else {
				lastErr = requestErr
			}
		} else {
			lastErr = err
		}
		select {
		case <-deadlineCtx.Done():
			if lastErr == nil {
				return deadlineCtx.Err()
			}
			return fmt.Errorf("%w: %v", deadlineCtx.Err(), lastErr)
		case <-time.After(c.interval):
		}
	}
}

func buildEngine(cfg helperConfig) (*imageupdater.Engine, error) {
	tokenFile := cfg.GitHubTokenFile
	releaseClient := repository.NewGitHubReleaseClientWithTokenFile(cfg.ProxyURL, false, tokenFile)
	verifier := githubReleaseVerifier{client: releaseClient}
	health := httpHealthChecker{
		client:   &http.Client{Timeout: 10 * time.Second},
		timeout:  cfg.HealthTimeout,
		interval: time.Duration(cfg.HealthIntervalMS) * time.Millisecond,
	}
	return imageupdater.NewEngine(imageupdater.Config{
		ImageRepository:     cfg.ImageRepository,
		ExpectedSource:      cfg.ExpectedSource,
		ComposeProjectDir:   cfg.ComposeProjectDir,
		ComposeFile:         cfg.ComposeFile,
		ComposeOverrideFile: cfg.ComposeOverrideFile,
		ComposeEnvFile:      cfg.ComposeEnvFile,
		ComposeService:      cfg.ComposeService,
		ReleaseRepo:         cfg.ReleaseRepo,
		ReleasePrefix:       cfg.ReleasePrefix,
		StateFile:           cfg.StateFile,
		HealthURL:           cfg.HealthURL,
	}, execCommandRunner{}, verifier, health)
}

func readToken(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read helper token: %w", err)
	}
	token := strings.TrimSpace(string(body))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", fmt.Errorf("helper token is empty")
	}
	return token, nil
}

func prepareSocket(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to replace non-socket path %s", path)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove stale socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func runHelper(ctx context.Context, configPath string) error {
	cfg, err := loadHelperConfig(configPath)
	if err != nil {
		return err
	}
	token, err := readToken(cfg.TokenFile)
	if err != nil {
		return err
	}
	engine, err := buildEngine(cfg)
	if err != nil {
		return fmt.Errorf("create image updater engine: %w", err)
	}
	if err := prepareSocket(cfg.Socket); err != nil {
		return err
	}
	listener, err := net.Listen("unix", cfg.Socket)
	if err != nil {
		return fmt.Errorf("listen on helper socket: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(cfg.Socket)
	}()
	if err := os.Chmod(cfg.Socket, 0660); err != nil {
		return fmt.Errorf("set helper socket permissions: %w", err)
	}
	server := &http.Server{
		Handler:           NewHTTPHandlerWithOptions(engine, token, handlerOptions{StageTimeout: cfg.StageTimeout, ApplyTimeout: cfg.ApplyTimeout}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	signalCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case <-signalCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func main() {
	configPath := os.Getenv("IMAGE_UPDATER_CONFIG")
	if strings.TrimSpace(configPath) == "" {
		configPath = defaultConfigPath
	}
	flag.StringVar(&configPath, "config", configPath, "path to the root-owned helper configuration")
	flag.Parse()
	if err := runHelper(context.Background(), configPath); err != nil {
		log.Printf("image updater stopped: %v", err)
		os.Exit(1)
	}
}
