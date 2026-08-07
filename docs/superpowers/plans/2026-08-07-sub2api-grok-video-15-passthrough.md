# Sub2API Grok Video 1.5 Passthrough Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build and deploy a maintainable Sub2API QA image based on official `v0.1.171` that forwards `grok-imagine-video-1.5` text-to-video requests without rewriting the model.

**Architecture:** Keep the official source commit as the baseline and isolate the behavioral change in one Go/test commit. Publish a private GHCR image with a separate workflow commit, then change only the Sub2API image reference on `47.253.211.154`; PostgreSQL, Redis, volumes, ports, and runtime secrets remain outside the image and unchanged.

**Tech Stack:** Go 1.26.5, testify, GitHub Actions, Docker Buildx, GHCR, Docker Compose

---

### Task 1: Record The Official Baseline

**Files:**
- Create: `docs/superpowers/specs/2026-08-07-sub2api-grok-video-15-passthrough-design.md`
- Create: `docs/superpowers/plans/2026-08-07-sub2api-grok-video-15-passthrough.md`

- [ ] **Step 1: Verify the tag and source commit**

Run:

```bash
git rev-parse v0.1.171
git rev-parse 'v0.1.171^{commit}'
git rev-parse 'v0.1.171^{tree}'
```

Expected:

```text
afd154b92aac36c6dafb1fa8e181ca827c78c465
f0e7a9c7a23a7d02fb159b62fa809621eb0475a6
e074f36f026c283298181512ebdcfa14654f5b74
```

- [ ] **Step 2: Verify repository isolation**

Run:

```bash
git branch --show-current
git remote -v
git status --short
```

Expected: branch `qa/grok-video-1080p`, `origin` points to `66-DASHUN/sub2api-qa`, `upstream` points to `Wei-Shaw/sub2api`, and only the two documentation files are untracked.

- [ ] **Step 3: Commit the design and plan**

Run:

```bash
git add -f docs/superpowers/specs/2026-08-07-sub2api-grok-video-15-passthrough-design.md docs/superpowers/plans/2026-08-07-sub2api-grok-video-15-passthrough.md
git commit -m "docs: record grok video 1.5 passthrough design"
```

Expected: one documentation-only commit.

### Task 2: Write Regression Expectations And Verify RED

**Files:**
- Modify: `backend/internal/service/openai_gateway_grok_test.go:857`
- Modify: `backend/internal/service/openai_gateway_grok_test.go:930`
- Modify: `backend/internal/service/openai_gateway_grok_test.go:1170`

- [ ] **Step 1: Change the normalization expectation**

Replace the text-only fallback case with:

```go
{name: "video 1.5 text-to-video passthrough", endpoint: GrokMediaEndpointVideosGenerations, model: "grok-imagine-video-1.5", want: "grok-imagine-video-1.5"},
```

- [ ] **Step 2: Change the account mapping expectation**

Use the requested 1.5 model as the mapping key:

```go
{
    name:             "video generation maps preserved text-to-video model",
    endpoint:         GrokMediaEndpointVideosGenerations,
    path:             "/v1/videos/generations",
    body:             `{"model":"grok-imagine-video-1.5","prompt":"waves"}`,
    modelMapping:     map[string]any{"grok-imagine-video-1.5": "vendor-video-1.5"},
    wantRequestModel: "grok-imagine-video-1.5",
    wantUpstream:     "vendor-video-1.5",
    wantBody:         `{"model":"vendor-video-1.5","prompt":"waves"}`,
    responseBody:     `{"request_id":"video-request-mapped"}`,
},
```

- [ ] **Step 3: Change the forwarding and billing expectation**

Use a 1080p request and require the upstream body and billing model to preserve 1.5:

```go
body := []byte(`{"model":"grok-imagine-video-1.5","prompt":"waves","resolution":"1080p","duration":5}`)
// ...
require.JSONEq(t, `{"model":"grok-imagine-video-1.5","prompt":"waves","resolution":"1080p","duration":5}`, string(upstream.lastBody))
require.Equal(t, "grok-imagine-video-1.5", result.BillingModel)
require.Equal(t, VideoBillingResolution1080P, result.VideoResolution)
require.Equal(t, 5, result.VideoDurationSeconds)
```

