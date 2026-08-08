# Custom Image Page Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep Sub2API's existing administrator Check, Update, and Restart workflow while making every manual update stage and apply an immutable private QA container image whose version matches the official release.

**Architecture:** The existing binary updater remains the default. When `UPDATE_MODE=container`, `UpdateService` filters releases from the configured private QA repository, stages an exact semantic version through an authenticated Unix-socket client, and delegates apply to a root-owned host helper. The helper owns Docker/Compose access, atomically edits only `SUB2API_VERSION`, health-checks the recreated application container, and rolls back the previous immutable image without touching PostgreSQL, Redis, or `/app/data`.

**Tech Stack:** Go 1.26, Gin, Wire, `net/http` Unix-socket transport, Docker CLI/Compose, Vue 3/TypeScript, GitHub Actions, systemd.

---

### Task 1: Define update configuration and release contracts

**Files:**
- Modify: `backend/internal/config/config.go:158-163`
- Modify: `backend/internal/config/config.go:2439`
- Modify: `backend/internal/repository/github_release_service.go:20-90`
- Modify: `backend/internal/repository/wire.go:25-40`
- Test: `backend/internal/repository/github_release_service_test.go`
- Test: `backend/internal/service/update_service_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestGitHubReleaseClientReadsTokenFromFile(t *testing.T) {

    tokenFile := filepath.Join(t.TempDir(), "github-token")
    require.NoError(t, os.WriteFile(tokenFile, []byte("file-token\n"), 0600))
    client := NewGitHubReleaseClientWithTokenFile("", false, tokenFile).(*githubReleaseClient)
    req, err := client.newAPIRequest(context.Background(), "https://api.github.com/repos/a/b/releases/latest")
    require.NoError(t, err)
    require.Equal(t, "Bearer file-token", req.Header.Get("Authorization"))
}

func TestContainerUpdateInfoCarriesModeAndSource(t *testing.T) {
    svc := newContainerUpdateTestService("0.1.171", []*GitHubRelease{
        {TagName: "qa-v0.1.172", Name: "QA 0.1.172"},
        {TagName: "v0.1.173", Name: "official"},
    })
    info, err := svc.CheckUpdate(context.Background(), true)
    require.NoError(t, err)
    require.Equal(t, "container", info.UpdateMode)
    require.Equal(t, "qa-v0.1.172", info.ReleaseInfo.TagName)
}
```

- [ ] **Step 2: Run the tests to verify RED**

Run: `go test -tags=unit ./internal/repository ./internal/service -run 'TestGitHubReleaseClientReadsTokenFromFile|TestContainerUpdateInfoCarriesModeAndSource' -count=1`

Expected: FAIL because the token-file constructor, `UpdateMode`, and container test fixture do not exist.

- [ ] **Step 3: Add the minimal contracts**

Add these fields without changing existing defaults:

```go
type UpdateConfig struct {
    ProxyURL       string `mapstructure:"proxy_url"`
    Mode           string `mapstructure:"mode"`
    ReleaseRepo    string `mapstructure:"release_repo"`
    ReleaseTagPrefix string `mapstructure:"release_tag_prefix"`
    HelperSocket   string `mapstructure:"helper_socket"`
    HelperTokenFile string `mapstructure:"helper_token_file"`
    GitHubTokenFile string `mapstructure:"github_token_file"`
}
```

`NewGitHubReleaseClient` remains a compatibility wrapper using `UPDATE_GITHUB_TOKEN`; `NewGitHubReleaseClientWithTokenFile` reads and trims a root-mounted token file once and never logs its contents. Add defaults for an empty binary mode and the existing proxy behavior. Update `VersionInfo`/`UpdateInfo` with `UpdateMode`, `ReleaseRepo`, and a release `TagName` field so the UI and tests can distinguish QA releases.

- [ ] **Step 4: Run focused tests to verify GREEN**

Run: `go test -tags=unit ./internal/repository ./internal/service -run 'TestGitHubReleaseClient|TestContainerUpdateInfo' -count=1`

Expected: PASS, with all existing binary-mode tests unchanged.

- [ ] **Step 5: Commit**

```text
git add backend/internal/config/config.go backend/internal/repository/github_release_service.go backend/internal/repository/github_release_service_test.go backend/internal/repository/wire.go backend/internal/service/update_service.go backend/internal/service/update_service_test.go
git commit -m "feat: add container update configuration contracts"
```

