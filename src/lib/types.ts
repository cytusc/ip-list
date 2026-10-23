export type User = { id: string; username: string };
export type Node = {
  share_uri: string;
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
  | { ok: true }
  | { ok: false; error: string; fields?: Record<string, string> };

export type ScanResult = {
  ip: string;
  status: "success" | "failed";
  delay_ms: number;
  change?: "new" | "lost" | "recovered";
  previous_delay_ms?: number;
  error?: string;
};
export type ScanInput = {
  node_id: string;
  network_ids: string[];
  url: string;
  timeout_ms: number;
  concurrency: number;
  interval_minutes: number;
  full_every: number;
};
export type ScanJob = {
  protocol?: string;
  input?: ScanInput;
  round?: number;
  scope?: "full" | "available";
  next_run_at?: string;
  started_at?: string;
  finished_at?: string;
  id: string;
  node_name: string;
  url: string;
  cidrs: string[];
  status: "running" | "waiting" | "completed" | "cancelled" | "interrupted";
  total: number;
  completed: number;
  success: number;
  results: ScanResult[];
  error?: string;
};

export type IPCheck = {
  protocol: string;
  delay_ms: number;
  node_id: string;
  node_name: string;
  checked_at: string;
};
export type AvailableIP = { ip: string; checks: IPCheck[] };
