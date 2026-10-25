import type { ScanJob } from "@/lib/types";

export const scanStatusText: Record<ScanJob["status"], string> = {
  running: "检测中",
  completed: "检测完成",
  cancelled: "已停止",
  interrupted: "已中断",
  waiting: "等待中",
};

export function formatTime(value?: string | null) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleString("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
}

/** 检测耗时，用于进度页展示“用时 3 分 12 秒”。 */
export function formatDuration(from?: string, to?: string | null) {
  if (!from) return "—";
  const start = new Date(from).getTime();
  const end = to ? new Date(to).getTime() : Date.now();
  const seconds = Math.max(0, Math.round((end - start) / 1000));
  if (seconds < 60) return `${seconds} 秒`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} 分 ${seconds % 60} 秒`;
  return `${Math.floor(minutes / 60)} 小时 ${minutes % 60} 分`;
}

/** 百分比，保留两位小数；分母为 0 时返回 null。大网段里 1 个可用 IP 也能显示为 0.03%。 */
export function percent(part: number, total: number) {
  if (!total) return null;
  return Math.round((part / total) * 10000) / 100;
}

export function availabilityTone(value?: number | null) {
  if (value == null) return "text-muted-foreground";
  if (value >= 60) return "text-emerald-700";
  if (value >= 20) return "text-amber-700";
  return "text-slate-600";
}