### Task 2: Implement the container strategy in UpdateService

**Files:**
- Modify: `backend/internal/service/update_service.go`
- Modify: `backend/internal/service/wire.go:34-36`
- Test: `backend/internal/service/update_service_test.go`

- [ ] **Step 1: Write failing behavior tests**

```go
func TestContainerPerformUpdateStagesOnlyNormalizedVersion(t *testing.T) {
    helper := &containerUpdateHelperStub{}
    svc := newContainerUpdateTestServiceWithHelper("0.1.171", helper, []*GitHubRelease{{TagName: "qa-v0.1.172"}})
    require.NoError(t, svc.PerformUpdate(context.Background()))
    require.Equal(t, []string{"0.1.172"}, helper.staged)
}

func TestContainerStrategyIgnoresOfficialAndMutableReleases(t *testing.T) {
    svc := newContainerUpdateTestService("0.1.171", []*GitHubRelease{
        {TagName: "v0.1.199"}, {TagName: "QA"}, {TagName: "qa-v0.1.172"},
    })
    info, err := svc.CheckUpdate(context.Background(), true)
    require.NoError(t, err)
    require.Equal(t, "0.1.172", info.LatestVersion)
}

func TestContainerRestartRequiresStagedApply(t *testing.T) {
    helper := &containerUpdateHelperStub{}
    svc := newContainerUpdateTestServiceWithHelper("0.1.171", helper, nil)
    handled, err := svc.Restart(context.Background())
    require.True(t, handled)
    require.ErrorIs(t, err, ErrNoStagedContainerUpdate)
}
```

- [ ] **Step 2: Run the tests to verify RED**

Run: `go test -tags=unit ./internal/service -run 'TestContainer' -count=1`

Expected: FAIL because the strategy, helper interface, and restart method are absent.

- [ ] **Step 3: Implement the smallest strategy boundary**

Introduce `ContainerUpdateClient` with `Stage(ctx, version string) error` and `Apply(ctx) error`; add `UpdateServiceOptions{Mode, ReleaseRepo, ReleaseTagPrefix, ContainerClient}`. Keep `NewUpdateService` binary-compatible by accepting options as a variadic argument. In container mode, fetch recent releases, keep non-draft releases with the configured `qa-v` prefix and valid `X.Y.Z` suffix, sort by `compareVersions`, and cache the selected source/prefix/repository. `PerformUpdate` calls `Stage` with only the normalized version. `RollbackToVersion` calls `Stage` after the same allow-list check. `Rollback` returns `ErrContainerRollbackUnsupported`. `Restart` returns `(true, err)` only in container mode and delegates `Apply`; binary mode returns `(false, nil)`. Add `UpdateMode` to JSON and ensure cache entries with another mode/source are ignored.

- [ ] **Step 4: Run all update-service tests**

Run: `go test -tags=unit ./internal/service -run 'Test(UpdateService|Container)' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```text
git add backend/internal/service/update_service.go backend/internal/service/update_service_test.go backend/internal/service/wire.go
git commit -m "feat: add immutable container update strategy"
```

### Task 3: Add the authenticated Unix-socket client

**Files:**
- Create: `backend/internal/repository/image_updater_client.go`
- Test: `backend/internal/repository/image_updater_client_test.go`
- Modify: `backend/internal/repository/wire.go`

- [ ] **Step 1: Write failing client tests**

```go
func TestImageUpdaterClientStageSendsVersionAndToken(t *testing.T) {
    server, requests := newUnixHTTPTestServer(t, `{"status":"staged"}`)
    client := NewImageUpdaterClient(server.SocketPath, "client-secret")
    require.NoError(t, client.Stage(context.Background(), "0.1.172"))
    require.Equal(t, "Bearer client-secret", requests[0].Header.Get("Authorization"))
    require.JSONEq(t, `{"version":"0.1.172"}`, requests[0].Body)
}

