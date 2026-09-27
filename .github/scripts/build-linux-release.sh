#!/usr/bin/env bash
set -euo pipefail

[[ $# == 2 ]] || { echo 'Usage: build-linux-release.sh vVERSION OUTPUT_DIRECTORY' >&2; exit 1; }
tag=$1
[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo 'Invalid release tag' >&2; exit 1; }
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
mkdir -p "$2"
output="$(cd "$2" && pwd)"
cd "$root"
gui_version="$(node -p 'require("./desktop/package.json").version')"
[[ ${tag#v} == "$gui_version" ]] || { echo 'Tag does not match desktop version' >&2; exit 1; }
core_version="$(tr -d '\r\n' < CLI_VERSION)"
[[ $core_version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo 'Invalid CLI_VERSION' >&2; exit 1; }

for arch in amd64 arm64; do
  stage="$(mktemp -d "$output/stage-$arch.XXXXXX")"
  for program in tunnel-server tunnelx-cli; do
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
      -ldflags "-s -w -X main.Version=$core_version" -o "$stage/$program" "./cmd/$program"
  done
  mkdir -p "$stage/deploy"
  cp README.md README.en.md ADMIN_LOGIN_GUIDE.md CLIENT_ACCESS_GUIDE.md "$stage/"
  cp deploy/README.md deploy/README.en.md "$stage/deploy/"
  # Normalize scripts when building from a Windows checkout.
  sed 's/\r$//' deploy/install.sh > "$stage/install.sh"
  sed 's/\r$//' deploy/upgrade.sh > "$stage/upgrade.sh"
  chmod 755 "$stage/tunnel-server" "$stage/tunnelx-cli" "$stage/install.sh" "$stage/upgrade.sh"
  printf 'Release: %s\nCLI/server: %s\nPlatform: linux/%s\n' "$tag" "$core_version" "$arch" > "$stage/VERSION.txt"
  tar -czf "$output/TunnelX-${tag#v}-linux-$arch.tar.gz" -C "$stage" \
    tunnel-server tunnelx-cli install.sh upgrade.sh VERSION.txt \
    README.md README.en.md ADMIN_LOGIN_GUIDE.md CLIENT_ACCESS_GUIDE.md deploy
done

cd "$output"
sha256sum "TunnelX-${tag#v}-linux-amd64.tar.gz" "TunnelX-${tag#v}-linux-arm64.tar.gz" > SHA256SUMS-linux.txt
sha256sum --check SHA256SUMS-linux.txt
