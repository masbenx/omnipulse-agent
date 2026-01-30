#!/usr/bin/env bash
set -euo pipefail

SOURCE="release"
VERSION="latest"
PREFIX="/usr/local/bin"
TOKEN=""
URL=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --source=*) SOURCE="${1#*=}"; shift ;;
    --source) SOURCE="${2:-}"; shift 2 ;;
    --version=*) VERSION="${1#*=}"; shift ;;
    --version) VERSION="${2:-}"; shift 2 ;;
    --prefix=*) PREFIX="${1#*=}"; shift ;;
    --prefix) PREFIX="${2:-}"; shift 2 ;;
    --token=*) TOKEN="${1#*=}"; shift ;;
    --token) TOKEN="${2:-}"; shift 2 ;;
    --url=*) URL="${1#*=}"; shift ;;
    --url) URL="${2:-}"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

if [[ -z "$TOKEN" ]]; then
  echo "Error: --token is required" >&2
  exit 1
fi

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "$1 is required" >&2
    exit 1
  fi
}

normalize_os() {
  case "$(uname -s | tr '[:upper:]' '[:lower:]')" in
    linux*) echo "linux" ;;
    darwin*) echo "darwin" ;;
    *) echo "unsupported os" >&2; exit 1 ;;
  esac
}

normalize_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *) echo "unsupported arch" >&2; exit 1 ;;
  esac
}

fetch_latest_tag() {
  local url="https://api.github.com/repos/masbenx/omnipulse-agent/releases/latest"
  local auth=()
  # Only use token for GitHub if it looks like a GitHub PAT (begins with ghp_)
  if [[ "$TOKEN" == ghp_* ]]; then
    auth=( -H "Authorization: token $TOKEN" )
  fi
  
  local tag
  tag=$(curl -fsSL "${auth[@]}" "$url" | sed -n 's/.*"tag_name": "\([^"]*\)".*/\1/p' | head -n 1)
  if [[ -z "$tag" ]]; then
    # Final fallback: try without any auth if ghp check failed
    tag=$(curl -fsSL "$url" | sed -n 's/.*"tag_name": "\([^"]*\)".*/\1/p' | head -n 1)
  fi

  if [[ -z "$tag" ]]; then
    echo "unable to resolve latest release tag" >&2
    exit 1
  fi
  echo "$tag"
}

WORKDIR="$(mktemp -d)"
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT

OS=$(normalize_os)
ARCH=$(normalize_arch)
BINARY_NAME="omnipulse-agent"
if [[ "$OS" == "windows" ]]; then BINARY_NAME="omnipulse-agent.exe"; fi

if [[ "$SOURCE" == "release" ]]; then
  require_cmd curl
  tag="$VERSION"
  if [[ "$VERSION" == "latest" ]]; then
    tag=$(fetch_latest_tag)
  fi

  asset="omnipulse-agent-${OS}-${ARCH}"
  if [[ "$OS" == "windows" ]]; then asset="${asset}.exe"; fi
  
  url="https://github.com/masbenx/omnipulse-agent/releases/download/${tag}/${asset}"
  auth=()
  if [[ "$TOKEN" == ghp_* ]]; then
     auth=( -H "Authorization: token $TOKEN" )
  fi

  echo "Downloading $asset ($tag)..."
  curl -fL "${auth[@]}" -o "$WORKDIR/$BINARY_NAME" "$url"
else
  require_cmd go
  if [[ "$SOURCE" == "git" ]]; then
    require_cmd git
    git clone --depth 1 --branch "$VERSION" https://github.com/masbenx/omnipulse-agent.git "$WORKDIR/src"
  elif [[ "$SOURCE" == "curl" ]]; then
    REF="heads"; if [[ "$VERSION" == v* ]]; then REF="tags"; fi
    curl -fsSL "https://github.com/masbenx/omnipulse-agent/archive/refs/${REF}/${VERSION}.tar.gz" | tar -xz -C "$WORKDIR" --strip-components=1
  fi
  cd "$WORKDIR"
  CGO_ENABLED=0 go build -o "$BINARY_NAME" .
fi

# Install binary
echo "Installing binary to $PREFIX/$BINARY_NAME..."
sudo install -m 0755 "$WORKDIR/$BINARY_NAME" "$PREFIX/$BINARY_NAME"

# Auto-configure service (if on Linux)
if [[ "$OS" == "linux" && -n "$URL" ]]; then
  echo "Registering systemd service..."
  sudo "$PREFIX/$BINARY_NAME" install --token "$TOKEN" --url "$URL"
  sudo "$PREFIX/$BINARY_NAME" restart
  echo "Service installed and started!"
elif [[ "$OS" == "linux" ]]; then
  echo "Binary installed. To register as service, run:"
  echo "  sudo $PREFIX/$BINARY_NAME install --token $TOKEN --url <YOUR_API_URL>"
fi

echo "Successfully installed OmniPulse Agent!"