- [ ] **Step 4: Run focused tests and verify RED**

Run from `backend/`:

```bash
go test ./internal/service -run 'TestNormalizeGrokMediaModelForEndpoint|TestForwardGrokMediaAppliesAccountModelMappingAfterEndpointNormalization|TestForwardGrokMediaVideoGenerationReturnsUsageAndResponseID' -count=1
```

Expected: FAIL because the unmodified implementation still rewrites text-to-video 1.5 to `grok-imagine-video`.

### Task 3: Preserve The Requested Model And Verify GREEN

**Files:**
- Modify: `backend/internal/service/grok_media.go:765`
- Test: `backend/internal/service/openai_gateway_grok_test.go`

- [ ] **Step 1: Remove only the built-in video fallback**

Keep the image aliases and return the requested video model unchanged:

```go
func NormalizeGrokMediaModelForEndpoint(endpoint GrokMediaEndpoint, model string, hasInputImage bool) string {
    model = strings.TrimSpace(model)
    switch endpoint {
    case GrokMediaEndpointImagesGenerations, GrokMediaEndpointImagesEdits:
        if model == "grok-imagine" {
            return "grok-imagine-image-quality"
        }
    }
    return model
}
```

Do not change account model mapping, request sanitization, billing fields, database code, or pricing configuration.

- [ ] **Step 2: Format the touched Go files**

Run:

```bash
gofmt -w internal/service/grok_media.go internal/service/openai_gateway_grok_test.go
```

- [ ] **Step 3: Run focused tests and verify GREEN**

Run:

```bash
go test ./internal/service -run 'TestNormalizeGrokMediaModelForEndpoint|TestForwardGrokMediaAppliesAccountModelMappingAfterEndpointNormalization|TestForwardGrokMediaVideoGenerationReturnsUsageAndResponseID|TestForwardGrokMediaVideoGenerationPreservesImageToVideoModel|TestForwardGrokMediaImagesGenerationNormalizesImagineAlias' -count=1
```

Expected: PASS.

- [ ] **Step 4: Run the complete backend test suite**

Run:

```bash
go test ./... -count=1
```

Expected: PASS with zero failures.

- [ ] **Step 5: Commit the cherry-pickable behavior patch**

Run:

```bash
git add backend/internal/service/grok_media.go backend/internal/service/openai_gateway_grok_test.go
git commit -m "fix: preserve grok video 1.5 text-to-video model"
```

Expected: the commit contains only the Go implementation and tests.

### Task 4: Add A Secret-Free GHCR Build

**Files:**
- Create: `.github/workflows/qa-image.yml`
- Modify: `.dockerignore`

- [ ] **Step 1: Strengthen runtime-data exclusions**

Append these patterns to `.dockerignore`:

```text
# QA downstream: never send local runtime state or credentials to BuildKit
data/
backups/
postgres_data/
redis_data/
**/data/
**/backups/
**/postgres_data/
**/redis_data/
*.pem
*.p12
*.pfx
*.key
```

- [ ] **Step 2: Create the QA image workflow**

Create `.github/workflows/qa-image.yml`:

```yaml
name: QA Container Image

on:
  workflow_dispatch:
  push:
    branches:
      - qa/grok-video-1080p

permissions:
  contents: read
  packages: write

concurrency:
  group: qa-container-${{ github.ref }}
  cancel-in-progress: true

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
      - uses: actions/setup-go@v6
        with:
          go-version-file: backend/go.mod
          cache-dependency-path: backend/go.sum
      - name: Test backend
        working-directory: backend
        run: go test ./... -count=1

  build:
    needs: test
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - uses: docker/build-push-action@v6
        with:
          context: .
          file: ./Dockerfile
          platforms: linux/amd64
          push: true
          tags: |
            ghcr.io/66-dashun/sub2api:QA
            ghcr.io/66-dashun/sub2api:qa-v0.1.171-${{ github.sha }}
          build-args: |
            VERSION=0.1.171-qa
            COMMIT=${{ github.sha }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
```

- [ ] **Step 3: Audit tracked files for secrets and runtime data**

Run:

```bash
git ls-files | rg '(^|/)(\.env($|\.)|data/|backups/|postgres_data/|redis_data/)|\.(pem|p12|pfx|key)$'
git diff --check
```

