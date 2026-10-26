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
};
export type Network = {
  disabled_protocols?: string[];
  asn: string;
  provider: string;
  id: string;
  name: string;
  cidr: string;
  enabled: boolean;
  description: string;
  created_at: string;
  updated_at: string;
};
export type ActionResult =
  | { ok: true; warning?: string }
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
  incremental?: boolean;
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
  scope?: "full" | "available" | "incremental";
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
/** 一轮连通性检测记录：列表一条记录对应一次轮次，而不是整个任务。 */
export type ScanRecord = {
  id: number;
  job_id: string;
  round: number;
  protocol: string;
  node_name: string;
  scope: "full" | "available" | "incremental";
  url: string;
  cidrs: string[];
  input: ScanInput;
  started_at: string;
  finished_at?: string | null;
  total: number;
  completed: number;
  success: number;
  status: ScanJob["status"];
  error?: string;
  results: ScanResult[];
  /** 数据来自正在运行的任务快照，而不是已保存的记录 */
  live?: boolean;
  next_run_at?: string | null;
};
/** 后台仍在进行的任务：巡检等待下一轮时没有任何记录处于运行状态。 */
export type ActiveScan = {
  job_id: string;
  protocol: string;
  node_name: string;
  round: number;
  status: "running" | "waiting";
  total: number;
  completed: number;
  success: number;
  interval_minutes: number;
  next_run_at?: string | null;
};
export type ScanRecordList = {
  items: ScanRecord[];
  active: ActiveScan | null;
};

export type IPCheck = {
  protocol: string;
  delay_ms: number;
  node_id: string;
  node_name: string;
  checked_at: string;
};
export type AvailableIP = {
  ip: string;
  asn?: string;
  provider?: string;
  checks: IPCheck[];
  probe_delays: ProbeIPDelay[];
};
export type LatencyProbe = {
  id: string;
  name: string;
  interval_seconds: number;
  timeout_ms: number;
  concurrency: number;
  interface_name: string;
  mode: "icmp" | "tcp";
  tcp_port: number;
  enabled: boolean;
  last_seen?: string | null;
  created_at: string;
  online: boolean;
  sample_window_seconds: number;
};
export type ProbeIPDelay = {
  window_end?: string;
  sample_window_seconds?: number;
  mode: "icmp" | "tcp";
  tcp_port: number;
  samples: LatencyProbeResult[];
  online: boolean;
  fresh: boolean;
  probe_id: string;
  name: string;
  delay_ms: number | null;
  success: boolean | null;
  checked_at: string | null;
};
export type LatencyProbeResult = {
  id: number;
  ip: string;
  delay_ms: number;
  success: boolean;
  checked_at: string;
};
export type BlacklistedIP = { ip: string; created_at: string };
export type IPLocation = {
  country: string;
  region: string;
  city: string;
};
export type GeneratedLink = {
  node_id: string;
  node_name: string;
  protocol: string;
  uri: string;
};

export type CFTarget = {
  id: string;
  name: string;
  url: string;
  created_at: string;
  last_checked_at?: string | null;
  /** 该域名当前可用的 IP 数 */
  available_count?: number;
  /** 所有域名共用的 IP 池可用数 */
  shared_count?: number;
  /** 最近一轮检测的可用率（百分比整数），无记录时为 null */
  availability?: number | null;
  /** 累计可用率（百分比整数） */
  overall?: number | null;
  rounds?: number;
  checked_total?: number | null;
  checked_success?: number | null;
};
export type CFIP = {
  ip: string;
  asn?: string;
  provider?: string;
  http_status: number;
  checked_at: string;
};
export type CFRound = {
  finished_at: string;
  total: number;
  success: number;
  status: "completed" | "cancelled" | "interrupted";
};
export type CFDetail = {
  target: CFTarget;
  items: CFIP[];
  history: CFRound[];
};
