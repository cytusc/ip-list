"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Activity, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { LatencyProbeResult } from "@/lib/types";

const width = 1000;
const height = 320;
const plot = { left: 64, right: 22, top: 18, bottom: 46 };
const timeLabel = (value: string) =>
  new Date(value).toLocaleString("zh-CN", { hour12: false });

export function ProbeLatencyChart({
  probeID,
  probeName,
  ip,
  initial,
}: {
  probeID: string;
  probeName: string;
  ip: string;
  initial: LatencyProbeResult[];
}) {
  const [items, setItems] = useState(initial);
  const [hours, setHours] = useState(24);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState("");

  const refresh = useCallback(async (signal?: AbortSignal) => {
    setRefreshing(true);
    try {
      const response = await fetch(
        `/api/latency-probes/${encodeURIComponent(probeID)}/history?ip=${encodeURIComponent(ip)}&hours=${hours}`,
        { cache: "no-store", signal },
      );
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error || "读取延迟历史失败");
      setItems(payload);
      setError("");
    } catch (caught) {
      if (caught instanceof DOMException && caught.name === "AbortError") return;
      setError(caught instanceof Error ? caught.message : "读取延迟历史失败");
    } finally {
      setRefreshing(false);
    }
  }, [hours, ip, probeID]);

  useEffect(() => {
    const controller = new AbortController();
    const timer = window.setInterval(() => {
      if (!document.hidden) void refresh(controller.signal);
    }, 15000);
    return () => {
      controller.abort();
      window.clearInterval(timer);
    };
  }, [refresh]);

  const { points, failures, stats, labels, endTime } = useMemo(() => {
    const good = items.filter((item) => item.success);
    const bad = items.filter((item) => !item.success);
    const endTime = items.length
      ? new Date(items[items.length - 1].checked_at).getTime()
      : 0;
    const from = endTime - hours * 60 * 60 * 1000;
    const values = good.map((item) => item.delay_ms);
    const maxValue = Math.max(10, ...values);
    const yMax = Math.ceil(maxValue / 5) * 5;
    const plotWidth = width - plot.left - plot.right;
    const plotHeight = height - plot.top - plot.bottom;
    const x = (time: number) => plot.left + ((time - from) / (endTime - from)) * plotWidth;
    const y = (delay: number) => plot.top + (1 - delay / yMax) * plotHeight;
    const points = good.map((item) => ({
      ...item,
      x: x(new Date(item.checked_at).getTime()),
      y: y(item.delay_ms),
    }));
    const failures = bad.map((item) => ({
      ...item,
      x: x(new Date(item.checked_at).getTime()),
      y: plot.top + plotHeight,
    }));
    const avg = values.length ? values.reduce((sum, value) => sum + value, 0) / values.length : null;
    const total = items.length;
    const labels = [0, 0.25, 0.5, 0.75, 1].map((fraction) => ({
      y: plot.top + fraction * plotHeight,
      value: yMax * (1 - fraction),
    }));
    return {
      points,
      failures,
      labels,
      endTime,
      stats: {
        count: total,
        loss: total ? Math.round((bad.length / total) * 100) : 0,
        average: avg,
        minimum: values.length ? Math.min(...values) : null,
        maximum: values.length ? Math.max(...values) : null,
      },
    };
  }, [hours, items]);

  const line = points.map((point) => `${point.x},${point.y}`).join(" ");
  return (
    <section className="space-y-4 rounded-xl border bg-white p-4 sm:p-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="font-semibold">{probeName} 到 {ip}</h2>
          <p className="mt-1 text-xs text-muted-foreground">最近 {hours} 小时 · 每 15 秒自动刷新</p>
        </div>
        <div className="flex items-center gap-2">
          <select
            aria-label="选择历史时间范围"
            className="h-9 rounded-md border bg-white px-3 text-sm"
            value={hours}
            onChange={(event) => setHours(Number(event.target.value))}
          >
            <option value={1}>最近 1 小时</option>
            <option value={6}>最近 6 小时</option>
            <option value={24}>最近 24 小时</option>
            <option value={72}>最近 3 天</option>
            <option value={168}>最近 7 天</option>
          </select>
          <Button variant="outline" size="icon" aria-label="刷新延迟历史" disabled={refreshing} onClick={() => void refresh()}>
            <RefreshCw className={refreshing ? "animate-spin" : ""} />
          </Button>
        </div>
      </div>
      {error && <p role="alert" className="rounded-md bg-amber-50 p-3 text-sm text-amber-900">{error}</p>}
      <div className="grid gap-3 sm:grid-cols-4">
        <Stat label="平均延迟" value={stats.average == null ? "—" : `${stats.average.toFixed(2)} ms`} />
        <Stat label="最低 / 最高" value={stats.minimum == null ? "—" : `${stats.minimum.toFixed(2)} / ${stats.maximum?.toFixed(2)} ms`} />
        <Stat label="超时率" value={stats.count ? `${stats.loss}%` : "—"} />
        <Stat label="检测次数" value={`${stats.count}`} />
      </div>
      {stats.count ? (
        <div className="overflow-x-auto rounded-lg border bg-slate-50/50 p-2">
          <svg viewBox={`0 0 ${width} ${height}`} className="min-w-[720px] w-full" role="img" aria-label={`${probeName} 对 ${ip} 的 ICMP 延迟历史曲线`}>
            {labels.map((label) => (
              <g key={label.value}>
                <line x1={plot.left} x2={width - plot.right} y1={label.y} y2={label.y} stroke="#e2e8f0" strokeDasharray="4 5" />
                <text x={plot.left - 10} y={label.y + 4} textAnchor="end" className="fill-slate-500 text-[12px]">{label.value.toFixed(0)} ms</text>
              </g>
            ))}
            {[0, 0.25, 0.5, 0.75, 1].map((fraction) => {
              const x = plot.left + fraction * (width - plot.left - plot.right);
              const value = endTime - hours * 60 * 60 * 1000 * (1 - fraction);
              return <text key={fraction} x={x} y={height - 12} textAnchor={fraction === 0 ? "start" : fraction === 1 ? "end" : "middle"} className="fill-slate-500 text-[12px]">{new Date(value).toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" })}</text>;
            })}
            {points.length > 1 && <polyline points={line} fill="none" stroke="#2563eb" strokeWidth="2.5" strokeLinejoin="round" strokeLinecap="round" />}
            {points.map((point) => <circle key={point.id} cx={point.x} cy={point.y} r="3.5" fill="#2563eb"><title>{`${timeLabel(point.checked_at)} · ${point.delay_ms.toFixed(2)} ms`}</title></circle>)}
            {failures.map((point) => <circle key={point.id} cx={point.x} cy={point.y} r="4" fill="#ef4444"><title>{`${timeLabel(point.checked_at)} · 超时`}</title></circle>)}
          </svg>
        </div>
      ) : (
        <div className="rounded-lg border border-dashed py-14 text-center">
          <Activity className="mx-auto size-7 text-slate-300" />
          <p className="mt-3 text-sm font-medium">还没有延迟记录</p>
          <p className="mt-1 text-xs text-muted-foreground">确认探针正在运行，并等待下一次检测上报。</p>
        </div>
      )}
      <p className="text-xs text-muted-foreground">蓝线表示收到 ICMP 回复，红点表示超时。目标可能屏蔽 ICMP；超时不一定代表该 IP 的代理连接不可用。</p>
    </section>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return <div className="rounded-lg border bg-white p-3"><p className="text-xs text-muted-foreground">{label}</p><p className="mt-1 font-medium tabular-nums">{value}</p></div>;
}
