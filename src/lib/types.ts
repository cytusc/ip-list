export type User = { id: string; username: string };
export type Node = {
  id: string;
  name: string;
  address: string;
  port: number;
  enabled: boolean;
  description: string;
  created_at: string;
  updated_at: string;
  network_count: number;
};
export type Network = {
  id: string;
  name: string;
  cidr: string;
  node_id: string;
  node_name: string;
  action: "allow" | "deny";
  enabled: boolean;
  description: string;
  created_at: string;
  updated_at: string;
};
export type ActionResult =
  { ok: true } | { ok: false; error: string; fields?: Record<string, string> };
