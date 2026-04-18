#!/usr/bin/env bash
# gitea2forgejo installer (Linux / macOS)
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/pacnpal/gitea2forgejo/main/install.sh | bash
#
# Environment variable overrides:
#   INSTALL_DIR  where to place the binary (default: /usr/local/bin)
#   VERSION      version tag to install (default: latest release)
#   SKIP_DEPS    set to 1 to skip the dependency install step
#
# The script detects OS + CPU, installs the external tools gitea2forgejo
# shells out to (rsync, openssh-client, sqlite3, postgresql-client,
# mysql/mariadb-client, zstd) via the platform package manager, then
# downloads the matching release binary and installs it to INSTALL_DIR
# (prompting for sudo if needed). On macOS it also strips
# com.apple.quarantine + applies an ad-hoc codesign so Gatekeeper doesn't
# refuse the first invocation.

set -euo pipefail

# Everything below runs inside _main(). When invoked via `curl | bash`,
# bash reads function bodies eagerly (looking for the closing `}`),
# so the whole script is parsed into memory BEFORE any subprocess
# starts. Without this wrapper, package-manager child scripts (Homebrew's
# postgres post-install, apt hooks, etc.) can consume remaining stdin
# bytes that were still on their way from curl — truncating mid-install.
_main() {

REPO="pacnpal/gitea2forgejo"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
VERSION="${VERSION:-}"

# ─── pretty output ─────────────────────────────────────────────────────────
if [ -t 1 ]; then
  C_RED='\033[0;31m'; C_GRN='\033[0;32m'; C_YEL='\033[1;33m'; C_BLU='\033[0;34m'
  C_BLD='\033[1m'; C_OFF='\033[0m'
else
  C_RED=''; C_GRN=''; C_YEL=''; C_BLU=''; C_BLD=''; C_OFF=''
fi

info()  { printf "${C_BLU}»${C_OFF} %s\n" "$*"; }
ok()    { printf "${C_GRN}✓${C_OFF} %s\n" "$*"; }
warn()  { printf "${C_YEL}!${C_OFF} %s\n" "$*"; }
die()   { printf "${C_RED}✗${C_OFF} %s\n" "$*" >&2; exit 1; }

# ─── detect environment ────────────────────────────────────────────────────
detect_os() {
  case "$(uname -s)" in
    Linux*)           echo linux ;;
    Darwin*)          echo darwin ;;
    MINGW*|MSYS*|CYGWIN*)
      die "Windows detected — use install.ps1 instead:
  iwr -useb https://raw.githubusercontent.com/${REPO}/main/install.ps1 | iex"
      ;;
    *)
      die "unsupported OS: $(uname -s)"
      ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)    echo amd64 ;;
    aarch64|arm64)   echo arm64 ;;
    *)
      die "unsupported CPU architecture: $(uname -m)"
      ;;
  esac
}

# ─── dependency install ────────────────────────────────────────────────────
sudo_if_needed() {
  if [ "$(id -u)" = 0 ]; then
    "$@"
  else
    sudo "$@"
  fi
}

install_deps() {
  [ "${SKIP_DEPS:-0}" = 1 ] && { info "skipping dependency install (SKIP_DEPS=1)"; return; }

  if [ "$OS" = "darwin" ]; then
    if command -v brew >/dev/null 2>&1; then
      info "installing dependencies via Homebrew ..."
      # macOS has rsync/ssh/sqlite/zstd preinstalled; brew for the DB clients.
      brew install --quiet postgresql@16 mysql-client zstd 2>&1 | sed 's/^/    /' || true
      ok "dependencies installed (or already present)"
    else
      warn "Homebrew not found. Install pg_dump/mysql/zstd manually:"
      warn "  https://brew.sh  then:  brew install postgresql mysql-client zstd"
    fi
    return
  fi

  # Linux: detect package manager.
  if command -v apt-get >/dev/null 2>&1; then
    info "installing dependencies via apt ..."
    # Debian 13+ replaced mysql-client with default-mysql-client.
    sudo_if_needed apt-get update -qq
    if ! sudo_if_needed apt-get install -y --no-install-recommends \
        rsync openssh-client sqlite3 postgresql-client default-mysql-client zstd \
        >/dev/null 2>&1; then
      # Fallback for Debian 12 / older Ubuntu where default-mysql-client is absent.
      sudo_if_needed apt-get install -y --no-install-recommends \
        rsync openssh-client sqlite3 postgresql-client mysql-client zstd || {
          warn "apt install hit errors; some optional packages may be missing"
        }
    fi
    ok "dependencies installed via apt"
  elif command -v dnf >/dev/null 2>&1; then
    info "installing dependencies via dnf ..."
    sudo_if_needed dnf install -y rsync openssh-clients sqlite postgresql mariadb zstd \
      >/dev/null 2>&1 || warn "dnf install hit errors; optional packages may be missing"
    ok "dependencies installed via dnf"
  elif command -v yum >/dev/null 2>&1; then
    info "installing dependencies via yum ..."
    sudo_if_needed yum install -y rsync openssh-clients sqlite postgresql mariadb zstd \
      >/dev/null 2>&1 || warn "yum install hit errors"
    ok "dependencies installed via yum"
  elif command -v pacman >/dev/null 2>&1; then
    info "installing dependencies via pacman ..."
    sudo_if_needed pacman -S --needed --noconfirm \
      rsync openssh sqlite postgresql-libs mariadb-clients zstd \
      >/dev/null 2>&1 || warn "pacman install hit errors"
    ok "dependencies installed via pacman"
  elif command -v zypper >/dev/null 2>&1; then
    info "installing dependencies via zypper ..."
    sudo_if_needed zypper --non-interactive install \
      rsync openssh sqlite3 postgresql mariadb-client zstd \
      >/dev/null 2>&1 || warn "zypper install hit errors"
    ok "dependencies installed via zypper"
  elif command -v apk >/dev/null 2>&1; then
    info "installing dependencies via apk ..."
    sudo_if_needed apk add --no-progress \
      rsync openssh-client sqlite postgresql-client mariadb-client zstd \
      >/dev/null 2>&1 || warn "apk install hit errors"
    ok "dependencies installed via apk"
  else
    warn "no recognized package manager — install these manually:"
    warn "  rsync, openssh, sqlite3, postgresql client, mysql/mariadb client, zstd"
  fi
}

