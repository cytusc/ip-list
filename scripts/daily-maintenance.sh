#!/usr/bin/env bash
# Run as postgres; the systemd timer invokes this once a day.
set -euo pipefail
umask 077
export PGDATABASE="${PGDATABASE:-ipguard}"
export PGCONNECT_TIMEOUT=10
export PGOPTIONS='-c lock_timeout=2s -c statement_timeout=120s'
psql_cmd=(psql -X -v ON_ERROR_STOP=1)

"${psql_cmd[@]}" <<'SQL'
BEGIN;
DELETE FROM node_scan_rounds WHERE started_at < now()-interval '24 hours'
 AND (status NOT IN ('running','waiting') OR updated_at < now()-interval '2 minutes');
DELETE FROM dns_sync_runs WHERE id NOT IN (SELECT id FROM dns_sync_runs ORDER BY id DESC LIMIT 20);
-- Recent samples remain available to DNS decisions and history compaction.
DELETE FROM latency_probe_results WHERE checked_at < date_bin('15 minutes',now()-interval '24 hours','2000-01-01'::timestamptz);
DELETE FROM latency_probe_rollups WHERE bucket_start < date_bin('15 minutes',now()-interval '24 hours','2000-01-01'::timestamptz);
COMMIT;
VACUUM (ANALYZE) node_scan_rounds, dns_sync_runs, latency_probe_results, latency_probe_rollups,
 node_ip_pool, ip_results, cf_ip_pool, cf_results;
CREATE EXTENSION IF NOT EXISTS pgstattuple;
SQL

# Rewrite only large, mostly empty tables; normal daily maintenance stays online.
tables=$("${psql_cmd[@]}" -qAt <<'SQL'
SELECT c.relname FROM pg_class c
CROSS JOIN LATERAL pgstattuple_approx(c.oid::regclass) s
WHERE c.oid IN ('node_ip_pool'::regclass,'ip_results'::regclass,'cf_ip_pool'::regclass,'cf_results'::regclass)
 AND pg_relation_size(c.oid)>=64*1024*1024 AND s.approx_free_percent>=50
ORDER BY c.relname;
SQL
)
if [[ -n "$tables" ]]; then
  backup_dir=${MAINTENANCE_BACKUP_DIR:-/var/lib/postgresql/ip-list-maintenance}
  mkdir -p "$backup_dir"
  backup="$backup_dir/maintenance-$(date -u +%Y%m%dT%H%M%SZ).dump"
  pg_dump -Fc > "$backup.partial"
  pg_restore --list "$backup.partial" >/dev/null
  mv "$backup.partial" "$backup"
  echo "Database backup: $backup"
  while IFS= read -r table; do
    case "$table" in
      node_ip_pool|ip_results|cf_ip_pool|cf_results)
        echo "Reclaiming disk space: $table"
        "${psql_cmd[@]}" -c "VACUUM (FULL, ANALYZE) $table;" ;;
      *) echo "Unexpected table: $table" >&2; exit 1 ;;
    esac
  done <<< "$tables"
  shopt -s nullglob
  backups=("$backup_dir"/maintenance-*.dump)
  for ((i=0; i<${#backups[@]}-3; i++)); do rm -- "${backups[i]}"; done
fi
"${psql_cmd[@]}" -c 'SELECT pg_size_pretty(pg_database_size(current_database())) AS database_size;'
echo 'Daily database maintenance completed'
