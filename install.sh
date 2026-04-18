#!/usr/bin/env bash
# gitea2forgejo installer (Linux / macOS)
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/pacnpal/gitea2forgejo/main/install.sh | bash
#
# Environment variable overrides:
#   INSTALL_DIR  where to place the binary (default: /usr/local/bin)
#   VERSION      version tag to install (default: latest release)
#
# The script detects OS + CPU, downloads the matching release binary,
# installs it to INSTALL_DIR (prompting for sudo if needed), and on
# macOS also strips com.apple.quarantine + applies an ad-hoc codesign
# so Gatekeeper doesn't refuse the first invocation.

set -euo pipefail

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
