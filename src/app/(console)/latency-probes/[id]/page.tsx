import Link from "next/link";
import { Activity } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ProbeLatencyChart } from "@/components/probe-latency-chart";
import { getLatencyProbeHistory, getLatencyProbes } from "@/lib/queries";

export default async function LatencyProbeDetailPage({
  params,
  searchParams,
}: {
  params: Promise<{ id: string }>;
  searchParams: Promise<{ ip?: string }>;
}) {
  const [{ id }, { ip }, probes] = await Promise.all([
    params,
    searchParams,
    getLatencyProbes(),
  ]);
  const probe = probes.find((item) => item.id === id);
  const history = ip ? await getLatencyProbeHistory(id, ip) : [];
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="mb-2 flex items-center gap-2 text-xs text-muted-foreground">
            <Activity className="size-4" />
            探针延迟历史
          </div>
          <h1 className="text-2xl font-semibold tracking-tight">
            {probe?.name || "探针"}{ip ? ` · ${ip}` : ""}
          </h1>
          <p className="mt-2 text-sm text-muted-foreground">
            显示该探针电脑直连目标 IP 的{probe?.mode === "tcp" ? ` TCP :${probe.tcp_port} 建连时间` : " ICMP 往返时间"}。
          </p>
        </div>
        <Button asChild variant="outline">
          <Link href="/available-ips">返回可用 IP</Link>
        </Button>
      </div>
      {!probe ? (
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
          // This is an async server component: capture this request’s real time.
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
