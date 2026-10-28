export type DNSConfig = {
  domain: string;
  enabled: boolean;
  interval_seconds: number;
  ttl: number;
  max_ips: number;
  window_minutes: number;
  min_success_percent: number;
  probe_id: string;
  protocol: string;
};
export type DNSRecord = {
  name: string; type: string; country: string; current: string[]; desired: string[];
  action: string; reason: string; applied: boolean;
};
export type DNSReport = {
  candidate_count?: number;
  mode?: string; domain?: string; config?: DNSConfig; started_at?: string; finished_at?: string;
  error?: string; warning?: string; records?: DNSRecord[];
  candidates?: { ip: string; country: string; state: string; reason: string; delay_ms: number }[];
};
export type DNSStatus = {
  config: DNSConfig; credentials_ready: boolean; running: boolean; requested: string;
  next_run_at: string; report: DNSReport;
  history: { id: number; started_at: string; report: DNSReport }[];
};
