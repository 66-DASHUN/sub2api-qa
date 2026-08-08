# Sub2API QA Custom Image Page Update Design

## Goal

Preserve the existing administrator workflow:

```text
Check for updates -> Update -> Restart -> wait for service
```

while changing the deployed artifact from the official Sub2API build to the
private QA build. A page-triggered update must stage and apply an immutable
`ghcr.io/66-dashun/sub2api:X.Y.Z` image. It must never install an official
binary or an unverified mutable image.

## Confirmed Requirements

- The administrator keeps using the existing version badge and update dialog.
- Updates run only after an administrator clicks the page controls. There is no
  timer, background polling updater, or unattended container replacement.
- The running image is always from `ghcr.io/66-dashun/sub2api`.
- The embedded application version and production image tag use the same
  `X.Y.Z` value as the corresponding official release.
- The QA source is merged with the official release and tested before the QA
  image becomes visible to the application as an available update.
- PostgreSQL data, Redis data, application data, users, keys, and configuration
  remain outside the image and survive container replacement.
- The private repository and GHCR package remain private.
- Registry credentials remain on the host. They are not stored in the web UI,
  application database, Git repository, image layers, Compose environment, or
  application container.
- Existing official update behavior remains available when container-image
  update mode is not configured. This keeps the fork easy to merge with future
  upstream changes.

## Current State Evidence

The local branch is `qa/grok-video-1080p` at `dbf94e3f9`. Its current QA
workflow publishes a mutable `QA` image with embedded version `0.1.171-qa`.
The application update service still has `Wei-Shaw/sub2api` hard-coded and
downloads release binaries into the running filesystem.

A read-only inspection of `47.253.211.154` on 2026-08-08 found:

- Compose project directory: `/root/sub2api`
- Compose file: `/root/sub2api/docker-compose.yml`
- configured application image: `weishaw/sub2api:latest`
- image version: `0.1.171`
- application data: host bind mount to `/app/data`
- `sub2api`, PostgreSQL, and Redis containers were stopped before this work
  began; this design does not treat that state as an update failure

No server files, images, containers, databases, users, or keys were changed by
that inspection.

## Rejected Approaches

### Continue replacing the binary inside the container

This is the smallest code change but does not update the Docker image. The
replacement lives in the writable container layer and is lost when Compose
recreates the container. It therefore does not satisfy the requested artifact
or persistence model.

### Mount the Docker socket into the Sub2API application container

This lets the web application control Docker directly, but compromise of the
application would become compromise of the host. It also mixes HTTP request
handling with host lifecycle management. The Docker socket will not be mounted
into the application container.

### Use a periodically polling updater

Watchtower or a timer can follow a mutable tag, but that changes the requested
manual approval workflow and weakens exact-version rollback. No periodic
updater is part of this design.

## Architecture

The implementation adds an image-update strategy behind the existing update
service interface and a small, host-installed update helper.

```text
Administrator browser
        |
        | existing admin update API
        v
Sub2API container
        |
        | authenticated HTTP over a mounted Unix socket
        v
Host image-update helper
        |                         |
        | GitHub release metadata| Docker CLI + Compose
        v                         v
private QA repository       private GHCR / host Docker
```

The application never receives registry credentials or a Docker socket. The
application's existing GitHub release client reads a separate, read-only
repository token from a secret file; that token has no package-write access.
The helper never receives an arbitrary command, Compose path, service name,
image name, or registry address from an HTTP request. Those values are fixed in
the helper's root-owned configuration.

## Version And Release Contract

Official tags remain untouched so upstream tag identity can still be audited.
A QA release uses these related identifiers:

```text
official upstream tag: v0.1.172
QA GitHub release tag: qa-v0.1.172
embedded app version: 0.1.172
immutable image tag:  ghcr.io/66-dashun/sub2api:0.1.172
optional preview tag: ghcr.io/66-dashun/sub2api:QA
```

