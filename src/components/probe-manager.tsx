"use client";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useRef, useState, useTransition } from "react";
import { Activity, ArrowRight, Clock, History, Loader2, RotateCcw } from "lucide-react";
import { toast } from "sonner";
import { cancelScan, startScan } from "@/app/actions";
import { protocolLabels } from "@/lib/protocol";
import { availabilityTone, formatDuration, formatTime, percent } from "@/lib/cf";
import { NewProbeDialog } from "@/components/probe-form";
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
import type {
  ActiveScan,
  Network,
  Node,
  ScanRecord,
  ScanRecordList,
} from "@/lib/types";

const statusText: Record<string, string> = {
  running: "检测中",
  waiting: "等待下轮",
  completed: "已完成",
  cancelled: "已停止",
  interrupted: "已中断",
};

export function ProbeList({
  nodes,
  networks,
  initial,
}: {
  nodes: Node[];
  networks: Network[];
  initial: ScanRecordList;
}) {
  const [data, setData] = useState(initial);
  const [error, setError] = useState("");
  const [stopping, setStopping] = useState(false);
  const [protocol, setProtocol] = useState("");
  const [page, setPage] = useState(1);
  const [version, setVersion] = useState(0);
  const router = useRouter();
  // 一轮检测结束后刷新服务端数据：协议池禁用的网段等信息只在服务端渲染。
  const seen = useRef<string | null>(null);
  const active = data.active;
  const scanning = data.items.some((item) => item.status === "running");
  const busy = scanning || !!active;
  useEffect(() => {
    let disposed = false;
    const controller = new AbortController();
    const load = async () => {
      if (document.hidden) return;
      try {
        const response = await fetch("/api/scan-records", {
          cache: "no-store",
          signal: controller.signal,
        });
        const payload = await response.json();
        if (!response.ok) throw new Error(payload.error || "读取检测记录失败");
        if (disposed) return;
        setData(payload);
        setError("");
        const latest = payload.items?.[0] as ScanRecord | undefined;
        if (latest) {
          const key = `${latest.id}:${latest.status}`;
          if (seen.current === null) seen.current = key;
          else if (seen.current !== key) {
            seen.current = key;
            if (latest.status !== "running") router.refresh();
          }
        }
      } catch (e) {
        if (!disposed && !(e instanceof DOMException && e.name === "AbortError"))
          setError(e instanceof Error ? e.message : "读取检测记录失败");
      }
    };
    const timer = window.setInterval(load, busy ? 2000 : 10000);
    void load();
    return () => {
      disposed = true;
      controller.abort();
      window.clearInterval(timer);
    };
  }, [busy, version, router]);
  const protocols = [...new Set(data.items.map((item) => item.protocol))];
  const filtered = protocol
    ? data.items.filter((item) => item.protocol === protocol)
    : data.items;
  const pages = Math.max(1, Math.ceil(filtered.length / 20));
  const current = Math.min(page, pages);
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0 sm:flex-1">
          <div className="mb-2 flex items-center gap-2 text-xs text-muted-foreground">
            <Activity className="size-4" />
            节点检测
          </div>
          <h1 className="text-2xl font-semibold">连通性检测记录</h1>
          <p className="mt-2 max-w-2xl text-sm leading-6 text-muted-foreground">
            每轮检测保存为一条记录，可回看该轮的可用 IP、延迟与变化。同一协议池的所有节点共用结果。
          </p>
          <Link
            href="/available-ips"
            className="mt-2 inline-block text-sm text-primary underline underline-offset-4"
          >
            查看可用 IP 与支持的协议
          </Link>
        </div>
        <NewProbeDialog
          nodes={nodes}
          networks={networks}
          defaults={data.items[0]?.input}
          active={active}
          onStarted={() => setVersion((value) => value + 1)}
        />
      </div>

      {error && (
        <p
          role="alert"
          className="rounded-lg border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900"
        >
          {error}。正在自动重试，已有记录已保留。
        </p>
      )}

      {active && (
        <ActiveBanner
          active={active}
          record={data.items.find(
            (item) =>
              item.job_id === active.job_id && item.round === active.round,
          )}
          stopping={stopping}
          onStop={async () => {
            setStopping(true);
            try {
              const result = await cancelScan(active.job_id);
              if (!result.ok) {
                toast.error(result.error);
                setStopping(false);
                return;
              }
              setVersion((value) => value + 1);
            } catch {
              toast.error("停止失败，请重试");
              setStopping(false);
            }
          }}
        />
      )}

      {data.items.length ? (
        <>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-sm text-muted-foreground">
              最近 {data.items.length} 条记录 · 保留 7 天
            </p>
            <Select
              value={protocol || "all"}
              onValueChange={(value) => {
                setProtocol(value === "all" ? "" : value);
                setPage(1);
              }}
            >
              <SelectTrigger className="w-44" aria-label="按协议筛选">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部协议</SelectItem>
                {protocols.map((value) => (
                  <SelectItem value={value} key={value}>
                    {protocolLabels[value] || value}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <RecordTable
            items={filtered.slice((current - 1) * 20, current * 20)}
            active={active}
            onRetried={() => setVersion((value) => value + 1)}
          />
          {pages > 1 && (
            <div className="flex items-center justify-end gap-3 text-sm">
              <Button
                variant="outline"
                size="sm"
                disabled={current === 1}
                onClick={() => setPage(current - 1)}
              >
                上一页
              </Button>
              <span className="text-muted-foreground tabular-nums">
                {current} / {pages}
              </span>
              <Button
                variant="outline"
                size="sm"
                disabled={current === pages}
                onClick={() => setPage(current + 1)}
              >
                下一页
              </Button>
            </div>
          )}
        </>
      ) : (
        <div className="rounded-xl border border-dashed bg-white/60 px-6 py-14 text-center">
          <History className="mx-auto size-8 text-slate-300" />
          <p className="mt-3 text-sm font-medium">还没有检测记录</p>
          <p className="mt-1 text-sm text-muted-foreground">
            点击「新建检测」选择节点与网段，第一轮完成后会在这里留下记录。
          </p>
        </div>
      )}
    </div>
  );
}

function ActiveBanner({
  active,
  record,
  stopping,
  onStop,
}: {
  active: ActiveScan;
  record?: ScanRecord;
  stopping: boolean;
  onStop: () => void;
}) {
  const running = active.status === "running";
  const done = active.total
    ? Math.round((active.completed / active.total) * 100)
    : 0;
  return (
    <div className="flex flex-col gap-3 rounded-xl border bg-white p-4 sm:flex-row sm:items-center sm:gap-4 sm:px-5">
      <div className="flex min-w-0 flex-1 items-start gap-3">
        <span
          className={`flex size-9 shrink-0 items-center justify-center rounded-full ${running ? "bg-blue-50 text-primary" : "bg-slate-100 text-slate-500"}`}
        >
          <Activity className={`size-4 ${running ? "animate-pulse" : ""}`} />
        </span>
        <div className="min-w-0 flex-1">
          <p className="flex flex-wrap items-center gap-2 text-sm font-medium">
            <span className="min-w-0 truncate">{active.node_name}</span>
            <span className="shrink-0">第 {active.round} 轮</span>
            <Badge variant="secondary" className="shrink-0">
              {statusText[active.status]}
            </Badge>
            {running && (
              <span className="font-normal text-muted-foreground tabular-nums">
                {active.completed} / {active.total}（{done}%）
              </span>
            )}
          </p>
          <p className="mt-1 truncate text-xs text-muted-foreground">
            {running
              ? `本轮可用 ${active.success} 个`
              : active.next_run_at
                ? `下一轮 ${formatTime(active.next_run_at)}`
                : "等待下一轮"}
            {active.interval_minutes
              ? ` · 自动巡检间隔 ${active.interval_minutes} 分钟`
              : ""}
          </p>
          {running && (
            <progress
              aria-label="检测进度"
              className="mt-2 h-1.5 w-full accent-blue-600"
              max={active.total || 1}
              value={active.completed}
            />
          )}
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-2 sm:justify-end">
        {record && (
          <Button variant="ghost" size="sm" asChild>
            <Link href={`/probes/${record.id}`}>查看本轮 →</Link>
          </Button>
        )}
        <Button variant="outline" size="sm" disabled={stopping} onClick={onStop}>
          {stopping ? "正在停止…" : active.interval_minutes ? "停止巡检" : "停止测试"}
        </Button>
      </div>
    </div>
  );
}

function RecordTable({
  items,
  active,
  onRetried,
}: {
  items: ScanRecord[];
  active: ActiveScan | null;
  onRetried: () => void;
}) {
  return (
    <>
      <div className="divide-y overflow-hidden rounded-xl border bg-white sm:hidden">
        {items.map((item) => {
          const rate = percent(item.success, item.completed);
          return (
            <div key={item.id} className="px-4 py-3">
              <Link
                href={`/probes/${item.id}`}
                className="block transition-colors active:bg-muted/50"
              >
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <p className="truncate font-medium">{item.node_name}</p>
                    <p className="mt-0.5 text-xs text-muted-foreground">
                      {protocolLabels[item.protocol] || item.protocol} · 第{" "}
                      {item.round} 轮 ·{" "}
                      {item.scope === "incremental" ? "增量" : item.scope === "available" ? "复测" : "全量"}
                    </p>
                  </div>
                  <Badge
                    variant="outline"
                    className={`shrink-0 ${item.status === "running" ? "text-primary" : ""}`}
                  >
                    {statusText[item.status] ?? item.status}
                  </Badge>
                </div>
                <dl className="mt-2.5 grid grid-cols-2 gap-x-3 gap-y-1 text-xs text-muted-foreground">
                  <div className="flex gap-1">
                    <dt>可用</dt>
                    <dd className="font-medium text-foreground tabular-nums">
                      {item.success} / {item.completed}
                    </dd>
                  </div>
                  <div className="flex gap-1">
                    <dt>可用率</dt>
                    <dd className={`tabular-nums ${availabilityTone(rate)}`}>
                      {rate == null ? "—" : `${rate}%`}
                    </dd>
                  </div>
                  <div className="flex gap-1">
                    <dt>开始</dt>
                    <dd className="tabular-nums">{formatTime(item.started_at)}</dd>
                  </div>
                  <div className="flex gap-1">
                    <dt>耗时</dt>
                    <dd className="tabular-nums">
                      {formatDuration(item.started_at, item.finished_at)}
                    </dd>
                  </div>
                </dl>
              </Link>
              {item.status === "interrupted" && (
                <div className="mt-3 flex justify-end">
                  <RetryScanButton
                    item={item}
                    disabled={active?.protocol === item.protocol}
                    onRetried={onRetried}
                  />
                </div>
              )}
            </div>
          );
        })}
      </div>
      <div className="hidden overflow-hidden rounded-xl border bg-white sm:block">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="pl-5">开始时间</TableHead>
              <TableHead>节点 / 协议</TableHead>
              <TableHead className="w-32">轮次</TableHead>
              <TableHead className="w-32">可用 / 已测</TableHead>
              <TableHead className="w-24">可用率</TableHead>
              <TableHead className="w-28">耗时</TableHead>
              <TableHead className="w-24">状态</TableHead>
              <TableHead className="w-24 pr-5 text-right">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((item) => {
              const rate = percent(item.success, item.completed);
              return (
                <TableRow key={item.id} className="group relative">
                  <TableCell className="pl-5 text-sm tabular-nums text-muted-foreground">
                    {formatTime(item.started_at)}
                  </TableCell>
                  <TableCell>
                    <Link
                      href={`/probes/${item.id}`}
                      className="font-medium after:absolute after:inset-0 after:content-[''] group-hover:text-primary"
                    >
                      {item.node_name}
                    </Link>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {protocolLabels[item.protocol] || item.protocol}
                    </p>
                  </TableCell>
                  <TableCell className="text-sm text-muted-foreground">
                    第 {item.round} 轮 ·{" "}
                    {item.scope === "incremental" ? "增量" : item.scope === "available" ? "复测可用" : "全量"}
                  </TableCell>
                  <TableCell className="text-sm tabular-nums">
                    <span className="font-medium text-emerald-700">
                      {item.success}
                    </span>
                    <span className="text-muted-foreground">
                      {" "}
                      / {item.completed}
                    </span>
                    {item.completed < item.total && (
                      <p className="mt-1 text-[11px] text-muted-foreground">
                        共 {item.total} 个
                      </p>
                    )}
                  </TableCell>
                  <TableCell>
                    {rate == null ? (
                      <span className="text-sm text-muted-foreground">—</span>
                    ) : (
                      <span
                        className={`text-sm tabular-nums ${availabilityTone(rate)}`}
                      >
                        {rate}%
                      </span>
                    )}
                  </TableCell>
                  <TableCell className="text-sm tabular-nums text-muted-foreground">
                    <span className="inline-flex items-center gap-1">
                      <Clock className="size-3.5" />
                      {formatDuration(item.started_at, item.finished_at)}
                    </span>
                  </TableCell>
                  <TableCell>
                    <Badge
                      variant="outline"
                      className={
                        item.status === "running" ? "text-primary" : undefined
                      }
                    >
                      {statusText[item.status] ?? item.status}
                    </Badge>
                  </TableCell>
                  <TableCell className="pr-5 text-right">
                    <span className="relative z-10 inline-flex items-center gap-2">
                      {item.status === "interrupted" && (
                        <RetryScanButton
                          item={item}
                          disabled={active?.protocol === item.protocol}
                          onRetried={onRetried}
                        />
                      )}
                      <Link
                        href={`/probes/${item.id}`}
                        className="inline-flex items-center gap-1 text-sm text-primary hover:underline"
                      >
                        查看
                        <ArrowRight className="size-3.5" />
                      </Link>
                    </span>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </div>
    </>
  );
}

function RetryScanButton({
  item,
  disabled,
  onRetried,
}: {
  item: ScanRecord;
  disabled: boolean;
  onRetried: () => void;
}) {
  const [pending, startTransition] = useTransition();
  return (
    <Button
      type="button"
      variant="outline"
      size="sm"
      className="relative z-10"
      disabled={disabled || pending}
      title={disabled ? "同一协议已有检测任务运行" : undefined}
      onClick={() => {
        startTransition(async () => {
          const result = await startScan({
            ...item.input,
            interval_minutes: 0,
            incremental: false,
          });
          if (!result.ok) {
            toast.error(result.error);
            return;
          }
          toast.success("已重新开始全量检测");
          onRetried();
        });
      }}
    >
      {pending ? <Loader2 className="animate-spin" /> : <RotateCcw />}
      重试
    </Button>
  );
}
