CREATE TABLE IF NOT EXISTS users (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  username varchar(64) NOT NULL UNIQUE,
  password_hash text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS nodes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name varchar(80) NOT NULL UNIQUE,
  address varchar(253) NOT NULL,
  port integer NOT NULL DEFAULT 8080 CHECK (port BETWEEN 1 AND 65535),
  enabled boolean NOT NULL DEFAULT true,
  description varchar(500) NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (address, port)
);

CREATE TABLE IF NOT EXISTS networks (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name varchar(80) NOT NULL,
  cidr cidr NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  description varchar(500) NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (cidr)
);

-- Individual addresses excluded from all proxy and CF scans.
CREATE TABLE IF NOT EXISTS ip_blacklist (
  ip inet PRIMARY KEY,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Existing installations: run -init again; existing data is preserved.
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS share_uri text NOT NULL DEFAULT '';
ALTER TABLE nodes DROP CONSTRAINT IF EXISTS nodes_address_port_key;
CREATE UNIQUE INDEX IF NOT EXISTS nodes_manual_address_port_key
  ON nodes(address,port) WHERE share_uri = '';

-- Keep only the latest check for each node/IP, rather than a scan history.
CREATE TABLE IF NOT EXISTS ip_results (
  node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  ip inet NOT NULL,
  config_hash varchar(32) NOT NULL,
  protocol varchar(16) NOT NULL,
  available boolean NOT NULL,
  delay_ms integer NOT NULL,
  checked_at timestamptz NOT NULL,
  PRIMARY KEY (node_id, ip)
);

ALTER TABLE ip_results ALTER COLUMN protocol TYPE varchar(32);

-- A pool may be shared only while an enabled node still has its tested configuration.
CREATE TABLE IF NOT EXISTS node_ip_pool (
 protocol varchar(32) NOT NULL,
 ip inet NOT NULL,
 available boolean NOT NULL,
 delay_ms integer NOT NULL,
 checked_at timestamptz NOT NULL,
 PRIMARY KEY(protocol,ip)
);

ALTER TABLE node_ip_pool ADD COLUMN IF NOT EXISTS config_hash varchar(32) NOT NULL DEFAULT '';
ALTER TABLE node_ip_pool ADD COLUMN IF NOT EXISTS certificate jsonb NOT NULL DEFAULT '{}'::jsonb;
CREATE INDEX IF NOT EXISTS node_ip_pool_available_idx ON node_ip_pool(ip) WHERE available;
CREATE INDEX IF NOT EXISTS node_ip_pool_certificate_invalid_idx ON node_ip_pool(ip,(certificate->>'server_name')) WHERE certificate->>'status'='invalid';
CREATE INDEX IF NOT EXISTS nodes_enabled_config_hash_idx ON nodes((md5(share_uri))) WHERE enabled;
CREATE OR REPLACE VIEW current_node_ip_pool AS
 SELECT p.* FROM node_ip_pool p WHERE (NOT p.available OR p.certificate->>'status' IN ('passed','not_required')) AND NOT EXISTS (
  SELECT 1 FROM node_ip_pool bad WHERE bad.ip=p.ip AND bad.certificate->>'status'='invalid'
  AND bad.certificate->>'server_name'=p.certificate->>'server_name'
  AND EXISTS(SELECT 1 FROM nodes n WHERE n.enabled AND n.share_uri<>'' AND md5(n.share_uri)=bad.config_hash)
 ) AND EXISTS (
  SELECT 1 FROM nodes n WHERE n.enabled AND n.share_uri<>'' AND md5(n.share_uri)=p.config_hash
 );

-- Named client probes report direct ICMP latency for currently available IPs.
CREATE TABLE IF NOT EXISTS latency_probes (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 name varchar(80) NOT NULL UNIQUE,
 token_hash bytea NOT NULL UNIQUE,
 interval_seconds integer NOT NULL DEFAULT 20 CHECK (interval_seconds BETWEEN 15 AND 86400),
 timeout_ms integer NOT NULL DEFAULT 1000 CHECK (timeout_ms BETWEEN 250 AND 10000),
 concurrency integer NOT NULL DEFAULT 64 CHECK (concurrency BETWEEN 1 AND 512),
 interface_name varchar(100) NOT NULL DEFAULT '',
 enabled boolean NOT NULL DEFAULT true,
 last_seen timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE latency_probes ADD COLUMN IF NOT EXISTS heartbeat_at timestamptz;
ALTER TABLE latency_probes ADD COLUMN IF NOT EXISTS token_encrypted bytea;
ALTER TABLE latency_probes ADD COLUMN IF NOT EXISTS deployment_platform text NOT NULL DEFAULT 'darwin';
CREATE TABLE IF NOT EXISTS latency_probe_results (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 probe_id uuid NOT NULL REFERENCES latency_probes(id) ON DELETE CASCADE,
 ip inet NOT NULL,
 delay_ms double precision NOT NULL DEFAULT 0 CHECK (delay_ms BETWEEN 0 AND 60000),
 success boolean NOT NULL,
 checked_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS latency_probe_results_probe_ip_time_idx
 ON latency_probe_results(probe_id,ip,checked_at DESC);
CREATE INDEX IF NOT EXISTS latency_probe_results_time_idx
 ON latency_probe_results(checked_at);
INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at,config_hash)
 SELECT DISTINCT ON (protocol,ip) protocol,ip,available,delay_ms,checked_at,config_hash FROM (
 SELECT CASE WHEN protocol LIKE '%/%' THEN protocol ELSE 'vless/'||protocol END AS protocol,
 ip,available,delay_ms,checked_at,node_id,config_hash FROM ip_results
 ) legacy ORDER BY protocol,ip,checked_at DESC,node_id
 ON CONFLICT(protocol,ip) DO UPDATE SET available=excluded.available,
 delay_ms=excluded.delay_ms,checked_at=excluded.checked_at,config_hash=excluded.config_hash
 WHERE node_ip_pool.checked_at<excluded.checked_at
 OR (node_ip_pool.checked_at=excluded.checked_at AND node_ip_pool.config_hash='');

-- One DNS policy; ownership and history survive restarts and domain changes.
CREATE TABLE IF NOT EXISTS dns_settings (
 id boolean PRIMARY KEY DEFAULT true CHECK(id),
 config jsonb NOT NULL DEFAULT '{"domain":"reiiv.com","enabled":false,"interval_seconds":300,"ttl":300,"max_ips":10,"window_minutes":5,"min_success_percent":0,"probe_id":"","protocol":""}',
 requested text NOT NULL DEFAULT '',
 running boolean NOT NULL DEFAULT false,
 next_run_at timestamptz NOT NULL DEFAULT now(),
 report jsonb NOT NULL DEFAULT '{}'
);
INSERT INTO dns_settings(id) VALUES(true) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS dns_managed_records (
  zone_id text NOT NULL,
 domain text NOT NULL,
 name text NOT NULL,
 type text NOT NULL,
 PRIMARY KEY(domain,zone_id,name,type)
);
CREATE TABLE IF NOT EXISTS dns_sync_runs (
 id bigserial PRIMARY KEY,
 started_at timestamptz NOT NULL DEFAULT now(),
 report jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS dns_ip_locations (
 ip inet PRIMARY KEY,
 country varchar(2) NOT NULL,
 checked_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS cf_targets (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 name varchar(100) NOT NULL,
 url text NOT NULL UNIQUE,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS cf_results (
 target_id uuid NOT NULL REFERENCES cf_targets(id) ON DELETE CASCADE,
 ip inet NOT NULL,
 available boolean NOT NULL,
 delay_ms integer NOT NULL,
 http_status integer NOT NULL,
 checked_at timestamptz NOT NULL,
 PRIMARY KEY(target_id,ip)
);

-- CF domains are probe samples; their results feed one independent IP pool.
-- Keep legacy per-domain rows so upgrading preserves the original data.
CREATE TABLE IF NOT EXISTS cf_ip_pool (
 ip inet PRIMARY KEY,
 available boolean NOT NULL,
 delay_ms integer NOT NULL,
 http_status integer NOT NULL,
 checked_at timestamptz NOT NULL
);

-- Per-domain view of one unified CF scan: which IPs answered this domain, and
-- how each round went, so the domain page can report its own availability.
ALTER TABLE cf_targets ADD COLUMN IF NOT EXISTS last_checked_at timestamptz;
CREATE TABLE IF NOT EXISTS cf_target_rounds (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 target_id uuid NOT NULL REFERENCES cf_targets(id) ON DELETE CASCADE,
 started_at timestamptz NOT NULL,
 finished_at timestamptz NOT NULL,
 total integer NOT NULL,
 success integer NOT NULL,
 status varchar(16) NOT NULL
);
CREATE INDEX IF NOT EXISTS cf_target_rounds_target_idx ON cf_target_rounds(target_id,finished_at DESC);
-- Domains scanned before the round table keep a usable "last checked" time.
UPDATE cf_targets t SET last_checked_at=s.last_checked
FROM (SELECT target_id,max(checked_at) AS last_checked FROM cf_results GROUP BY target_id) s
WHERE s.target_id=t.id AND (t.last_checked_at IS NULL OR t.last_checked_at<s.last_checked);
INSERT INTO cf_ip_pool(ip,available,delay_ms,http_status,checked_at)
 SELECT DISTINCT ON (ip) ip,available,delay_ms,http_status,checked_at
 FROM cf_results ORDER BY ip,checked_at DESC,target_id
 ON CONFLICT(ip) DO UPDATE SET available=excluded.available,
 delay_ms=excluded.delay_ms,http_status=excluded.http_status,checked_at=excluded.checked_at
 WHERE cf_ip_pool.checked_at<excluded.checked_at;

ALTER TABLE networks ADD COLUMN IF NOT EXISTS asn varchar(12) NOT NULL DEFAULT '';
ALTER TABLE networks ALTER COLUMN asn TYPE varchar(255);
ALTER TABLE networks ADD COLUMN IF NOT EXISTS provider varchar(100) NOT NULL DEFAULT '';

-- Migrate node-bound ranges once. Keep the oldest ID so existing selections
-- remain valid where possible, merge notes, and retain any enabled copy.
DO $$
BEGIN
 IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='networks' AND column_name='node_id') THEN
  ALTER TABLE networks ALTER COLUMN description TYPE text;
  WITH merged AS (
   SELECT cidr,(array_agg(id ORDER BY created_at,id))[1] AS keep_id,
    bool_or(enabled) AS enabled,
    COALESCE(string_agg(DISTINCT NULLIF(description,''), E'\n'),'') AS description,
    COALESCE((array_agg(asn ORDER BY updated_at DESC,id) FILTER (WHERE asn<>''))[1],'') AS asn,
    COALESCE((array_agg(provider ORDER BY updated_at DESC,id) FILTER (WHERE provider<>''))[1],'') AS provider
   FROM networks GROUP BY cidr HAVING count(*)>1
  )
  UPDATE networks w SET enabled=m.enabled,description=m.description,asn=m.asn,provider=m.provider
  FROM merged m WHERE w.id=m.keep_id;
  DELETE FROM networks w USING (
   SELECT id,row_number() OVER(PARTITION BY cidr ORDER BY created_at,id) AS position FROM networks
  ) duplicates WHERE w.id=duplicates.id AND duplicates.position>1;
  ALTER TABLE networks DROP COLUMN node_id;
  ALTER TABLE networks DROP COLUMN action;
 END IF;
END $$;
CREATE UNIQUE INDEX IF NOT EXISTS networks_cidr_key ON networks(cidr);

CREATE TABLE IF NOT EXISTS protocol_networks (
 protocol varchar(32) NOT NULL,
 network_id uuid NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
 cidr cidr NOT NULL,
 checked_at timestamptz NOT NULL,
 PRIMARY KEY(protocol,network_id)
);
CREATE TABLE IF NOT EXISTS node_scan_schedules (
 protocol varchar(32) PRIMARY KEY,
 job_id text NOT NULL,
 job jsonb NOT NULL,
 enabled boolean NOT NULL DEFAULT true,
 updated_at timestamptz NOT NULL DEFAULT now()
);

-- Connectivity rounds are listed as records: one row per round, written when the
-- round starts and completed when it ends. Per-IP results are attached only for
-- the latest rounds, so the list stays cheap while a record can be reopened.
CREATE TABLE IF NOT EXISTS node_scan_rounds (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 job_id text NOT NULL,
 round integer NOT NULL,
 protocol varchar(32) NOT NULL,
 node_name varchar(80) NOT NULL DEFAULT '',
 scope varchar(16) NOT NULL DEFAULT 'full',
 url text NOT NULL DEFAULT '',
 cidrs text[] NOT NULL DEFAULT '{}',
 input jsonb NOT NULL DEFAULT '{}'::jsonb,
 started_at timestamptz NOT NULL,
 finished_at timestamptz,
 total integer NOT NULL DEFAULT 0,
 completed integer NOT NULL DEFAULT 0,
 success integer NOT NULL DEFAULT 0,
 status varchar(16) NOT NULL DEFAULT 'running',
 error text NOT NULL DEFAULT '',
 results jsonb NOT NULL DEFAULT '[]'::jsonb,
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (job_id, round)
);
CREATE INDEX IF NOT EXISTS node_scan_rounds_started_idx ON node_scan_rounds(started_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS node_scan_rounds_protocol_idx ON node_scan_rounds(protocol,started_at DESC);

-- Upgrade existing pools once: every address must have a result and none may
-- be available. The migration marker preserves subsequent manual restores.
CREATE TABLE IF NOT EXISTS app_migrations (
 name text PRIMARY KEY,
 applied_at timestamptz NOT NULL DEFAULT now()
);
WITH upgrade AS (
 INSERT INTO app_migrations(name) VALUES('backfill_empty_protocol_networks_v1')
 ON CONFLICT DO NOTHING RETURNING name
)
INSERT INTO protocol_networks(protocol,network_id,cidr,checked_at)
 SELECT s.protocol,w.id,w.cidr,max(s.checked_at)
 FROM networks w JOIN node_ip_pool s ON s.ip <<= w.cidr
 CROSS JOIN upgrade
 GROUP BY s.protocol,w.id,w.cidr
 HAVING NOT bool_or(s.available)
 AND count(*)=power(2::numeric,(CASE family(w.cidr) WHEN 4 THEN 32 ELSE 128 END)-masklen(w.cidr))
 ON CONFLICT(protocol,network_id) DO NOTHING;

-- New ranges are queued atomically with their configuration. The per-pool
-- Redis lease serializes workers; unfinished work survives process restarts.
CREATE TABLE IF NOT EXISTS network_auto_scans (
 network_id uuid NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
 cidr cidr NOT NULL,
 protocol varchar(32) NOT NULL,
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (network_id, protocol)
);

-- Full rechecks retain results and use one cutoff across overlapping ranges.
ALTER TABLE network_auto_scans ADD COLUMN IF NOT EXISTS recheck_after timestamptz;

-- Keep previous agents and samples on ICMP; TCP histories are isolated by port.
ALTER TABLE latency_probes ADD COLUMN IF NOT EXISTS mode text NOT NULL DEFAULT 'icmp' CHECK (mode IN ('icmp','tcp'));
ALTER TABLE latency_probes ADD COLUMN IF NOT EXISTS tcp_port integer NOT NULL DEFAULT 443 CHECK (tcp_port BETWEEN 1 AND 65535);
ALTER TABLE latency_probe_results ADD COLUMN IF NOT EXISTS mode text NOT NULL DEFAULT 'icmp' CHECK (mode IN ('icmp','tcp'));
ALTER TABLE latency_probe_results ADD COLUMN IF NOT EXISTS tcp_port integer NOT NULL DEFAULT 0 CHECK ((mode='icmp' AND tcp_port=0) OR (mode='tcp' AND tcp_port BETWEEN 1 AND 65535));
CREATE INDEX IF NOT EXISTS latency_probe_results_measurement_idx ON latency_probe_results(probe_id,ip,mode,tcp_port,checked_at DESC);

-- Apply the requested 20-second interval once; later manual edits survive restart.
ALTER TABLE latency_probes ALTER COLUMN interval_seconds SET DEFAULT 20;
WITH upgrade AS (
 INSERT INTO app_migrations(name) VALUES('probe_interval_20_seconds_v1')
 ON CONFLICT DO NOTHING RETURNING name
)
UPDATE latency_probes SET interval_seconds=20 WHERE EXISTS(SELECT 1 FROM upgrade);
