"use client";

import Link from "next/link";
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
  const failure = probe.mode === "tcp" ? "TCP 连接失败或超时" : "ICMP 超时";
  const thresholds = latencyThresholds(location?.country, location?.region, location?.city);
  // Retain the last real sample for compatibility while API/web roll out.
  const samples: (LatencyProbeResult | null)[] = [...(probe.samples ?? (probe.checked_at && probe.success != null ? [{
    id: 0, ip, delay_ms: probe.delay_ms ?? 0, success: probe.success, checked_at: probe.checked_at,
  }] : []))];
  const stale = !probe.online || !probe.fresh;
  if (stale && samples.length) samples.push(null);
  const cells = [...Array<null>(Math.max(0, 16 - samples.length)).fill(null), ...samples].slice(-16);
  const selected = hovered == null ? cells.at(-1) : cells[hovered];
  const quality = selected ? latencyQuality(selected.delay_ms, selected.success, location?.country, location?.region, location?.city) : "unknown";
  const description = selected
    ? `${selected.success ? `${qualityNames[quality]} · ${selected.delay_ms.toFixed(2)} ms` : failure}；${formatTime(selected.checked_at)}`
    : stale && probe.checked_at ? "暂无新采样，最近结果已转为历史记录" : "暂无采样";
  const summary = `${probe.name}：${description}。${thresholds.name}，绿色不超过 ${thresholds.green} 毫秒，黄色不超过 ${thresholds.yellow} 毫秒。查看延迟曲线`;
  return <Tooltip.Provider delayDuration={120}>
    <Tooltip.Root>
      <Tooltip.Trigger asChild>
        <Link href={`/latency-probes/${probe.probe_id}?ip=${encodeURIComponent(ip)}`} aria-label={summary}
          onPointerLeave={() => setHovered(null)} onFocus={() => setHovered(null)}
          className="group inline-flex items-center gap-2 rounded-md px-1 py-1.5 text-xs hover:bg-slate-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary">
          <span className="max-w-20 truncate font-medium">{probe.name}</span>
          <span className="flex shrink-0 gap-0.5" aria-hidden="true">
            {cells.map((sample, index) => {
              const quality = sample ? latencyQuality(sample.delay_ms, sample.success, location?.country, location?.region, location?.city) : "unknown";
              return <span key={index} data-quality={quality} onPointerEnter={() => setHovered(index)}
                className={`h-5 w-1.5 rounded-[2px] ${styles[quality]} ${sample && !sample.success ? "bg-[repeating-linear-gradient(135deg,transparent,transparent_2px,rgba(0,0,0,0.15)_2px,rgba(0,0,0,0.15)_4px)]" : ""} ${hovered === index ? "ring-1 ring-slate-500 ring-offset-1" : ""}`} />;
            })}
          </span>
        </Link>
      </Tooltip.Trigger>
      <Tooltip.Portal><Tooltip.Content sideOffset={8} className="z-50 max-w-80 rounded-lg bg-slate-900 px-3 py-2 text-xs leading-5 text-white shadow-lg">
        <p className="font-medium">{probe.name} · {ip} · {measurement}</p>
        <p>{description}</p>
        <p className="text-slate-300">{thresholds.name}：绿 ≤{thresholds.green} ms · 黄 ≤{thresholds.yellow} ms · 红 &gt;{thresholds.yellow} ms 或超时</p>
        {stale && <p className="text-slate-300">色格保留历史采样，最右灰格表示等待新结果。</p>}
        <p className="text-slate-300">左旧右新 · 点击查看完整曲线</p>
        <Tooltip.Arrow className="fill-slate-900" />
      </Tooltip.Content></Tooltip.Portal>
    </Tooltip.Root>
  </Tooltip.Provider>;
}
