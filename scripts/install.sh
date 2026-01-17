#!/usr/bin/env bash
set -euo pipefail

SOURCE="git"
VERSION="main"
PREFIX="/usr/local/bin"

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
    *)
      echo "unknown arg: $1" >&2
      exit 1
      ;;
  esac
done

if ! command -v go >/dev/null 2>&1; then
  echo "go is required" >&2
  exit 1
fi

WORKDIR="$(mktemp -d)"
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT

REPO_DIR="$WORKDIR/omnipulse-agent"
if [[ "$SOURCE" == "git" ]]; then
  if ! command -v git >/dev/null 2>&1; then
    echo "git is required for source=git" >&2
    exit 1
  fi
  git clone --depth 1 --branch "$VERSION" https://github.com/masbenx/omnipulse-agent.git "$REPO_DIR"
elif [[ "$SOURCE" == "curl" ]]; then
  if ! command -v curl >/dev/null 2>&1; then
    echo "curl is required for source=curl" >&2
    exit 1
  fi
  ARCHIVE="$WORKDIR/omnipulse-agent.tar.gz"
  curl -fsSL "https://github.com/masbenx/omnipulse-agent/archive/refs/heads/${VERSION}.tar.gz" -o "$ARCHIVE"
  mkdir -p "$REPO_DIR"
  tar -xzf "$ARCHIVE" -C "$REPO_DIR" --strip-components=1
else
  echo "invalid source: $SOURCE (use git|curl)" >&2
  exit 1
fi

cd "$REPO_DIR"
CGO_ENABLED=0 go build -o "$WORKDIR/omnipulse-agent" .

install -m 0755 "$WORKDIR/omnipulse-agent" "$PREFIX/omnipulse-agent"
echo "installed to $PREFIX/omnipulse-agent"
