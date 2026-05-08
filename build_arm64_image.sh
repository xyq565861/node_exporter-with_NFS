#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${SCRIPT_DIR}"

if [[ ! -f "Makefile" || ! -f "Dockerfile" ]]; then
    echo "Error: run this script from the repository root."
    exit 1
fi

if ! command -v make >/dev/null 2>&1; then
    echo "Error: 'make' is required."
    exit 1
fi

if ! command -v go >/dev/null 2>&1; then
    echo "Error: 'go' is required."
    exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
    echo "Error: 'docker' is required."
    exit 1
fi

IMAGE_REPO="${IMAGE_REPO:-local}"
IMAGE_NAME="${IMAGE_NAME:-node-exporter-nfs}"
IMAGE_TAG="${IMAGE_TAG:-arm64}"
VARIANT="${VARIANT:-busybox}"
PUSH="${PUSH:-false}"
BUILDER_NAME="${BUILDER_NAME:-node-exporter-arm64-builder}"
GOPROXY_VALUE="${GOPROXY:-https://goproxy.cn,direct}"
MOD_DOWNLOAD_RETRIES="${MOD_DOWNLOAD_RETRIES:-3}"
DISTROLESS_BASE_IMAGE="${DISTROLESS_BASE_IMAGE:-gcr.io/distroless/static-debian13}"

usage() {
    cat <<'EOF'
Usage:
    ./build_arm64_image.sh [--repo <repo>] [--name <name>] [--tag <tag>] [--variant busybox|distroless] [--goproxy <url>] [--distroless-base <image>] [--push]

Examples:
  ./build_arm64_image.sh
  ./build_arm64_image.sh --repo yourrepo --name node-exporter-nfs --tag v1.0.0
    ./build_arm64_image.sh --goproxy https://goproxy.cn,direct
    ./build_arm64_image.sh --variant distroless --distroless-base gcr.m.daocloud.io/distroless/static-debian13
  ./build_arm64_image.sh --variant distroless --push --repo yourrepo --name node-exporter-nfs --tag v1.0.0

Environment variable equivalents:
    IMAGE_REPO, IMAGE_NAME, IMAGE_TAG, VARIANT, PUSH, BUILDER_NAME, GOPROXY, MOD_DOWNLOAD_RETRIES, DISTROLESS_BASE_IMAGE
EOF
}

download_go_modules() {
        local attempt

        for ((attempt = 1; attempt <= MOD_DOWNLOAD_RETRIES; attempt++)); do
                if GOPROXY="${GOPROXY_VALUE}" GO111MODULE=on go mod download; then
                        return 0
                fi

                if (( attempt < MOD_DOWNLOAD_RETRIES )); then
                        echo "go mod download failed on attempt ${attempt}/${MOD_DOWNLOAD_RETRIES}, retrying..."
                fi
        done

        echo "Error: failed to download Go modules after ${MOD_DOWNLOAD_RETRIES} attempts."
        echo "Hint: rerun with a reachable mirror, for example:"
        echo "  GOPROXY=https://goproxy.cn,direct ./build_arm64_image.sh"
        exit 1
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --repo)
            IMAGE_REPO="$2"
            shift 2
            ;;
        --name)
            IMAGE_NAME="$2"
            shift 2
            ;;
        --tag)
            IMAGE_TAG="$2"
            shift 2
            ;;
        --variant)
            VARIANT="$2"
            shift 2
            ;;
        --goproxy)
            GOPROXY_VALUE="$2"
            shift 2
            ;;
        --distroless-base)
            DISTROLESS_BASE_IMAGE="$2"
            shift 2
            ;;
        --push)
            PUSH="true"
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "Unknown argument: $1"
            usage
            exit 1
            ;;
    esac
done

if [[ "${VARIANT}" != "busybox" && "${VARIANT}" != "distroless" ]]; then
    echo "Error: --variant must be 'busybox' or 'distroless'."
    exit 1
fi

if ! docker buildx version >/dev/null 2>&1; then
    echo "Error: docker buildx is required."
    exit 1
fi

if ! docker buildx inspect "${BUILDER_NAME}" >/dev/null 2>&1; then
    docker buildx create --name "${BUILDER_NAME}" --use >/dev/null
else
    docker buildx use "${BUILDER_NAME}" >/dev/null
fi

docker buildx inspect --bootstrap >/dev/null

echo "==> Using GOPROXY ${GOPROXY_VALUE}"
echo "==> Downloading Go modules"
download_go_modules

echo "==> Building linux/arm64 binary"
GOPROXY="${GOPROXY_VALUE}" GO111MODULE=on make build GOOS=linux GOARCH=arm64

DOCKERFILE="Dockerfile"
SUFFIX=""
BUILD_ARGS=(
    --build-arg "ARCH=arm64"
    --build-arg "OS=linux"
)

if [[ "${VARIANT}" == "distroless" ]]; then
    DOCKERFILE="Dockerfile.distroless"
    SUFFIX="-distroless"
    BUILD_ARGS+=(
        --build-arg "DISTROLESS_ARCH=arm64"
        --build-arg "DISTROLESS_BASE_IMAGE=${DISTROLESS_BASE_IMAGE}"
    )
fi

IMAGE_REF="${IMAGE_REPO}/${IMAGE_NAME}:${IMAGE_TAG}${SUFFIX}"

OUTPUT_FLAG="--load"
if [[ "${PUSH}" == "true" ]]; then
    OUTPUT_FLAG="--push"
fi

echo "==> Building image ${IMAGE_REF}"
docker buildx build \
    --platform linux/arm64 \
    -f "${DOCKERFILE}" \
    "${BUILD_ARGS[@]}" \
    -t "${IMAGE_REF}" \
    ${OUTPUT_FLAG} \
    .

echo "==> Done"
echo "Image: ${IMAGE_REF}"
if [[ "${PUSH}" == "true" ]]; then
    echo "Pushed: ${IMAGE_REF}"
else
    echo "Loaded to local Docker image store: ${IMAGE_REF}"
fi
