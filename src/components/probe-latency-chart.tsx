"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Activity, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { analyzeProbeHistory } from "@/lib/probe-history";
import type { LatencyProbe, LatencyProbeResult } from "@/lib/types";

const width = 1000, height = 350;
const plot = { left: 70, right: 22, top: 20, bottom: 76 };
const timeLabel = (value: string | number) => new Date(value).toLocaleString("zh-CN", { hour12: false });
const ms = (value: number | null | undefined) => value == null ? "—" : `${value.toFixed(2)} ms`;

export function ProbeLatencyChart({ probeID, probeName, ip, initial, initialProbe, initialTime }: {
  probeID: string; probeName: string; ip: string; initial: LatencyProbeResult[];
  initialProbe: LatencyProbe; initialTime: number;
}) {
  const [items, setItems] = useState(initial);
  const [probe, setProbe] = useState(initialProbe);
  const [now, setNow] = useState(initialTime);
  const [hours, setHours] = useState(1);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState<number | null>(null);
  const active = useRef<AbortController | null>(null);
  const refresh = useCallback(async () => {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    setRefreshing(true);
    try {
      const response = await fetch(`/api/latency-probes/${encodeURIComponent(probeID)}/history?ip=${encodeURIComponent(ip)}&hours=${hours}`,
        { cache: "no-store", signal: controller.signal });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error || "读取延迟历史失败");
      if (controller.signal.aborted) return;
      setItems(payload.items);
      setProbe(payload.probe);
      setNow(Date.parse(payload.server_time));
      setSelected(null);
      setError("");
    } catch (caught) {
      if (!controller.signal.aborted) setError(caught instanceof Error ? caught.message : "读取延迟历史失败");
    } finally {
      if (!controller.signal.aborted) setRefreshing(false);
    }
  }, [hours, ip, probeID]);

  useEffect(() => {
    const immediate = window.setTimeout(() => void refresh(), 0);
    const visibleRefresh = () => { if (!document.hidden) void refresh(); };
    const timer = window.setInterval(visibleRefresh, 10000);
    document.addEventListener("visibilitychange", visibleRefresh);
    return () => { active.current?.abort(); clearTimeout(immediate); clearInterval(timer); document.removeEventListener("visibilitychange", visibleRefresh); };
  }, [refresh]);

  const gapSeconds = (probe.sample_window_seconds || probe.interval_seconds + 30) * 2;
  const data = useMemo(() => analyzeProbeHistory(items, now, hours, gapSeconds), [items, now, hours, gapSeconds]);
  const yMax = Math.ceil(Math.max(10, (data.maximum ?? 0) * 1.1) / 10) * 10;
  const plotWidth = width - plot.left - plot.right;
  const plotHeight = height - plot.top - plot.bottom;
  const x = (time: string | number) => plot.left + ((typeof time === "string" ? Date.parse(time) : time) - data.from) / (hours * 3600000) * plotWidth;
  const y = (delay: number) => plot.top + (1 - delay / yMax) * plotHeight;
  const current = data.latest;
  const stale = current && now - Date.parse(current.checked_at) > gapSeconds * 1000;
  const state = !probe.enabled ? "已停用" : !probe.online ? "探针离线" : !current ? "等待检测" : stale ? "数据已过期" : current.success ? "ICMP 可达" : "ICMP 超时";
  const hovered = selected == null ? undefined : data.samples[selected];
  function selectAt(clientX: number, bounds: DOMRect) {
    const position = Math.max(0, Math.min(1, ((clientX - bounds.left) / bounds.width * width - plot.left) / plotWidth));
    const time = data.from + position * hours * 3600000;
    let low = 0, high = data.samples.length - 1;
    while (low < high) {
      const middle = Math.floor((low + high) / 2);
      if (Date.parse(data.samples[middle].checked_at) < time) low = middle + 1;
      else high = middle;
    }
    if (low > 0 && time - Date.parse(data.samples[low - 1].checked_at) < Date.parse(data.samples[low].checked_at) - time) low--;
    setSelected(low);
  }
  return (
    <section className="space-y-4 rounded-xl border bg-white p-4 sm:p-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="font-semibold">{probeName} 到 {ip}</h2>
          <p className="mt-1 text-xs text-muted-foreground">ICMP 往返延迟 · 检测间隔 {probe.interval_seconds} 秒 · 每 10 秒刷新</p>
        </div>
        <div className="flex items-center gap-2">
          <select aria-label="选择历史时间范围" className="h-9 rounded-md border bg-white px-3 text-sm" value={hours} onChange={event => { setSelected(null); setHours(Number(event.target.value)); }}>
            {[1, 6, 24, 72, 168].map(value => <option key={value} value={value}>最近 {value < 24 ? `${value} 小时` : `${value / 24} 天`}</option>)}
          </select>
          <Button variant="outline" size="icon" aria-label="刷新延迟历史" disabled={refreshing} onClick={() => void refresh()}><RefreshCw className={refreshing ? "animate-spin" : ""} /></Button>
        </div>
      </div>
      {error && <p role="alert" className="rounded-md bg-amber-50 p-3 text-sm text-amber-900">{error}；当前保留上次成功获取的数据。</p>}
      <div className="flex flex-wrap items-center gap-x-5 gap-y-2 rounded-lg bg-slate-50 p-3 text-sm">
        <span className="font-medium">{state}</span>
        <span>最近一次：{current ? current.success ? ms(current.delay_ms) : "超时" : "—"}{current && (!probe.online || stale) ? "（历史值）" : ""}</span>
        <span className="text-xs text-muted-foreground">检测时间：{current ? timeLabel(current.checked_at) : "暂无"}</span>
        <span className="text-xs text-muted-foreground">心跳：{probe.last_seen ? timeLabel(probe.last_seen) : "尚未连接"}</span>
      </div>
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-6">
        <Stat label="平均延迟" value={ms(data.average)} />
        <Stat label="最低 / 最高 (ms)" value={data.minimum == null ? "—" : `${data.minimum.toFixed(2)} / ${data.maximum?.toFixed(2)}`} />
        <Stat label="P95 延迟" value={ms(data.p95)} />
        <Stat label="平均相邻抖动" value={ms(data.jitter)} />
        <Stat label="采样超时率" value={data.loss == null ? "—" : `${data.loss.toFixed(1)}%`} />
        <Stat label="成功 / 总采样" value={`${data.samples.length - data.failed.length} / ${data.samples.length}`} />
      </div>
      {data.samples.length ? (
        <div className="rounded-lg border bg-slate-50/50 p-2">
          <p aria-live="polite" className="min-h-7 px-2 text-xs tabular-nums text-slate-600">{hovered ? `${timeLabel(hovered.checked_at)} · ${hovered.success ? ms(hovered.delay_ms) : "ICMP 超时"}` : "移动到曲线上查看采样，或聚焦后使用左右方向键。"}</p>
          <svg viewBox={`0 0 ${width} ${height}`} className="w-full" role="img" aria-label={`${probeName} 对 ${ip} 的 ICMP 延迟历史曲线`} tabIndex={0}
            onPointerMove={event => selectAt(event.clientX, event.currentTarget.getBoundingClientRect())} onPointerLeave={() => setSelected(null)}
            onKeyDown={event => { if (["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) { event.preventDefault(); setSelected(value => event.key === "Home" ? 0 : event.key === "End" ? data.samples.length - 1 : Math.max(0, Math.min(data.samples.length - 1, (value ?? data.samples.length - 1) + (event.key === "ArrowLeft" ? -1 : 1)))); } }}>
            {[0, .25, .5, .75, 1].map(fraction => <g key={fraction}>
              <line x1={plot.left} x2={width - plot.right} y1={plot.top + fraction * plotHeight} y2={plot.top + fraction * plotHeight} stroke="#e2e8f0" strokeDasharray="4 5" />
              <text x={plot.left - 10} y={plot.top + fraction * plotHeight + 4} textAnchor="end" className="fill-slate-500 text-[12px]">{(yMax * (1 - fraction)).toFixed(0)} ms</text>
            </g>)}
            {[0, .25, .5, .75, 1].map(fraction => {
              const value = data.from + hours * 3600000 * fraction;
              return <text key={fraction} x={plot.left + fraction * plotWidth} y={height - 12} textAnchor={fraction === 0 ? "start" : fraction === 1 ? "end" : "middle"} className="fill-slate-500 text-[12px]">{new Date(value).toLocaleString("zh-CN", { ...(hours >= 24 ? { month: "2-digit", day: "2-digit" } as const : {}), hour: "2-digit", minute: "2-digit", hour12: false })}</text>;
            })}
            <text x={plot.left - 10} y={height - 43} textAnchor="end" className="fill-red-600 text-[12px]">超时</text>
            {data.failed.map(point => <line key={point.id} x1={x(point.checked_at)} x2={x(point.checked_at)} y1={height - 56} y2={height - 36} stroke="#ef4444" strokeWidth="2" />)}
            {data.segments.map((segment, index) => <g key={index} data-segment="latency">
              <polyline points={segment.map(point => `${x(point.checked_at)},${y(point.delay_ms)}`).join(" ")} fill="none" stroke="#2563eb" strokeWidth="2" strokeLinejoin="round" />
              {segment.filter((_, index) => segment.length < 600 || index % Math.ceil(segment.length / 600) === 0).map(point => <circle key={point.id} cx={x(point.checked_at)} cy={y(point.delay_ms)} r="2.5" fill="#2563eb" />)}
            </g>)}
            {hovered && <g pointerEvents="none">
              <line x1={x(hovered.checked_at)} x2={x(hovered.checked_at)} y1={plot.top} y2={height - 33} stroke="#64748b" strokeDasharray="3 3" />
              <circle cx={x(hovered.checked_at)} cy={hovered.success ? y(hovered.delay_ms) : height - 46} r="5" fill={hovered.success ? "#2563eb" : "#ef4444"} stroke="white" strokeWidth="2" />
            </g>}
          </svg>
        </div>
      ) : <div className="rounded-lg border border-dashed py-14 text-center"><Activity className="mx-auto size-7 text-slate-300" /><p className="mt-3 text-sm font-medium">此时间段暂无延迟记录</p><p className="mt-1 text-xs text-muted-foreground">检查探针连接，或选择更长的时间范围。</p></div>}
      <p className="text-xs text-muted-foreground">蓝线为 ICMP 延迟，红色标记为超时，断报留空且不连线。超时率仅统计已上报采样，缺失采样不计为丢包；抖动为连续成功采样延迟差的平均绝对值。目标屏蔽 ICMP 时仍可能支持代理连接。</p>
    </section>
  );
}
function Stat({ label, value }: { label: string; value: string }) {
  return <div className="rounded-lg border bg-white p-3"><p className="text-xs text-muted-foreground">{label}</p><p className="mt-1 text-sm font-medium tabular-nums">{value}</p></div>;
}
