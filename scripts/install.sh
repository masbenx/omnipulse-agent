#!/usr/bin/env bash
set -euo pipefail

SOURCE="release"
VERSION="latest"
PREFIX="/usr/local/bin"
TOKEN=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --source)
      SOURCE="$2"
      shift 2
      ;;
    --version)
      VERSION="$2"
      shift 2
      ;;
    --prefix)
      PREFIX="$2"
      shift 2
      ;;
    --token)
      TOKEN="$2"
      shift 2
      ;;
    *)
      echo "unknown arg: $1" >&2
      exit 1
      ;;
  esac
done

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
    mingw*|msys*|cygwin*) echo "windows" ;;
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
  if [[ -n "$TOKEN" ]]; then
    auth=( -H "Authorization: token $TOKEN" )
  fi
  local tag
  tag=$(curl -fsSL "${auth[@]}" "$url" | sed -n 's/.*"tag_name": "\([^"]*\)".*/\1/p' | head -n 1)
  if [[ -z "$tag" ]]; then
    echo "unable to resolve latest release tag" >&2
    exit 1
  fi
  echo "$tag"
}

WORKDIR="$(mktemp -d)"
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT

if [[ "$SOURCE" == "release" ]]; then
  require_cmd curl
  os=$(normalize_os)
  arch=$(normalize_arch)
  ext=""
  if [[ "$os" == "windows" ]]; then
    ext=".exe"
  fi

  tag="$VERSION"
  if [[ "$VERSION" == "latest" ]]; then
    tag=$(fetch_latest_tag)
  fi

  asset="omnipulse-agent-${os}-${arch}${ext}"
  url="https://github.com/masbenx/omnipulse-agent/releases/download/${tag}/${asset}"
  auth=()
  if [[ -n "$TOKEN" ]]; then
    auth=( -H "Authorization: token $TOKEN" )
  fi

  curl -fL "${auth[@]}" -o "$WORKDIR/$asset" "$url"
  install -m 0755 "$WORKDIR/$asset" "$PREFIX/omnipulse-agent${ext}"
  echo "installed to $PREFIX/omnipulse-agent${ext}"
  exit 0
fi

require_cmd go

REPO_DIR="$WORKDIR/omnipulse-agent"
if [[ "$SOURCE" == "git" ]]; then
  require_cmd git
  git clone --depth 1 --branch "$VERSION" https://github.com/masbenx/omnipulse-agent.git "$REPO_DIR"
elif [[ "$SOURCE" == "curl" ]]; then
  require_cmd curl
  ARCHIVE="$WORKDIR/omnipulse-agent.tar.gz"
  REF="heads"
  if [[ "$VERSION" == v* ]]; then
    REF="tags"
  fi
  curl -fsSL "https://github.com/masbenx/omnipulse-agent/archive/refs/${REF}/${VERSION}.tar.gz" -o "$ARCHIVE"
  mkdir -p "$REPO_DIR"
  tar -xzf "$ARCHIVE" -C "$REPO_DIR" --strip-components=1
else
  echo "invalid source: $SOURCE (use release|git|curl)" >&2
  exit 1
fi

cd "$REPO_DIR"
CGO_ENABLED=0 go build -o "$WORKDIR/omnipulse-agent" .

install -m 0755 "$WORKDIR/omnipulse-agent" "$PREFIX/omnipulse-agent"
echo "installed to $PREFIX/omnipulse-agent"