The QA workflow must verify that the official `vX.Y.Z` commit is an ancestor of
the QA release commit. It then runs tests, builds the application with version
`X.Y.Z`, pushes the immutable image, verifies its label and digest, and only
then creates the `qa-vX.Y.Z` GitHub release. Update checks ignore releases that
do not have the `qa-v` prefix.

This ordering prevents the page from announcing a version whose QA image has
not finished building.

## Runtime Components

### Application update strategy

`UpdateService` will retain the current binary strategy and gain an optional
container-image strategy selected by configuration. Image mode uses:

- `UPDATE_MODE=container`
- `UPDATE_RELEASE_REPO=66-DASHUN/sub2api-qa`
- `UPDATE_RELEASE_TAG_PREFIX=qa-v`
- `UPDATE_HELPER_SOCKET=/run/sub2api-image-updater/updater.sock`
- `UPDATE_HELPER_TOKEN_FILE=/run/secrets/sub2api-updater-client-token`

The helper client is isolated behind a small interface. Unit tests can exercise
update behavior without Docker, SSH, or GitHub.

In image mode:

- update checks read only QA release metadata
- `PerformUpdate` asks the helper to stage the latest immutable image
- version rollback asks the helper to stage the selected immutable image
- `RestartService` asks the helper to apply the staged image
- the local `.backup` binary rollback path is not offered because container
  rollback uses immutable image tags

The frontend retains its current controls and service-recovery polling. It only
needs enough update metadata to suppress the unsupported local-binary rollback
choice in image mode.

### Host image-update helper

The helper is a separate, static Go binary managed by systemd. It runs as root
because Docker and Compose require host privileges, but listens only on a Unix
socket under `/run/sub2api-image-updater`. The socket and an independent client
token restrict callers to the Sub2API container.

Root-owned helper configuration fixes:

- image repository: `ghcr.io/66-dashun/sub2api`
- Compose project directory: `/root/sub2api`
- Compose file: `/root/sub2api/docker-compose.yml`
- Compose environment file: `/root/sub2api/.env`
- Compose service: `sub2api`
- GitHub release repository: `66-DASHUN/sub2api-qa`
- accepted release prefix: `qa-v`
- state directory: `/var/lib/sub2api-image-updater`

The API accepts only normalized `X.Y.Z` versions. The helper constructs every
image reference itself and invokes Docker with fixed argument arrays through
`os/exec`, never through a shell.

### Secrets

The server holds three distinct values:

1. Docker's root-owned GHCR credential in `/root/.docker/config.json`, with
   package-read scope only.
2. A read-only GitHub metadata token used by the application release client to
   read releases from the private repository. It has repository contents-read
   scope only and is mounted as a secret file, never placed in the image or
   database.
3. A random helper client token. The host copy is root-owned; a read-only copy
   is mounted into the Sub2API container solely to authenticate calls over the
   Unix socket.

No secret value is logged. Error messages redact authorization headers and
never include command environments.

## Update Flow

### Check

1. The frontend calls the existing `GET /admin/system/check-updates` endpoint.
2. In image mode, Sub2API uses its existing GitHub release client against the
   configured private repository and filters for `qa-vX.Y.Z` releases.
3. The release client reads only the separate read-only metadata token.
4. Sub2API compares `X.Y.Z` with its embedded current version and returns the
   existing version response plus `update_mode: container`.

### Stage

1. The administrator clicks Update.
2. Sub2API calls the helper's stage endpoint with only `X.Y.Z`.
3. The helper confirms a matching QA release exists.
4. The helper pulls `ghcr.io/66-dashun/sub2api:X.Y.Z` using host credentials.
5. The helper resolves the pulled digest and verifies the image version/source
   labels.
6. The helper atomically writes staged state containing the requested version,
   digest, and currently configured version.
7. Sub2API returns the existing `need_restart: true` response. The running
   container has not changed yet.

### Apply On Restart

1. The administrator clicks Restart.
2. Sub2API asks the helper to apply the staged operation.
3. The helper acknowledges the request before starting replacement so the HTTP
   response can reach the browser.