latest_version() {
  # Follow the /releases/latest redirect to avoid needing jq. The Location
  # header ends with the tag name.
  local loc
  loc="$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
        "https://github.com/${REPO}/releases/latest")"
  [ -n "$loc" ] || die "couldn't resolve latest release; try setting VERSION=vX.Y.Z"
  basename "$loc"
}

# ─── main ──────────────────────────────────────────────────────────────────
printf "${C_BLD}gitea2forgejo installer${C_OFF}\n\n"

command -v curl >/dev/null 2>&1 || die "curl is required but not installed"

OS="$(detect_os)"
ARCH="$(detect_arch)"
PLATFORM="${OS}-${ARCH}"
info "platform:     ${PLATFORM}"

install_deps
echo

if [ -z "$VERSION" ]; then
  VERSION="$(latest_version)"
fi
info "version:      ${VERSION}"

BINARY_NAME="gitea2forgejo-${PLATFORM}"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${BINARY_NAME}"
info "source:       ${URL}"
info "install dir:  ${INSTALL_DIR}"
echo

TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT

info "downloading ..."
curl -fL --progress-bar -o "$TMP" "$URL" || die "download failed: ${URL}"
chmod +x "$TMP"

# ─── verify size (anything under 1 MB is almost certainly wrong) ───────────
size=$(wc -c < "$TMP")
if [ "$size" -lt 1048576 ]; then
  die "downloaded file is only ${size} bytes — aborting"
fi
ok "downloaded $((size / 1048576)) MB"

# ─── macOS post-download: strip quarantine + ad-hoc sign ───────────────────
if [ "$OS" = "darwin" ]; then
  info "macOS post-install:"
  if xattr -dr com.apple.quarantine "$TMP" 2>/dev/null; then
    ok "  cleared com.apple.quarantine"
  else
    warn "  xattr -dr com.apple.quarantine failed (non-fatal)"
  fi
  if codesign --force --sign - "$TMP" 2>/dev/null; then
    ok "  ad-hoc codesigned"
  else
    warn "  codesign failed; you may need to click 'Open Anyway' in System Settings → Privacy & Security on first run"
  fi
fi

# ─── install ───────────────────────────────────────────────────────────────
DEST="${INSTALL_DIR}/gitea2forgejo"
if [ -w "$INSTALL_DIR" ] || [ "$(id -u)" = 0 ]; then
  install -m 0755 "$TMP" "$DEST"
else
  warn "${INSTALL_DIR} is not writable; trying sudo ..."
  sudo install -m 0755 "$TMP" "$DEST"
fi
ok "installed to ${DEST}"

# ─── verify + PATH hint ────────────────────────────────────────────────────
echo
if command -v gitea2forgejo >/dev/null 2>&1 && [ "$(command -v gitea2forgejo)" = "$DEST" ]; then
  ok "$("$DEST" --version)"
  echo
  printf "Next: ${C_BLD}gitea2forgejo init${C_OFF}\n"
  printf "Docs: ${C_BLU}https://github.com/${REPO}${C_OFF}\n"
elif command -v gitea2forgejo >/dev/null 2>&1; then
  warn "another gitea2forgejo is ahead of ${INSTALL_DIR} in your PATH:"
  warn "  $(command -v gitea2forgejo)"
  printf "Check with:  ${C_BLU}which -a gitea2forgejo${C_OFF}\n"
else
  warn "${INSTALL_DIR} is not in your PATH"
  printf "Add it to \$PATH or call the binary directly:\n"
  printf "  ${C_BLU}${DEST} --version${C_OFF}\n"
fi

} # end _main

# Belt-and-braces: redirect stdin away from this shell so any helper
# subprocess that accidentally reads it can't steal our bytes.
_main "$@" < /dev/null
