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

ALTER TABLE networks ADD COLUMN IF NOT EXISTS asn varchar(12) NOT NULL DEFAULT '';
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
