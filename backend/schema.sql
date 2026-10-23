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
  node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
  action varchar(5) NOT NULL DEFAULT 'deny' CHECK (action IN ('allow', 'deny')),
  enabled boolean NOT NULL DEFAULT true,
  description varchar(500) NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (node_id, cidr)
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
