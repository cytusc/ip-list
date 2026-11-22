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
grep -q '^CONTROL_INTERNAL_TOKEN=' "$env_file" || { echo "Missing CONTROL_INTERNAL_TOKEN in $env_file" >&2; exit 1; }
id ipguard >/dev/null
for tool in curl systemd-run pg_dump pg_restore runuser python3; do command -v "$tool" >/dev/null; done
(cd "$(dirname "$archive")" && sha256sum --check "$(basename "$archive").sha256")
python3 "$cleanup_script"

if [[ -L "$current" && "$(readlink -f "$current")" == "$release_dir" ]]; then
  systemctl is-active --quiet ip-list-worker ip-list-web
  curl -fsS --max-time 5 http://127.0.0.1:8082/health
  curl -fsS --max-time 5 http://127.0.0.1:3000/login/ >/dev/null
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
for service in api worker web; do
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
    for service in api worker web; do
      config="/etc/systemd/system/ip-list-$service.service.d/release.conf"
      if [[ -f "$snapshot/$service.conf" ]]; then
        cp "$snapshot/$service.conf" "$config"
      else
        rm -f "$config"
      fi
    done
    systemctl daemon-reload
    systemctl restart ip-list-worker ip-list-web || true
    if [[ -n "$previous" && -x "$previous/bin/ipguard" ]]; then
      systemctl start ip-list-api || true
    fi
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
test -x "$staging/bin/ipguard-worker"
test -x "$staging/bin/control"
test -f "$staging/index.html"
test -f "$staging/bin/latency-probe/latency-probe-linux-amd64"
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
# Stop every process so the old control plane and worker cannot write during schema migration.
stopped=1
systemctl stop ip-list-web ip-list-worker ip-list-api 2>/dev/null || true
systemctl disable ip-list-api >/dev/null 2>&1 || true
if [[ "$needs_migration" -eq 1 ]]; then
  systemd-run --quiet --wait --pipe --collect \
  --property="EnvironmentFile=$env_file" \
  --property=User=ipguard --property=Group=ipguard \
  --property="WorkingDirectory=$release_dir" \
  "$release_dir/bin/ipguard-worker" -init
fi

for service in worker web; do
  install -d -m 755 "/etc/systemd/system/ip-list-$service.service.d"
done
cat > /etc/systemd/system/ip-list-worker.service.d/release.conf <<'EOF'
[Service]
WorkingDirectory=/opt/ip-list-current
Environment=INTERNAL_ADDR=127.0.0.1:8082
ExecStart=
ExecStart=/opt/ip-list-current/bin/ipguard-worker
EOF
cat > /etc/systemd/system/ip-list-web.service.d/release.conf <<'EOF'
[Service]
WorkingDirectory=/opt/ip-list-current
Environment=CONTROL_ADDR=127.0.0.1:3000
Environment=WORKER_URL=http://127.0.0.1:8082
Environment=WEB_DIR=/opt/ip-list-current
Environment=PROBE_BIN_DIR=/opt/ip-list-current/bin/latency-probe
Environment=PROBE_SCRIPT_DIR=/opt/ip-list-current/scripts/latency-probe
ExecStart=
ExecStart=/opt/ip-list-current/bin/control
EOF
ln -s "$release_dir" "$current.next.$$"
mv -Tf "$current.next.$$" "$current"
switched=1
if [[ -f "$release_dir/scripts/systemd/ip-list-worker.service" ]]; then
  install -m 644 "$release_dir/scripts/systemd/ip-list-worker.service" /etc/systemd/system/ip-list-worker.service
  systemctl enable ip-list-worker.service >/dev/null
fi
systemctl daemon-reload
systemctl restart ip-list-worker ip-list-web

healthy=0
for ((attempt=0; attempt<30; attempt++)); do
  if systemctl is-active --quiet ip-list-worker ip-list-web \
    && curl -fsS --max-time 3 http://127.0.0.1:8082/health >/dev/null \
    && curl -fsS --max-time 3 http://127.0.0.1:3000/health >/dev/null \
    && curl -fsS --max-time 3 http://127.0.0.1:3000/login/ >/dev/null; then
    healthy=1
    break
  fi
  sleep 2
done
[[ "$healthy" -eq 1 ]] || { echo "Application health checks failed" >&2; exit 1; }
if python3 "$release_dir/scripts/install-nightly.py" "$release_dir/scripts/db-diagnostics.py" /usr/local/sbin/ip-list-db-diagnostics \
  && install -m 644 "$release_dir/scripts/systemd/ip-list-db-diagnostics.service" /etc/systemd/system/ip-list-db-diagnostics.service \
  && systemctl daemon-reload \
  && systemctl enable ip-list-db-diagnostics.service \
  && systemctl restart ip-list-db-diagnostics.service; then
  echo "Database diagnostics started"
else
  echo "Database diagnostics installation failed; inspect ip-list-db-diagnostics.service" >&2
fi
# A bad script must leave the previous timer executable intact.
if python3 "$release_dir/scripts/install-nightly.py" "$release_dir/scripts/daily-scan.py" /usr/local/sbin/ip-list-daily-scan; then
  install -m 644 "$release_dir/scripts/systemd/ip-list-daily-scan.service" /etc/systemd/system/ip-list-daily-scan.service
  install -m 644 "$release_dir/scripts/systemd/ip-list-daily-scan.timer" /etc/systemd/system/ip-list-daily-scan.timer
  systemctl daemon-reload
  systemctl enable --now ip-list-daily-scan.timer
  echo "Nightly scan script installed"
else
  echo "Nightly scan script installation failed; inspect the timer before the next run" >&2
fi
if [[ -n "$previous" && "$previous" != "$release_dir" ]]; then
  ln -s "$previous" "$previous_link.next.$$"
  mv -Tf "$previous_link.next.$$" "$previous_link"
fi
completed=1
echo "Successfully deployed $revision"
if [[ -f "$snapshot/database.dump" ]]; then echo "Database backup: $snapshot/database.dump"; fi