func TestImageUpdaterClientRejectsNonSuccess(t *testing.T) {
    server := newUnixHTTPTestServerWithStatus(t, http.StatusConflict, `{"error":"busy"}`)
    err := NewImageUpdaterClient(server.SocketPath, "secret").Apply(context.Background())
    require.Error(t, err)
    require.Contains(t, err.Error(), "409")
}
```

- [ ] **Step 2: Run tests to verify RED**

Run: `go test ./internal/repository -run 'TestImageUpdaterClient' -count=1`

Expected: FAIL because the Unix transport and client are not defined.

- [ ] **Step 3: Implement the client**

Use `http.Transport.DialContext` to dial the configured Unix socket, set a bounded request timeout, send `POST /v1/stage` with `{"version":...}` or `POST /v1/apply` with no user-controlled fields, and include the bearer token only in the request header. Treat any non-2xx response as an error containing the status code but not the response authorization header or token. Return a disabled-client error when the socket is empty.

- [ ] **Step 4: Run repository tests**

Run: `go test ./internal/repository -run 'Test(ImageUpdaterClient|GitHubReleaseClient)' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```text
git add backend/internal/repository/image_updater_client.go backend/internal/repository/image_updater_client_test.go backend/internal/repository/wire.go
git commit -m "feat: add authenticated image updater socket client"
```

### Task 4: Wire restart and update metadata through the admin API

**Files:**
- Modify: `backend/internal/handler/admin/system_handler.go`
- Modify: `backend/internal/handler/admin/system_handler_test.go`
- Modify: `frontend/src/api/admin/system.ts`
- Modify: `frontend/src/components/common/VersionBadge.vue`
- Test: `frontend/src/api/__tests__/admin.system.container.spec.ts`

- [ ] **Step 1: Write failing tests**

```ts
it('exposes container mode and sends restart after staging', async () => {
  get.mockResolvedValue({ data: { update_mode: 'container', has_update: true } })
  post.mockResolvedValue({ data: { need_restart: true } })
  expect((await checkUpdates(true)).update_mode).toBe('container')
  await restartService()
  expect(post).toHaveBeenCalledWith('/admin/system/restart')
})
```

```go
func TestSystemHandlerContainerRestartDelegatesToUpdateService(t *testing.T) {
    updateSvc := &systemHandlerUpdateServiceStub{restartHandled: true}
    router := newSystemHandlerTestRouter(t, updateSvc, newMemoryIdempotencyRepoStub())
    rec := httptest.NewRecorder()
    router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/system/restart", nil))
    require.Equal(t, http.StatusOK, rec.Code)
    require.Equal(t, 1, updateSvc.restartCall)
}
```

- [ ] **Step 2: Run tests to verify RED**

Run: `go test -tags=unit ./internal/handler/admin -run 'TestSystemHandlerContainerRestart' -count=1` and `pnpm vitest run src/api/__tests__/admin.system.container.spec.ts`.

Expected: FAIL because the service interface has no restart delegation and the frontend test file/API field do not exist.

- [ ] **Step 3: Implement API compatibility**

Extend the handler interface with `Restart(context.Context) (bool, error)`. In `RestartService`, call it before the existing asynchronous `sysutil.RestartServiceAsync`; when it returns `handled=true`, return the normal success response and do not exit the application process. A no-staged container operation returns HTTP 409. Add `update_mode` and `update_source` fields to the TypeScript `VersionInfo`; keep all existing binary-mode behavior and timeout values. In the version badge, keep versioned rollback for container mode and hide the legacy local-backup/manual rollback entry when `update_mode === 'container'`.

- [ ] **Step 4: Run focused backend and frontend tests**

Run: `go test -tags=unit ./internal/handler/admin -run 'TestSystemHandler' -count=1` and `pnpm vitest run src/api/__tests__/admin.system.rollback.spec.ts src/api/__tests__/admin.system.container.spec.ts`.

Expected: PASS for backend; frontend passes when the local pnpm environment is available, otherwise retain the captured pnpm installer error.

- [ ] **Step 5: Commit**

```text
git add backend/internal/handler/admin/system_handler.go backend/internal/handler/admin/system_handler_test.go frontend/src/api/admin/system.ts frontend/src/components/common/VersionBadge.vue frontend/src/api/__tests__/admin.system.container.spec.ts
git commit -m "feat: delegate container restart through admin update API"
```

### Task 5: Build the host image-updater core

**Files:**
- Create: `backend/internal/imageupdater/version.go`
- Create: `backend/internal/imageupdater/env.go`
- Create: `backend/internal/imageupdater/engine.go`
- Create: `backend/internal/imageupdater/state.go`
- Test: `backend/internal/imageupdater/version_test.go`
- Test: `backend/internal/imageupdater/env_test.go`
- Test: `backend/internal/imageupdater/engine_test.go`