Expected: the first command returns no files; `git diff --check` returns no errors.

- [ ] **Step 4: Build locally when Docker is available**

Run:

```bash
docker build --pull --build-arg VERSION=0.1.171-qa --build-arg COMMIT=local-test -t sub2api:QA .
```

Expected: exit code 0. If Docker is unavailable locally, the GitHub Actions build is the required build evidence.

- [ ] **Step 5: Commit the workflow**

Run:

```bash
git add .dockerignore .github/workflows/qa-image.yml
git commit -m "ci: publish private QA image to GHCR"
```

### Task 5: Create The Private Repository And Publish

**Files:** none

- [ ] **Step 1: Create `66-DASHUN/sub2api-qa` as a private ordinary repository**

Do not initialize it with a README, license, or gitignore. The repository must not be created as a fork.

- [ ] **Step 2: Push the branch and official tag**

Run:

```bash
git push -u origin qa/grok-video-1080p
git push origin refs/tags/v0.1.171:refs/tags/v0.1.171
```

- [ ] **Step 3: Verify the workflow**

Require the `QA Container Image` workflow to pass both `test` and `build`.
Derive the immutable tag from the pushed commit:

```bash
QA_SHA="$(git rev-parse HEAD)"
QA_IMAGE="ghcr.io/66-dashun/sub2api:qa-v0.1.171-${QA_SHA}"
printf '%s\n' "${QA_IMAGE}"
```

Record that image's digest from GHCR.

- [ ] **Step 4: Verify package privacy**

The GHCR package visibility must be `Private`. Do not expose a token in workflow logs, commit history, Docker labels, or build arguments.

### Task 6: Deploy Only To The 47 Server

**Files:**
- Modify remotely: the existing Docker Compose file for the Sub2API service only

- [ ] **Step 1: Re-establish temporary SSH authorization**

Use a temporary SSH public key. Do not place the server password, GitHub token, application API key, or upstream key in a command line, repository, or file under the project.

- [ ] **Step 2: Verify server identity and current state**

Run remotely:

```bash
hostname
docker compose ps
docker inspect sub2api --format '{{.Config.Image}} {{.Image}}'
```

Expected: the target is `47.253.211.154`; no command is sent to `142.214.159.77`.

- [ ] **Step 3: Create and validate a pre-deployment backup**

Back up the active Compose file, `.env`, application data, PostgreSQL data, and Redis data under `/root/sub2api-update-backups/`. List the archive and compute its SHA-256 before changing the image reference.

- [ ] **Step 4: Authenticate to GHCR without persisting the token**

Provide a token limited to `read:packages` over standard input:

```bash
docker login ghcr.io -u 66-DASHUN --password-stdin
```

Export the `QA_SHA` recorded in Task 5, derive the immutable image name, pull it,
record its digest, and then log out after deployment:

```bash
QA_IMAGE="ghcr.io/66-dashun/sub2api:qa-v0.1.171-${QA_SHA}"
docker pull "${QA_IMAGE}"
docker logout ghcr.io
```

- [ ] **Step 5: Change only the Sub2API image**

Set the existing Sub2API service image to the immutable QA tag. Preserve PostgreSQL, Redis, ports, volumes, environment values, health checks, and restart policies.

- [ ] **Step 6: Start and verify health**

Run:

```bash
docker compose up -d
docker compose ps
curl --fail --silent http://127.0.0.1:8080/health
```

Expected: PostgreSQL, Redis, and Sub2API are healthy, and health returns `{"status":"ok"}`.

- [ ] **Step 7: Perform one controlled 1080p request**

Before sending it, report the configured per-second price and the total expected charge for the minimum supported duration. Send one text-to-video request using:

```json
{
  "model": "grok-imagine-video-1.5",
  "prompt": "calm ocean waves, fixed camera",
  "resolution": "1080p",
  "duration": 5
}
```

Verify the upstream request/log preserves `grok-imagine-video-1.5`, `1080p`, and `5`; verify the usage row records the same billing model, resolution, and duration. Do not expose the API key or upstream key in evidence.

- [ ] **Step 8: Leave the validated stack running**

Record the active container image digest, health result, request ID, and redacted billing evidence. Retain the previous official image digest and backup checksum for rollback.
