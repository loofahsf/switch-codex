#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

mapfile -t packages < <(find release -maxdepth 1 -name 'Switch-Codex-*-linux-amd64.deb' -print)
if [[ ${#packages[@]} -ne 1 ]]; then
  echo "Expected exactly one Linux amd64 package, found ${#packages[@]}" >&2
  exit 1
fi
package="${packages[0]}"
version="$(node -p "require('./package.json').version")"

test "$(dpkg-deb -f "$package" Package)" = "switch-codex"
test "$(dpkg-deb -f "$package" Architecture)" = "amd64"
test "$(dpkg-deb -f "$package" Version)" = "$version-1"
depends="$(dpkg-deb -f "$package" Depends)"
[[ "$depends" == *"libgtk-4-1"* ]]
[[ "$depends" == *"libwebkitgtk-6.0-4"* ]]
desktop-file-validate build/linux/switch-codex.desktop

contents_file="$(mktemp)"
smoke_data="$(mktemp -d)"
trap 'rm -f "$contents_file"; rm -rf "$smoke_data"' EXIT
dpkg-deb -c "$package" > "$contents_file"
grep -q './usr/bin/switch-codex' "$contents_file"
grep -q './usr/share/applications/switch-codex.desktop' "$contents_file"
grep -q './usr/share/icons/hicolor/256x256/apps/switch-codex.png' "$contents_file"

sudo apt-get install --no-install-recommends -y "$repo_root/$package"
set +e
# GitHub-hosted runners prohibit the user namespace mapping required by
# WebKitGTK's Bubblewrap sandbox. Disable it only for this isolated smoke test;
# the installed desktop application retains WebKitGTK's default sandbox.
CODEX_SWITCH_DATA_DIR="$smoke_data" WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS=1 \
  timeout 15s dbus-run-session -- xvfb-run -a /usr/bin/switch-codex \
  > /tmp/switch-codex-ubuntu-smoke.log 2>&1
status=$?
set -e
if [[ $status -ne 124 ]]; then
  cat /tmp/switch-codex-ubuntu-smoke.log
  exit 1
fi

docker run --rm \
  -e PACKAGE_PATH="/workspace/$package" \
  -v "$repo_root:/workspace:ro" \
  debian:13-slim bash -lc '
    set -euo pipefail
    apt-get update
    apt-get install --no-install-recommends -y "$PACKAGE_PATH" xvfb xauth dbus-x11
    smoke_data=$(mktemp -d)
    set +e
    CODEX_SWITCH_DATA_DIR="$smoke_data" WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS=1 \
      timeout 15s dbus-run-session -- xvfb-run -a /usr/bin/switch-codex \
      > /tmp/switch-codex-debian-smoke.log 2>&1
    status=$?
    set -e
    if [[ $status -ne 124 ]]; then
      cat /tmp/switch-codex-debian-smoke.log
      exit 1
    fi
  '
