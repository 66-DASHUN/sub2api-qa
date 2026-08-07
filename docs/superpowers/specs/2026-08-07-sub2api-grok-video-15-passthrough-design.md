# Sub2API Grok Video 1.5 Passthrough Design

## Objective

Fork the official Sub2API `v0.1.171` release and maintain a minimal downstream patch that preserves `grok-imagine-video-1.5` for text-to-video requests. Build the fork as a private GHCR image named `ghcr.io/66-dashun/sub2api:QA`, deploy it only to `47.253.211.154`, verify `1080p` forwarding and billing behavior, and leave that test service running.

## Scope

The source baseline is the official tag `v0.1.171`. Its annotated tag object is
`afd154b92aac36c6dafb1fa8e181ca827c78c465`, and the source commit peeled from
that tag is `f0e7a9c7a23a7d02fb159b62fa809621eb0475a6`.

The production behavior change is limited to `backend/internal/service/grok_media.go`:

- Remove the built-in text-to-video rewrite from `grok-imagine-video-1.5` to `grok-imagine-video`.
- Preserve the existing image endpoint aliases.
- Preserve image-to-video processing, request normalization, account-level model mapping, scheduling, billing, database schemas, and administrator configuration.

The patch must not change pricing data, group pricing, account credentials, channel configuration, database migrations, Redis behavior, or the deployment on `142.214.159.77`.

## Expected Behavior

For a text-to-video request:

```json
{
  "model": "grok-imagine-video-1.5",
  "prompt": "waves on a beach",
  "resolution": "1080p",
  "duration": 5
}
```

Sub2API must forward the same model and resolution to the selected upstream account:

```json
{
  "model": "grok-imagine-video-1.5",
  "prompt": "waves on a beach",
  "resolution": "1080p",
  "duration": 5
}
```

Account-level model mapping remains authoritative. If an account maps `grok-imagine-video-1.5` to a vendor-specific model, mapping is applied after endpoint normalization. Billing must continue to use the requested `grok-imagine-video-1.5` model and the forwarded resolution/duration.

## Repository And Patch Strategy

Create the ordinary repository `66-DASHUN/sub2api-qa` as `origin` and use
`Wei-Shaw/sub2api` as `upstream`. Leave the existing `66-DASHUN/sub2api` fork
unchanged; GitHub does not allow the account to create a second fork in the
same fork network.

Do not place the downstream behavior change directly on the fork's synchronized `main` branch. Create `qa/grok-video-1080p` from the official `v0.1.171` tag and keep the behavior change in one isolated commit:

```text
fix: preserve grok video 1.5 text-to-video model
```

The design document and build workflow may use separate commits. The behavior patch commit must contain only the affected Go implementation and tests so it can be cherry-picked onto later official release tags.

For each future upstream release:

1. Fetch the new official tag from `upstream`.
2. Create a temporary upgrade branch from that tag.
3. Cherry-pick the isolated behavior patch commit.
4. Resolve conflicts explicitly if upstream changed the same model-normalization function or tests.
5. Run the targeted and complete backend test suites.
6. Build and publish a new immutable image tag.
7. Move the mutable `QA` tag only after server verification succeeds.

No automatic merge from upstream to the running server is allowed.

## Test Design

Follow test-driven development. Change the existing expectations first and run them against unmodified `v0.1.171`; they must fail for the known fallback behavior.

Required regression coverage:

- `NormalizeGrokMediaModelForEndpoint` preserves `grok-imagine-video-1.5` when `hasInputImage` is false.
- Text-to-video forwarding preserves `model`, `resolution`, and `duration` in the upstream request body.
- Account model mapping uses the `grok-imagine-video-1.5` key rather than the old fallback key.
- Billing model remains `grok-imagine-video-1.5` for text-to-video.
- Existing image alias and image-to-video tests remain green.

Run the focused service tests first, then the complete backend tests. The image build starts only after all required tests pass.

## Image Build And Security

GitHub Actions builds the official multi-stage `Dockerfile` and publishes a private GHCR package.

Required tags:

```text
ghcr.io/66-dashun/sub2api:QA
ghcr.io/66-dashun/sub2api:qa-v0.1.171-<commit>
```

The immutable commit tag is the rollback target. `QA` is the mutable tag used by the test server.

The build workflow uses GitHub's scoped `GITHUB_TOKEN` with `packages: write`. It must not accept application API keys, database credentials, SSH credentials, or server `.env` content as build arguments or workflow inputs.

Before publishing, verify that the Docker build context and final image exclude:

- `.env` files and local credential files.
- PostgreSQL and Redis data directories.
- Sub2API runtime `data` directories and backups.
- Logs, API keys, upstream keys, SSH keys, and GitHub personal access tokens.

The final image contains only the application, frontend assets, resources, runtime dependencies, and entrypoint supplied by the official Dockerfile.

## Server Deployment

The deployment target is `47.253.211.154`. Do not modify `142.214.159.77`.

Before changing the test deployment:

1. Verify server identity and current Compose project directory.
2. Record the currently running image digest.
3. Create and validate a cold or logical backup of Compose files, `.env`, application data, PostgreSQL data, and Redis data.
4. Authenticate to private GHCR using a token limited to `read:packages`.

Change only the Sub2API service image reference to `ghcr.io/66-dashun/sub2api:QA`. Preserve the existing PostgreSQL, Redis, ports, volumes, environment settings, and restart policy.

Start the stack and require all of the following before declaring success:

- PostgreSQL, Redis, and Sub2API containers are healthy.
- `GET /health` returns HTTP 200 with `{"status":"ok"}`.
- Startup logs contain no fatal database migration, Redis, or configuration errors.
- A controlled `grok-imagine-video-1.5` text-to-video request with `resolution: "1080p"` reaches the upstream without being rewritten.
- Usage records identify `grok-imagine-video-1.5` and the configured duration/resolution.

After validation, leave the 47 server stack running. Retain the previous official image digest and the immutable QA image tag for rollback.

## Rollback

Rollback changes only the Sub2API image reference back to the recorded previous digest or immutable image tag, then recreates the Sub2API service while preserving database and Redis volumes.

If the new image runs a database migration that prevents the previous image from starting, stop the stack and restore the verified pre-deployment backup before starting the previous image.

## Success Criteria

- The QA repository branch is based exactly on official `v0.1.171` source
  commit `f0e7a9c7a23a7d02fb159b62fa809621eb0475a6`.
- The downstream behavior change is isolated in one cherry-pickable commit.
- Focused and complete backend tests pass.
- The private GHCR image has both `QA` and immutable tags and contains no runtime secrets or data.
- The 47 server uses the custom image, passes health checks, forwards 1.5 text-to-video at `1080p`, records correct usage, and remains running.
- The 142 server is unchanged.
