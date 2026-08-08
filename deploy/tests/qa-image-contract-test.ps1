$ErrorActionPreference = 'Stop'

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path

function Require-Match {
    param(
        [Parameter(Mandatory = $true)][string]$Text,
        [Parameter(Mandatory = $true)][string]$Pattern,
        [Parameter(Mandatory = $true)][string]$Message
    )
    if ($Text -notmatch $Pattern) {
        throw $Message
    }
}

$dockerfile = Get-Content (Join-Path $repoRoot 'Dockerfile') -Raw
$deployDockerfile = Get-Content (Join-Path $repoRoot 'deploy\Dockerfile') -Raw
$compose = Get-Content (Join-Path $repoRoot 'deploy\docker-compose.yml') -Raw
$composeOverride = Get-Content (Join-Path $repoRoot 'deploy\docker-compose.qa-update.yml') -Raw
$helperConfig = Get-Content (Join-Path $repoRoot 'deploy\image-updater\config.example.yaml') -Raw
$workflow = Get-Content (Join-Path $repoRoot '.github\workflows\qa-image.yml') -Raw

foreach ($file in @($dockerfile, $deployDockerfile)) {
    Require-Match $file 'ARG VERSION' 'VERSION build argument is missing'
    Require-Match $file 'ARG SOURCE_REPOSITORY' 'SOURCE_REPOSITORY build argument is missing'
    Require-Match $file 'org\.opencontainers\.image\.version' 'OCI version label is missing'
    Require-Match $file 'org\.opencontainers\.image\.source' 'OCI source label is missing'
    Require-Match $file 'org\.opencontainers\.image\.revision' 'OCI revision label is missing'
}

Require-Match $compose 'ghcr\.io/66-dashun/sub2api:\$\{SUB2API_VERSION' 'Compose does not use the private exact-version image'
Require-Match $compose 'UPDATE_MODE=\$\{UPDATE_MODE:-manual\}' 'Base QA Compose must disable in-place binary updates'
Require-Match $compose 'UPDATE_RELEASE_REPO=\$\{UPDATE_RELEASE_REPO:-\}' 'Base QA Compose must not select a binary release repository'
Require-Match $compose 'UPDATE_RELEASE_TAG_PREFIX=\$\{UPDATE_RELEASE_TAG_PREFIX:-\}' 'Base QA Compose must not select a binary release prefix'
Require-Match $compose 'sub2api_data:/app/data' 'Sub2API data volume is missing'
Require-Match $compose 'postgres_data:/var/lib/postgresql/data' 'PostgreSQL data volume is missing'
Require-Match $compose 'redis_data:/data' 'Redis data volume is missing'
if ($compose -match 'target:\s*/run/sub2api-image-updater') {
    throw 'The base Compose file must not mount helper paths'
}
Require-Match $composeOverride 'UPDATE_MODE:\s*container' 'QA override does not enable container update mode'
Require-Match $composeOverride 'UPDATE_RELEASE_REPO:\s*66-DASHUN/sub2api-qa' 'QA override release repository is missing'
Require-Match $composeOverride 'UPDATE_RELEASE_TAG_PREFIX:\s*qa-v' 'QA override release prefix is missing'
Require-Match $composeOverride 'target:\s*/run/secrets/sub2api-updater-client-token' 'Updater client token must use a path outside the read-only socket mount'
Require-Match $composeOverride 'UPDATE_HELPER_TOKEN_FILE:\s*/run/secrets/sub2api-updater-client-token' 'Updater client token configuration must match the secret mount target'
if ($composeOverride -match 'target:\s*/run/sub2api-image-updater/client-token') {
    throw 'Updater client token must not be nested below the read-only socket mount'
}
Require-Match $composeOverride 'create_host_path:\s*false' 'Updater bind mounts may create missing secret paths'
Require-Match $helperConfig 'compose_override_file:\s*/root/sub2api/docker-compose\.qa-update\.yml' 'Updater must preserve the QA Compose override when recreating the application'
if (($compose + $composeOverride) -match 'docker\.sock') {
    throw 'The Docker socket must not be mounted into the application container'
}

Require-Match $workflow 'workflow_dispatch:' 'QA workflow must be manually dispatchable'
Require-Match $workflow 'version:' 'QA workflow version input is missing'
Require-Match $workflow 'ghcr\.io/66-dashun/sub2api:\$\{\{' 'Immutable image tag is missing'
Require-Match $workflow 'qa-v(\$\{QA_VERSION\}|\$\{\{)' 'QA release tag is missing'
Require-Match $workflow 'packages: write' 'GHCR publish permission is missing'
Require-Match $workflow 'contents: write' 'QA release permission is missing'
Require-Match $workflow 'Wei-Shaw/sub2api\.git' 'Official upstream tag source is missing'
Require-Match $workflow 'sub2api-image-updater-linux-amd64' 'Updater release artifact is missing'
Require-Match $workflow 'Validate immutable version state' 'Immutable version state validation is missing'
Require-Match $workflow 'group:\s*qa-container-\$\{\{ inputs\.version' 'QA concurrency is not serialized by version'
Require-Match $workflow 'actual_revision' 'Existing images are not bound to the source commit'
Require-Match $workflow 'image_exists=true' 'Interrupted image publishing cannot be resumed safely'
Require-Match $workflow 'targetCommitish' 'Existing releases are not bound to the source commit'
Require-Match $workflow 'gh api .*git/ref/tags' 'Private repository tag checks must use the authenticated GitHub API'
Require-Match $workflow '(?s)Create or resume QA release metadata.*--notes .*\r?\n\s+fi\s*$' 'QA release script conditional is not closed'
if ($workflow -match 'git ls-remote --tags origin') {
    throw 'Private repository tag checks must not depend on removed checkout credentials'
}
if ($workflow -match '--clobber|gh release edit') {
    throw 'The QA workflow must not overwrite an existing image release'
}

Write-Output 'QA image contracts passed.'
