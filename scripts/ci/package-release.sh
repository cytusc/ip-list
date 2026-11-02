#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."

revision="${RELEASE_SHA:-$(git rev-parse HEAD)}"
[[ "$revision" =~ ^[a-f0-9]{40}$ ]] || { echo "Invalid release revision" >&2; exit 1; }
[[ "$(node -p 'process.platform + "/" + process.arch')" == linux/x64 ]] || {
  echo "Production releases must be packaged on Linux x64" >&2; exit 1;
}
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
mkdir -p dist "$stage/.next" "$stage/bin" "$stage/runtime" "$stage/scripts"
cp -a .next/standalone/. "$stage/"
cp -a .next/static "$stage/.next/"
if [[ -d public ]]; then cp -a public "$stage/"; fi
cp bin/ipguard "$stage/bin/"
cp -a bin/latency-probe "$stage/bin/"
cp -a scripts/latency-probe "$stage/scripts/"
cp scripts/deploy-release.sh "$stage/scripts/"
cp "$(command -v node)" "$stage/runtime/node"
printf '%s\n' "$revision" > "$stage/REVISION"
# Runtime credentials belong only in the server EnvironmentFile.
find "$stage" -name '.env*' -type f -delete
tar -czf "dist/ip-list-$revision.tar.gz" -C "$stage" .
(cd dist && sha256sum "ip-list-$revision.tar.gz" > "ip-list-$revision.tar.gz.sha256")