- [ ] **Step 1: Write failing tests**

```go
func TestNormalizeVersionRejectsImageInjection(t *testing.T) {
    for _, input := range []string{"", "latest", "0.1.1:bad", "../../x", "v0.1.172"} {
        _, err := NormalizeVersion(input)
        require.Error(t, err, input)
    }
    require.Equal(t, "0.1.172", mustNormalize(t, "0.1.172"))
}

func TestUpdateEnvVersionPreservesUnrelatedLinesAtomically(t *testing.T) {
    path := filepath.Join(t.TempDir(), ".env")
    require.NoError(t, os.WriteFile(path, []byte("DB_HOST=postgres\nSUB2API_VERSION=0.1.171\n# keep\n"), 0600))
    require.NoError(t, UpdateEnvVersion(path, "0.1.172"))
    body, err := os.ReadFile(path)
    require.NoError(t, err)
    require.Equal(t, "DB_HOST=postgres\nSUB2API_VERSION=0.1.172\n# keep\n", string(body))
}
```

- [ ] **Step 2: Run tests to verify RED**

Run: `go test ./internal/imageupdater -run 'Test(NormalizeVersion|UpdateEnvVersion)' -count=1`.

Expected: FAIL because the package and functions are absent.

- [ ] **Step 3: Implement fixed-parameter engine**

Define `Config` with fixed image repository, Compose directory/file/service, env path, state path, release repository/prefix, token file, health URL, and timeouts. Define injected `CommandRunner`, `ReleaseVerifier`, and `HealthChecker` interfaces for tests. Construct image references only as `repo + ":" + normalizedVersion`; invoke Docker and Compose with argument slices, never a shell. Stage must acquire a process mutex, verify the matching `qa-vX.Y.Z` release, pull the image, inspect source/version labels and immutable image ID, then atomically write state. Apply must load staged state, atomically set only `SUB2API_VERSION`, recreate only the configured service, wait for health, verify image ID, clear state on success, and on failure restore the previous version and recreate only that service. State and error files contain versions/digests only; authorization values are never logged.

- [ ] **Step 4: Run engine tests**

Run: `go test ./internal/imageupdater -count=1`.

Expected: PASS, including malformed versions, concurrent operations, fixed command arguments, atomic env updates, successful apply, and failed-health recovery.

- [ ] **Step 5: Commit**

```text
git add backend/internal/imageupdater
git commit -m "feat: add safe image updater engine"
```

### Task 6: Add the Unix-socket helper binary and systemd unit

**Files:**
- Create: `backend/cmd/image-updater/main.go`
- Create: `backend/cmd/image-updater/main_test.go`
- Create: `deploy/image-updater/config.example.yaml`
- Create: `deploy/sub2api-image-updater.service`
- Modify: `deploy/.env.example`
- Modify: `deploy/README.md`

- [ ] **Step 1: Write failing authentication tests**

```go
func TestHelperRejectsMissingOrWrongToken(t *testing.T) {
    handler := NewHTTPHandler(testEngineConfig(t), "expected")
    for _, token := range []string{"", "wrong"} {
        req := httptest.NewRequest(http.MethodPost, "/v1/stage", strings.NewReader(`{"version":"0.1.172"}`))
        req.Header.Set("Authorization", "Bearer "+token)
        rec := httptest.NewRecorder()
        handler.ServeHTTP(rec, req)
        require.Equal(t, http.StatusUnauthorized, rec.Code)
    }
}
```

- [ ] **Step 2: Run test to verify RED**

Run: `go test ./cmd/image-updater -run 'TestHelperRejects' -count=1`.

Expected: FAIL because the helper command package is absent.

- [ ] **Step 3: Implement the helper HTTP server**

Load a root-owned config and token file at startup, create the Unix socket with mode `0660`, and expose only `POST /v1/stage` and `POST /v1/apply`. Require an exact bearer token using constant-time comparison. Reject unknown routes, malformed JSON, extra fields, non-semver versions, and concurrent operations. `/v1/apply` returns `202 Accepted` after recording the operation and runs the engine in a bounded background context. Handle SIGTERM/SIGINT by closing the listener and removing the socket.

- [ ] **Step 4: Add a locked-down systemd service and docs**

