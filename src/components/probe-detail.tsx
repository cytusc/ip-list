"use client";
import Link from "next/link";
import { useEffect, useState } from "react";
import { Activity, Copy } from "lucide-react";
import { toast } from "sonner";
import { cancelScan } from "@/app/actions";
import { protocolLabels } from "@/lib/protocol";
import {
  availabilityTone,
  formatDuration,
  formatTime,
  percent,
  scanStatusText,
} from "@/lib/cf";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableHeader,
  TableHead,
  TableRow,
  TableBody,
  TableCell,
} from "@/components/ui/table";
import {
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@/components/ui/select";
import type { ScanRecord } from "@/lib/types";

export function ProbeDetail({ initial }: { initial: ScanRecord }) {
  const [record, setRecord] = useState(initial);
  const [error, setError] = useState("");
  const [stopping, setStopping] = useState(false);
  const [filter, setFilter] = useState("all");
  const [page, setPage] = useState(1);
  const running = record.status === "running";
  const id = record.id;
  useEffect(() => {
    if (!running) return;
    let disposed = false;
    const controller = new AbortController();
    const load = async () => {
      if (document.hidden) return;
      try {
        const response = await fetch(`/api/scan-records/${id}`, {
          cache: "no-store",
          signal: controller.signal,
        });
        const data = await response.json();
        if (!response.ok) throw new Error(data.error || "读取检测记录失败");
        if (disposed) return;
        setRecord(data);
        setError("");
        if (data.status !== "running") setStopping(false);
      } catch (e) {
        if (!disposed && !(e instanceof DOMException && e.name === "AbortError"))
          setError(e instanceof Error ? e.message : "读取检测记录失败");
      }
    };
    const timer = window.setInterval(load, 1500);
    return () => {
      disposed = true;
      controller.abort();
      window.clearInterval(timer);
    };
  }, [id, running]);
  const rate = percent(record.success, record.completed);
  const results = (record.results || [])
    .filter((row) => filter === "all" || row.status === filter)
    .sort((a, b) => {
      if (a.status !== b.status) return a.status === "success" ? -1 : 1;
      return a.delay_ms - b.delay_ms || a.ip.localeCompare(b.ip);
    });
  const pages = Math.max(1, Math.ceil(results.length / 50));
  const current = Math.min(page, pages);
  const successIPs = record.results
    .filter((row) => row.status === "success")
    .sort((a, b) => a.delay_ms - b.delay_ms)
    .map((row) => row.ip);
  return (
    <div className="space-y-6">
      <Link
        href="/probes"
        className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-primary"
      >
        ← 返回检测记录
      </Link>
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-3">
            <h1 className="text-2xl font-semibold">{record.node_name}</h1>
            <Badge variant="secondary">
              {scanStatusText[record.status] ?? record.status}
            </Badge>
          </div>
          <p className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
            <span>{protocolLabels[record.protocol] || record.protocol}</span>
            <span>第 {record.round} 轮</span>
            <span>
              {record.scope === "incremental" ? "增量检测" : record.scope === "available" ? "复测可用 IP" : "全量扫描"}
            </span>
            <span>开始于 {formatTime(record.started_at)}</span>
            <span>
              {record.finished_at
                ? `结束于 ${formatTime(record.finished_at)}`
                : "尚未结束"}
            </span>
            <span>
              用时 {formatDuration(record.started_at, record.finished_at)}
            </span>
          </p>
          <p className="mt-2 break-all text-xs text-muted-foreground">
            {record.cidrs.join("、")} · {record.url}
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            单 IP 超时 {record.input?.timeout_ms ?? "—"} 毫秒 · 并发{" "}
            {record.input?.concurrency ?? "—"} ·{" "}
            {record.input?.interval_minutes
              ? `自动巡检间隔 ${record.input.interval_minutes} 分钟`
              : "单次检测"}
          </p>
        </div>
        {running && (
          <Button
            variant="outline"
            disabled={stopping}
            onClick={async () => {
              setStopping(true);
              try {
                const result = await cancelScan(record.job_id);
                if (!result.ok) {
                  toast.error(result.error);
                  setStopping(false);
                }
              } catch {
                toast.error("停止失败，请重试");
                setStopping(false);
              }
            }}
          >
            {stopping
              ? "正在停止…"
              : record.input?.interval_minutes
                ? "停止巡检"
                : "停止测试"}
          </Button>
        )}
      </div>

      {error && (
        <p
          role="alert"
          className="rounded-lg border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900"
        >
          {error}。正在自动重试，已有结果已保留。
        </p>
      )}
      {record.error && (
        <p role="alert" className="text-sm text-destructive">
          {record.error}
        </p>
      )}

      <div className="grid grid-cols-2 gap-3 xl:grid-cols-4">
        <Metric
          label="已检测 IP"
          value={`${record.completed} / ${record.total}`}
          hint={
            record.completed < record.total
              ? `还有 ${record.total - record.completed} 个未测试`
              : "本轮全部地址已测试"
          }
        />
        <Metric
          label="本轮可用"
          value={String(record.success)}
          hint="代理连通检测网址并返回延迟"
          tone="text-emerald-700"
        />
        <Metric
          label="本轮失败"
          value={String(Math.max(0, record.completed - record.success))}
          hint="超时、TLS 或上游不可用"
          tone="text-red-700"
        />
        <Metric
          label="可用率"
          value={rate == null ? "—" : `${rate}%`}
          hint="可用数 / 已测试数"
          tone={availabilityTone(rate)}
        />
      </div>

      {running && (
        <div className="rounded-xl border bg-white p-5">
          <div className="flex flex-wrap items-center justify-between gap-2 text-sm">
            <span className="flex items-center gap-2 font-medium">
              <Activity className="size-4 animate-pulse text-primary" />
              正在检测
            </span>
            <span className="text-muted-foreground tabular-nums">
              {record.completed} / {record.total}
            </span>
          </div>
          <progress
            aria-label="检测进度"
            className="mt-3 h-2 w-full accent-blue-600"
            max={record.total || 1}
            value={record.completed}
          />
          <p className="mt-2 text-xs text-muted-foreground">
            结果实时刷新，离开页面不会停止任务。
          </p>
        </div>
      )}

      <section className="overflow-hidden rounded-xl border bg-white">
        <div className="flex flex-wrap items-center justify-between gap-3 p-4">
          <div>
            <h2 className="font-semibold">本轮检测结果</h2>
            <p className="mt-1 text-xs text-muted-foreground">
              按可用优先、延迟升序排列，仅代表本轮已测试的 IP。
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Select
              value={filter}
              onValueChange={(value) => {
                setFilter(value);
                setPage(1);
              }}
            >
              <SelectTrigger className="w-36" aria-label="筛选检测结果">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部结果</SelectItem>
                <SelectItem value="success">仅可用</SelectItem>
                <SelectItem value="failed">仅失败</SelectItem>
              </SelectContent>
            </Select>
            <Button
              variant="outline"
              disabled={!successIPs.length}
              onClick={async () => {
                try {
                  await navigator.clipboard.writeText(successIPs.join("\n"));
                  toast.success("可用 IP 已复制");
                } catch {
                  toast.error("复制失败，请手动选择表格中的 IP");
                }
              }}
            >
              <Copy />
              复制可用 IP
            </Button>
          </div>
        </div>
        {results.length ? (
          <>
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="pl-6">连接 IP</TableHead>
                  <TableHead className="w-24">结果</TableHead>
                  <TableHead className="w-24">延迟</TableHead>
                  <TableHead>变化 / 说明</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {results
                  .slice((current - 1) * 50, current * 50)
                  .map((row) => (
                    <TableRow key={row.ip}>
                      <TableCell className="pl-6 font-mono">{row.ip}</TableCell>
                      <TableCell>
                        <Badge
                          variant="outline"
                          className={
                            row.status === "success"
                              ? "text-emerald-700"
                              : "text-red-700"
                          }
                        >
                          {row.status === "success" ? "可用" : "失败"}
                        </Badge>
                      </TableCell>
                      <TableCell className="tabular-nums">
                        {row.status === "success" ? `${row.delay_ms} ms` : "—"}
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {row.change === "new"
                          ? "首次可用 · "
                          : row.change === "lost"
                            ? "本轮失效 · "
                            : row.change === "recovered"
                              ? "恢复可用 · "
                              : ""}
                        {row.previous_delay_ms !== undefined &&
                        row.status === "success"
                          ? `上次 ${row.previous_delay_ms} ms · `
                          : ""}
                        {row.error || "代理请求成功"}
                      </TableCell>
                    </TableRow>
                  ))}
              </TableBody>
            </Table>
            <div className="flex items-center justify-between border-t p-4 text-xs text-muted-foreground">
              <span>
                {results.length} 条结果 · {current} / {pages}
              </span>
              <div className="flex gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  disabled={current === 1}
                  onClick={() => setPage(current - 1)}
                >
                  上一页
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={current === pages}
                  onClick={() => setPage(current + 1)}
                >
                  下一页
                </Button>
              </div>
            </div>
          </>
        ) : (
          <p className="border-t p-10 text-center text-sm text-muted-foreground">
            {running
              ? "等待检测结果…"
              : record.completed === 0
                ? "本轮没有完成任何 IP 的测试，可重新发起检测。"
                : "本轮没有留下逐条结果（仅最近 30 轮保留明细），可用 IP 仍以共用池为准。"}
          </p>
        )}
      </section>
    </div>
  );
}

function Metric({
  label,
  value,
  hint,
  tone,
}: {
  label: string;
  value: string;
  hint: string;
  tone?: string;
}) {
  return (
    <div className="rounded-xl border bg-white p-4">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p
        className={`mt-1.5 text-2xl font-semibold tabular-nums ${tone ?? ""}`}
      >
        {value}
      </p>
      <p className="mt-1 text-[11px] text-muted-foreground">{hint}</p>
    </div>
  );
}
