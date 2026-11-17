#!/usr/bin/env bash
# Run as root on the production Linux server; no build tools are used here.
set -euo pipefail
umask 027

revision="${1:?Usage: deploy-release.sh COMMIT_SHA ARCHIVE}"
archive="$(realpath "${2:?Missing release archive}")"
[[ "$revision" =~ ^[a-f0-9]{40}$ ]] || { echo "Invalid revision" >&2; exit 1; }
[[ "$EUID" -eq 0 && "$(uname -m)" == x86_64 ]] || { echo "Requires root on Linux x64" >&2; exit 1; }
env_file=/etc/ip-list.env
release_root=/opt/ip-list-releases
current=/opt/ip-list-current
previous_link=/opt/ip-list-previous
release_dir="$release_root/$revision"
backup_root=/opt/ip-list-backups/cicd
cleanup_script="$(dirname "$(realpath "$0")")/cleanup-deployments.py"
exec 9>/run/lock/ip-list-deploy.lock
flock -n 9 || { echo "Another deployment is running" >&2; exit 1; }
test -r "$env_file"
id ipguard >/dev/null
for tool in curl systemd-run pg_dump pg_restore runuser python3; do command -v "$tool" >/dev/null; done
(cd "$(dirname "$archive")" && sha256sum --check "$(basename "$archive").sha256")
python3 "$cleanup_script"

if [[ -L "$current" && "$(readlink -f "$current")" == "$release_dir" ]]; then
  systemctl is-active --quiet ip-list-api ip-list-web
  curl -fsS --max-time 5 http://127.0.0.1:8081/health
  curl -fsS --max-time 5 http://127.0.0.1:3000/login >/dev/null
  echo "Release $revision is already deployed"
  exit 0
fi

install -d -m 755 "$release_root"
install -d -m 700 "$backup_root"
staging=$(mktemp -d "$release_root/.staging.XXXXXX")
snapshot=$(mktemp -d "$backup_root/deploy-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")
previous=$(readlink -f "$current" 2>/dev/null || true)
switched=0
stopped=0
completed=0
for service in api web; do
  config="/etc/systemd/system/ip-list-$service.service.d/release.conf"
  if [[ -f "$config" ]]; then cp "$config" "$snapshot/$service.conf"; fi
done

cleanup() {
  status=$?
  trap - EXIT
  if [[ "$completed" -eq 0 && "$stopped" -eq 1 ]]; then
    echo "Deployment failed; restoring the previous application version" >&2
    if [[ "$switched" -eq 1 ]]; then
      if [[ -n "$previous" ]]; then
        ln -s "$previous" "$current.rollback.$$"
        mv -Tf "$current.rollback.$$" "$current"
      else
        rm -f "$current"
      fi
    fi
    for service in api web; do
      config="/etc/systemd/system/ip-list-$service.service.d/release.conf"
      if [[ -f "$snapshot/$service.conf" ]]; then
        cp "$snapshot/$service.conf" "$config"
      else
        rm -f "$config"
      fi
    done
    systemctl daemon-reload
    systemctl restart ip-list-api ip-list-web || true
    if [[ -f "$snapshot/database.dump" ]]; then
      echo "Database backup: $snapshot/database.dump (database is not automatically restored)" >&2
    fi
  fi
  rm -rf "$staging"
  python3 "$cleanup_script" || echo "Deployment cleanup failed; inspect server storage" >&2
  exit "$status"
}
trap cleanup EXIT

tar -xzf "$archive" -C "$staging" --no-same-owner
test "$(cat "$staging/REVISION")" = "$revision"
test -x "$staging/bin/ipguard"
test -x "$staging/runtime/node"
test -f "$staging/server.js"
test -f "$staging/bin/latency-probe/latency-probe-linux-amd64"
"$staging/runtime/node" --version
fingerprint=$(cat "$staging/DATABASE_FINGERPRINT")
[[ "$fingerprint" =~ ^[a-f0-9]{64}$ ]] || { echo "Invalid database fingerprint" >&2; exit 1; }
needs_migration=1
if [[ -f "$current/DATABASE_FINGERPRINT" && "$(cat "$current/DATABASE_FINGERPRINT")" == "$fingerprint" ]]; then
  needs_migration=0
fi

if [[ "$needs_migration" -eq 1 ]]; then
  database_name=$(systemd-run --quiet --wait --pipe --collect \
    --property="EnvironmentFile=$env_file" \
    /usr/bin/python3 -c 'import os, urllib.parse; print(urllib.parse.unquote(urllib.parse.urlsplit(os.environ["DATABASE_URL"]).path[1:]))')
  [[ -n "$database_name" ]] || { echo "Missing database name" >&2; exit 1; }
  runuser -u postgres -- pg_dump --format=custom --dbname="$database_name" > "$snapshot/database.dump.partial"
  pg_restore --list "$snapshot/database.dump.partial" >/dev/null
  mv "$snapshot/database.dump.partial" "$snapshot/database.dump"
  chmod 600 "$snapshot/database.dump"
else
  echo "Database initialization code is unchanged; skipping backup and migration"
fi

if [[ -e "$release_dir" ]]; then
  echo "Removing an inactive release from a previous attempt: $revision"
  rm -rf "$release_dir"
fi
mv "$staging" "$release_dir"
chown -R ipguard:ipguard "$release_dir"
# Stop both processes so the old API cannot write during schema migration.
stopped=1
systemctl stop ip-list-web ip-list-api
if [[ "$needs_migration" -eq 1 ]]; then
  systemd-run --quiet --wait --pipe --collect \
  --property="EnvironmentFile=$env_file" \
  --property=User=ipguard --property=Group=ipguard \
  --property="WorkingDirectory=$release_dir" \
  "$release_dir/bin/ipguard" -init
fi

for service in api web; do
  install -d -m 755 "/etc/systemd/system/ip-list-$service.service.d"
done
cat > /etc/systemd/system/ip-list-api.service.d/release.conf <<'EOF'
[Service]
WorkingDirectory=/opt/ip-list-current
ExecStart=
ExecStart=/opt/ip-list-current/bin/ipguard
EOF
cat > /etc/systemd/system/ip-list-web.service.d/release.conf <<'EOF'
[Service]
WorkingDirectory=/opt/ip-list-current
Environment=NODE_ENV=production
Environment=HOSTNAME=127.0.0.1
Environment=PORT=3000
ExecStart=
ExecStart=/opt/ip-list-current/runtime/node /opt/ip-list-current/server.js
EOF
ln -s "$release_dir" "$current.next.$$"
mv -Tf "$current.next.$$" "$current"
switched=1
systemctl daemon-reload
systemctl restart ip-list-api ip-list-web

healthy=0
for ((attempt=0; attempt<30; attempt++)); do
  if systemctl is-active --quiet ip-list-api ip-list-web \
    && curl -fsS --max-time 3 http://127.0.0.1:8081/health >/dev/null \
    && curl -fsS --max-time 3 http://127.0.0.1:3000/login >/dev/null; then
    healthy=1
    break
  fi
  sleep 2
done
[[ "$healthy" -eq 1 ]] || { echo "Application health checks failed" >&2; exit 1; }
if [[ -n "$previous" && "$previous" != "$release_dir" ]]; then
  ln -s "$previous" "$previous_link.next.$$"
  mv -Tf "$previous_link.next.$$" "$previous_link"
fi
completed=1
echo "Successfully deployed $revision"
if [[ -f "$snapshot/database.dump" ]]; then echo "Database backup: $snapshot/database.dump"; fi