The unit runs as root with `NoNewPrivileges=true`, `PrivateTmp=true`, `ProtectSystem=strict`, `ReadWritePaths=/var/lib/sub2api-image-updater /root/sub2api /run/sub2api-image-updater`, and no Docker socket mount into the app. The config example fixes `ghcr.io/66-dashun/sub2api`, `/root/sub2api`, `sub2api`, and `qa-v`; the token file paths are separate from the app environment. Document installation without printing token contents.

- [ ] **Step 5: Run helper tests and commit**

Run: `go test ./cmd/image-updater ./internal/imageupdater -count=1`.

```text
git add backend/cmd/image-updater deploy/image-updater deploy/sub2api-image-updater.service deploy/.env.example deploy/README.md
git commit -m "feat: add root-owned image updater helper"
```

### Task 7: Make images and Compose versioned and publish QA releases

**Files:**
- Modify: `Dockerfile:108-114`
- Modify: `deploy/Dockerfile:84-90`
- Modify: `deploy/docker-compose.yml:18-40`
- Modify: `.github/workflows/qa-image.yml`
- Test: `deploy/tests/qa-image-contract-test.ps1`

- [ ] **Step 1: Write the failing contract test**

```powershell
$dockerfile = Get-Content "$PSScriptRoot\..\..\Dockerfile" -Raw
if ($dockerfile -notmatch 'org.opencontainers.image.version') { throw 'version label missing' }
$compose = Get-Content "$PSScriptRoot\..\docker-compose.yml" -Raw
if ($compose -notmatch 'ghcr\.io/66-dashun/sub2api:\$\{SUB2API_VERSION\}') { throw 'private exact-version image missing' }
```

- [ ] **Step 2: Run test to verify RED**

Run: `pwsh -File deploy/tests/qa-image-contract-test.ps1`.

Expected: FAIL because the current image is `weishaw/sub2api:latest` and the Dockerfile has no version/source build labels.

- [ ] **Step 3: Implement build contracts**

Add `ARG SOURCE_REPOSITORY` and OCI labels for source, version, revision, and build type. Change the QA workflow to require a `version` input matching `X.Y.Z`, fetch/verify the official `vX.Y.Z` ancestor, run backend unit tests and frontend build, build/push `ghcr.io/66-dashun/sub2api:X.Y.Z` plus `QA`, inspect the pushed labels/digest, and only then create the private `qa-vX.Y.Z` release. The workflow must use `contents: write` and `packages: write`, never put user/database data in build context, and never expose a package token in logs.

Change the deployment Compose image to:

```yaml
image: ghcr.io/66-dashun/sub2api:${SUB2API_VERSION:-0.1.171}
```

Add only the read-only updater socket/token mounts and `UPDATE_MODE=container` settings to the QA deployment example; preserve all existing database, Redis, and `/app/data` mounts.

- [ ] **Step 4: Run contract and rendering tests**

Run: `pwsh -File deploy/tests/qa-image-contract-test.ps1` and `docker compose -f deploy/docker-compose.yml config`.

Expected: PASS; rendered services keep PostgreSQL/Redis volumes and select the private exact-version image.

- [ ] **Step 5: Commit**

```text
git add Dockerfile deploy/Dockerfile deploy/docker-compose.yml deploy/tests/qa-image-contract-test.ps1 .github/workflows/qa-image.yml
git commit -m "ci: publish immutable QA images with matching versions"
```

### Task 8: Regenerate wiring, run the complete local verification, and publish

**Files:**
- Modify: `backend/internal/service/wire.go`
- Modify: `backend/internal/repository/wire.go`
- Modify: `backend/cmd/server/wire_gen.go`
- Modify: `backend/cmd/server/VERSION`
- Modify: `docs/superpowers/specs/2026-08-08-custom-image-page-update-design.md`

- [ ] **Step 1: Write the wiring regression test**

```go
func TestContainerUpdateConfigurationUsesQARepository(t *testing.T) {
    cfg := config.Config{Update: config.UpdateConfig{Mode: "container", ReleaseRepo: "66-DASHUN/sub2api-qa", ReleaseTagPrefix: "qa-v"}}
    require.Equal(t, "container", cfg.Update.Mode)
    require.Equal(t, "qa-v", cfg.Update.ReleaseTagPrefix)
}
```

- [ ] **Step 2: Run RED, then wire the providers**

