"use client";

import Link from "next/link";
import { aggregateProbeMinutes } from "@/lib/probe-history";
import { useState } from "react";
import { Tooltip } from "radix-ui";
import { latencyQuality, latencyThresholds } from "@/lib/latency-quality";
import type { IPLocation, LatencyProbeResult, ProbeIPDelay } from "@/lib/types";

const styles = { green: "bg-emerald-500", yellow: "bg-amber-400", red: "bg-rose-500", unknown: "bg-slate-200" };
const qualityNames = { green: "低延迟", yellow: "中等延迟", red: "高延迟", unknown: "暂无采样" };
const formatTime = (value: string) => new Date(value).toLocaleString("zh-CN", { hour12: false });

export function ProbeLatencyStrip({ probe, ip, location }: { probe: ProbeIPDelay; ip: string; location?: IPLocation | null }) {
  const [hovered, setHovered] = useState<number | null>(null);
  const measurement = probe.mode === "tcp" ? `TCP :${probe.tcp_port}` : "ICMP";
  const thresholds = latencyThresholds(location?.country, location?.region, location?.city);
  const samples: LatencyProbeResult[] = probe.samples ?? (probe.checked_at && probe.success != null ? [{
    id: 0, ip, delay_ms: probe.delay_ms ?? 0, success: probe.success, checked_at: probe.checked_at,
  }] : []);
  const cells = aggregateProbeMinutes(samples, Date.parse(probe.window_end || probe.checked_at || "1970-01-01T00:00:00Z"), (probe.sample_window_seconds || 53) * 2);
  type Cell = typeof cells[number];
  const cellQuality = (cell: Cell) => cell.delay_ms != null
    ? latencyQuality(cell.delay_ms, true, location?.country, location?.region, location?.city)
    : cell.unavailable ? "red" : "unknown";
  const stale = !probe.online || !probe.fresh;
  const selected = cells[hovered ?? cells.length - 1];
  const quality = cellQuality(selected);
  const description = `${selected.delay_ms != null ? `${qualityNames[quality]} · 分钟平均 ${selected.delay_ms.toFixed(2)} ms` : selected.unavailable ? "持续不可用（连续失败满 60 秒）" : "本分钟暂无有效延迟"}；${formatTime(new Date(selected.start).toISOString())}–${formatTime(new Date(selected.start + 60000).toISOString())}；有效采样 ${selected.successCount}/${selected.totalCount}`;
  const summary = `${probe.name}：${description}。${thresholds.name}，绿色不超过 ${thresholds.green} 毫秒，黄色不超过 ${thresholds.yellow} 毫秒。查看延迟曲线`;
  return <Tooltip.Provider delayDuration={120}>
    <Tooltip.Root>
      <Tooltip.Trigger asChild>
        <Link href={`/latency-probes/${probe.probe_id}?ip=${encodeURIComponent(ip)}`} aria-label={summary}
          onPointerLeave={() => setHovered(null)} onFocus={() => setHovered(null)}
          className="group inline-flex items-center gap-2 rounded-md px-1 py-1.5 text-xs hover:bg-slate-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary">
          <span className="max-w-20 truncate font-medium">{probe.name}</span>
          <span className="flex shrink-0 gap-0.5" aria-hidden="true">
            {cells.map((cell, index) => {
              const quality = cellQuality(cell);
              return <span key={index} data-quality={quality} onPointerEnter={() => setHovered(index)}
                className={`h-5 w-1.5 rounded-[2px] ${styles[quality]} ${cell.unavailable && cell.delay_ms == null ? "bg-[repeating-linear-gradient(135deg,transparent,transparent_2px,rgba(0,0,0,0.15)_2px,rgba(0,0,0,0.15)_4px)]" : ""} ${hovered === index ? "ring-1 ring-slate-500 ring-offset-1" : ""}`} />;
            })}
          </span>
        </Link>
      </Tooltip.Trigger>
      <Tooltip.Portal><Tooltip.Content sideOffset={8} className="z-50 max-w-80 rounded-lg bg-slate-900 px-3 py-2 text-xs leading-5 text-white shadow-lg">
        <p className="font-medium">{probe.name} · {ip} · {measurement}</p>
        <p>{description}</p>
        <p className="text-slate-300">{thresholds.name}：绿 ≤{thresholds.green} ms · 黄 ≤{thresholds.yellow} ms · 红 &gt;{thresholds.yellow} ms 或持续不可用</p>
        {stale && <p className="text-slate-300">探针离线或数据过期；色格保留对应分钟的历史结果。</p>}
        <p className="text-slate-300">每格 1 分钟，取成功采样平均值；当前分钟持续更新。左旧右新 · 点击查看完整曲线</p>
        <Tooltip.Arrow className="fill-slate-900" />
      </Tooltip.Content></Tooltip.Portal>
    </Tooltip.Root>
  </Tooltip.Provider>;
}
