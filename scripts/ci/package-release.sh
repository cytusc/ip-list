#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."

revision="${RELEASE_SHA:-$(git rev-parse HEAD)}"
[[ "$revision" =~ ^[a-f0-9]{40}$ ]] || { echo "Invalid release revision" >&2; exit 1; }
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
mkdir -p dist "$stage/bin" "$stage/scripts"

# The UI is a static export served by the Rust gateway; no Node runtime ships.
test -f out/index.html || { echo "Missing out/: run npm run build first" >&2; exit 1; }
test -x bin/ipguard-worker || { echo "Missing bin/ipguard-worker" >&2; exit 1; }
test -x bin/control || { echo "Missing bin/control (Linux build of backend/rust/control)" >&2; exit 1; }
cp -a out/. "$stage/"
cp bin/ipguard-worker bin/control "$stage/bin/"
cp -a bin/latency-probe "$stage/bin/"
cp -a scripts/latency-probe "$stage/scripts/"
cp scripts/deploy-release.sh "$stage/scripts/"
cp scripts/cleanup-deployments.py "$stage/scripts/"
cp scripts/daily-scan.py "$stage/scripts/"
cp scripts/db-diagnostics.py "$stage/scripts/"
mkdir -p "$stage/scripts/systemd"
cp scripts/systemd/ip-list-db-diagnostics.service "$stage/scripts/systemd/"
cp scripts/systemd/ip-list-worker.service "$stage/scripts/systemd/"
cp scripts/systemd/ip-list-daily-scan.service "$stage/scripts/systemd/"
cp scripts/systemd/ip-list-daily-scan.timer "$stage/scripts/systemd/"
cp scripts/install-nightly.py "$stage/scripts/"
printf '%s\n' "$revision" > "$stage/REVISION"
# Include initialization code as well as SQL so changes to either trigger migration.
sha256sum backend/internal/ipguard/schema.sql backend/internal/ipguard/web.go backend/internal/ipguard/bootstrap.go | sha256sum | cut -d ' ' -f 1 > "$stage/DATABASE_FINGERPRINT"
# Runtime credentials belong only in the server EnvironmentFile.
find "$stage" -name '.env*' -type f -delete
tar -czf "dist/ip-list-$revision.tar.gz" -C "$stage" .
(cd dist && sha256sum "ip-list-$revision.tar.gz" > "ip-list-$revision.tar.gz.sha256")