Run: `go test -tags=unit ./cmd/server ./internal/service ./internal/repository -run 'TestContainerUpdateConfiguration' -count=1`.

Expected: the new test initially fails to compile until the provider signatures and generated Wire graph are updated. Add `ProvideImageUpdaterClient`, pass `config.Config` into `ProvideUpdateService`, and regenerate `wire_gen.go` with the repository's Wire command.

- [ ] **Step 3: Run all relevant verification**

Run: `go test -tags=unit ./internal/service ./internal/handler/admin ./internal/repository ./internal/imageupdater ./cmd/image-updater ./cmd/server -count=1`, `go test ./... -run 'TestImageUpdater|TestGitHubReleaseClient' -count=1`, `pwsh -File deploy/tests/qa-image-contract-test.ps1`, and `git diff --check`.

Expected: all selected tests pass, the contract script passes, and `git diff --check` is clean. Record the pnpm installer failure separately if it persists; do not claim frontend tests passed without a successful run.

- [ ] **Step 4: Build the binaries and image locally without secrets**

Run: `go build ./cmd/image-updater`, `docker build --build-arg VERSION=0.1.171 --build-arg SOURCE_REPOSITORY=https://github.com/66-DASHUN/sub2api-qa --tag sub2api:QA .`, and `docker image inspect sub2api:QA --format '{{json .Config.Labels}}'`.

Expected: the helper binary builds, the image builds without `.env`, database, or key files in context, and its labels contain version `0.1.171` and the QA source URL.

- [ ] **Step 5: Commit the integrated implementation**

```text
git add backend/internal/service/wire.go backend/internal/repository/wire.go backend/cmd/server/wire_gen.go backend/cmd/image-updater docs/superpowers
git commit -m "feat: integrate page-triggered private image updates"
```

- [ ] **Step 6: Publish the QA branch and image workflow**

Run: `git push origin qa/grok-video-1080p`, then manually dispatch the workflow with `version=0.1.171` only after the local tests and contract checks pass. Verify the GitHub Actions run, private GHCR immutable tag, image digest, and `qa-v0.1.171` release metadata. Do not log or paste any token.

### Task 9: Server migration and end-to-end verification (requires explicit write authorization)

**Files/Systems:**
- Server `47.253.211.154:/root/sub2api/docker-compose.yml`
- Server `/etc/sub2api-image-updater/`, `/var/lib/sub2api-image-updater/`, and systemd unit

- [ ] **Step 1: Stop before mutation and request/confirm write authorization**

Read-only checks must show the target paths, current container IDs, image tag/digest, and stopped/running state. No deploy command is allowed until the user explicitly authorizes server writes, GHCR login, Compose changes, or service start/restart.

- [ ] **Step 2: Create recoverable backups and install the helper**

Copy the Compose file and `.env` to a timestamped root-only backup, install the helper binary/config/token files with mode `0600`, install the systemd unit, and log Docker into GHCR on the host only. Never print environment values or credentials.

- [ ] **Step 3: Switch only the application image and start only with authorization**

Set `SUB2API_VERSION=0.1.171`, mount `/app/data` and existing database/Redis volumes unchanged, start/recreate only `sub2api`, and leave PostgreSQL/Redis untouched. The page's Check, Update, Restart controls then exercise stage/apply through the Unix socket.

- [ ] **Step 4: Verify and report evidence**

Verify private immutable image tag/digest, application health, matching embedded version/OCI label, unchanged PostgreSQL/Redis IDs and mounts, preserved `/app/data`, no Watchtower/timer, and a second update check reporting no update. Report only non-secret paths, versions, IDs, and statuses.

---

## Self-review

- Spec coverage: Tasks 1-4 cover configuration, release filtering, cache isolation, application API, and UI; Tasks 5-6 cover helper safety, authentication, locking, rollback, and systemd; Task 7 covers immutable labels, Compose persistence, and ordered QA releases; Tasks 8-9 cover wiring, tests, publishing, and the explicitly gated server migration.
- Placeholder scan: all commands, paths, names, and expected outcomes are concrete; no `TBD`, `TODO`, or unspecified error-handling steps remain.
- Type consistency: `ContainerUpdateClient`, `UpdateServiceOptions`, `UpdateMode`, `Restart(context.Context) (bool, error)`, `NormalizeVersion`, and `UpdateEnvVersion` are introduced before later tasks consume them.
