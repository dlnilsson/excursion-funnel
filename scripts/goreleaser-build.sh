#!/usr/bin/env bash
# Builds the Linux amd64 binary with GoReleaser from Linux.
#
# The host toolchain links against the host glibc, so a binary built on a
# rolling distribution will not start on an older target. This builds inside
# the GoReleaser Cross container instead, which ships an older glibc.
#
# Usage: ./scripts/goreleaser-build.sh
set -euo pipefail

IMAGE=${IMAGE:-ghcr.io/goreleaser/goreleaser-cross@sha256:3ce3506ee9179c4122ba0b5dc13ab564ff259fb65f45bfad005ddd5e4a3d326d}

for tool in goreleaser docker go npm; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "error: $tool was not found." >&2
        exit 127
    fi
done

if ! docker info --format '{{.ServerVersion}}' >/dev/null 2>&1; then
    echo 'error: docker is not running.' >&2
    exit 1
fi

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
dist_path=$repo_root/dist
module_cache=$(go env GOMODCACHE)
workspace=$(mktemp -d -t excursion-funnel-goreleaser-XXXXXXXX)
trap 'rm -rf -- "$workspace"' EXIT

mkdir -p "$dist_path"

# The container runs the build with --skip=before, so the UI assets the build
# embeds have to exist in the source tree beforehand.
echo 'Vendoring UI assets...'
npm ci --prefix "$repo_root"
npm run vendor-ui --prefix "$repo_root"

# Warm the local module cache so the container can compile with no network.
echo 'Preparing Linux dependencies...'
(cd "$repo_root" && go mod download)

if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
    echo 'Pulling GoReleaser Cross image...'
    docker pull "$IMAGE"
fi

build_command=$(cat <<'INNER'
set -eu
tar -C /source --exclude='./dist' --exclude='./node_modules' -cf - . | tar -C /workspace -xf -
cd /workspace
exec goreleaser build --snapshot --single-target --skip=before --output /output/ef-linux-amd64
INNER
)

echo 'Building Linux amd64 with GoReleaser Cross...'
docker run --rm --network none --pull never \
    --user "$(id -u):$(id -g)" \
    --mount "type=bind,source=$repo_root,target=/source,readonly" \
    --mount "type=bind,source=$workspace,target=/workspace" \
    --mount "type=bind,source=$dist_path,target=/output" \
    --mount "type=bind,source=$module_cache,target=/gomodcache" \
    --workdir /workspace \
    --env HOME=/tmp \
    --env GOMODCACHE=/gomodcache \
    --env GOCACHE=/tmp/gocache \
    --env GOPROXY=off \
    --env GIT_CONFIG_COUNT=1 \
    --env GIT_CONFIG_KEY_0=safe.directory \
    --env GIT_CONFIG_VALUE_0=/workspace \
    --entrypoint sh \
    "$IMAGE" -lc "$build_command"

echo 'Built:'
echo "  $dist_path/ef-linux-amd64"
