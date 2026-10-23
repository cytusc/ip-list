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