4. The helper atomically changes only `SUB2API_VERSION` in `/root/sub2api/.env`.
5. It runs fixed Compose commands to recreate only the `sub2api` service with
   the already-pulled immutable image. PostgreSQL and Redis are not recreated.
6. It waits for the application health check and confirms the running image
   digest and embedded version.
7. On success it clears staged state. The existing frontend health polling
   reconnects to the new application.

## Failure And Recovery

- Concurrent stage/apply operations return Conflict and do not overlap.
- A failed release lookup or image pull leaves the running container and `.env`
  unchanged.
- A label, source, version, or digest mismatch rejects the image before apply.
- If Compose replacement or the health check fails, the helper restores the
  previous `SUB2API_VERSION`, recreates only `sub2api` from the previous
  immutable image, and records both the primary and recovery outcomes.
- If recovery also fails, the helper retains state and emits exact manual
  recovery commands without printing secrets. It does not touch PostgreSQL,
  Redis, or application data.
- A helper timeout is surfaced as an update failure; the application never
  silently falls back to the official binary updater while image mode is set.
- Restart with no staged image follows the existing restart behavior only when
  binary mode is active. In image mode it returns a clear no-staged-update
  conflict instead of recreating an arbitrary image.

## Compose And Persistence

The application service uses an exact version variable:

```yaml
image: ghcr.io/66-dashun/sub2api:${SUB2API_VERSION}
```

The existing `/app/data`, PostgreSQL, and Redis mounts remain unchanged. The
helper socket and client-token mounts are read-only from the application
container's perspective. No database volume, data directory, environment file,
or Compose project is bundled into the image.

The first server migration creates a timestamped backup of Compose and `.env`,
logs into GHCR, stages the already published `0.1.171` QA image, and performs a
controlled application-only replacement. Because the inspected stack is
currently stopped, migration verification must explicitly distinguish
preserving the stopped state from an authorized start of the stack.

## Future Upstream Updates

For each official release:

1. Fetch upstream tags without moving the QA branch.
2. Merge the official release commit into the QA branch.
3. Resolve conflicts while preserving the custom patch and updater integration.
4. Run the full QA suite.
5. Dispatch the QA release workflow for that official version.
6. Confirm the immutable image and `qa-vX.Y.Z` release were published.
7. Open Sub2API and manually perform Check, Update, and Restart.

Custom updater code is confined to new helper files, a small update-strategy
boundary, Compose additions, and the QA workflow. The official binary updater
is retained instead of rewritten, reducing recurring merge conflicts.

## Testing And Verification

Implementation uses test-first development. Required automated evidence:

- update service selects binary or container strategy correctly
- only `qa-vX.Y.Z` releases are considered in image mode
- stage and rollback send normalized versions, never arbitrary image strings
- restart delegates to apply only when container mode has staged state
- helper authentication rejects missing or incorrect client tokens
- helper rejects malformed versions and concurrent operations
- Docker commands contain fixed paths and argument arrays
- stage verifies release, image source, version label, and digest before writing
  state
- `.env` version changes are atomic and preserve every unrelated line
- apply success and failed-health rollback behavior are covered with fake
  command and health runners
- Compose rendering selects the private exact-version image and preserves all
  data mounts
- release workflow validation proves version equality and publish ordering
- existing update-service and frontend update tests continue to pass in binary
  mode

Server verification must show, without reading database or application
secrets:

- configured and running image are the expected private immutable tag/digest
- embedded application version equals the image tag
- only the `sub2api` container ID changes during an update
- PostgreSQL and Redis container IDs and mounts remain unchanged
- application health returns healthy after replacement
- a second update check reports no update
- no periodic updater is running

## Out Of Scope

- Automatically merging upstream releases
- Automatically updating a production server after a CI build
- Publishing the private source repository or GHCR package
- Migrating or modifying PostgreSQL, Redis, users, keys, pricing, or accounts
- Updating unrelated services in the Compose project
