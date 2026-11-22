"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { Activity } from "lucide-react";
import { api } from "@/lib/api";
import { getLatencyProbeHistory } from "@/lib/queries";
import { ProbeLatencyChart } from "@/components/probe-latency-chart";
import { Button } from "@/components/ui/button";
import type { LatencyProbe, LatencyProbeResult } from "@/lib/types";

export default function LatencyProbeDetailPage() {
  const [id, setId] = useState("");
  const [ip, setIP] = useState("");
  const [probe, setProbe] = useState<LatencyProbe | null | undefined>(undefined);
  const [history, setHistory] = useState<LatencyProbeResult[]>([]);

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const probeID = params.get("id") || "";
    const address = params.get("ip") || "";
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setId(probeID);
    setIP(address);
    if (!probeID) {
      setProbe(null);
      return;
    }
    api<LatencyProbe[]>(`/latency-probes?id=${encodeURIComponent(probeID)}`)
      .then(({ data }) => {
        const found = data[0] || null;
        setProbe(found);
        if (found && address)
          return getLatencyProbeHistory(probeID, address, 1).then(setHistory);
      })
      .catch(() => setProbe(null));
  }, []);

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="mb-2 flex items-center gap-2 text-xs text-muted-foreground">
            <Activity className="size-4" />
            探针延迟历史
          </div>
          <h1 className="text-2xl font-semibold tracking-tight">
            {probe?.name || "探针"}
            {ip ? ` · ${ip}` : ""}
          </h1>
          <p className="mt-2 text-sm text-muted-foreground">
            显示该探针电脑直连目标 IP 的
            {probe?.mode === "tcp"
              ? ` TCP :${probe.tcp_port} 建连时间`
              : " ICMP 往返时间"}
            。
          </p>
        </div>
        <Button asChild variant="outline">
          <Link href="/available-ips">返回可用 IP</Link>
        </Button>
      </div>
      {probe === undefined ? (
        <p className="py-16 text-center text-sm text-muted-foreground">正在加载探针…</p>
      ) : !probe ? (
        <div className="rounded-xl border border-dashed bg-white p-8 text-center">
          <p className="font-medium">探针不存在或已删除</p>
          <Button asChild variant="link" className="mt-2">
            <Link href="/latency-probes">前往探针管理</Link>
          </Button>
        </div>
      ) : ip ? (
        <ProbeLatencyChart
          probeID={id}
          probeName={probe.name}
          ip={ip}
          initial={history}
          initialProbe={probe}
          // eslint-disable-next-line react-hooks/purity
          initialTime={Date.now()}
        />
      ) : (
        <div className="rounded-xl border border-dashed bg-white p-8 text-center">
          <p className="font-medium">选择一个 IP 查看延迟曲线</p>
          <p className="mt-2 text-sm text-muted-foreground">
            在可用 IP 列表中点击“探针延迟”里的探针名称。
          </p>
          <Button asChild variant="link" className="mt-2">
            <Link href="/available-ips">前往可用 IP 列表</Link>
          </Button>
        </div>
      )}
    </div>
  );
}
