// Thresholds are based on destination geography for probes in mainland China.
export function latencyThresholds(country?: string) {
  const code = country?.trim().toUpperCase();
  if (code && ["HK", "MO", "TW"].includes(code)) return { name: "港澳台", green: 80, yellow: 150 };
  if (code && ["JP", "KR", "SG", "MY", "TH", "VN", "PH", "ID", "KH", "LA", "MM", "BN", "TL"].includes(code)) {
    return { name: "日韩及东南亚", green: 120, yellow: 200 };
  }
  return { name: code ? "其他地区" : "未知地区（默认阈值）", green: 200, yellow: 300 };
}

export function latencyQuality(delay: number | null, success: boolean | null, country?: string) {
  if (success == null || (success && (delay == null || !Number.isFinite(delay) || delay < 0))) return "unknown";
  if (!success) return "red";
  const thresholds = latencyThresholds(country);
  return delay! <= thresholds.green ? "green" : delay! <= thresholds.yellow ? "yellow" : "red";
}
