"use client";
import Link from "next/link";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import { Activity, Copy, Search } from "lucide-react";
import { cancelScan } from "@/app/actions";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableHeader,
  TableHead,
  TableBody,
  TableRow,
  TableCell,
} from "@/components/ui/table";
import type { CFIP, ScanJob } from "@/lib/types";
import { ListPagination } from "@/components/list-pagination";
import { usePagedList } from "@/components/use-paged-list";
import type { PageResult } from "@/lib/pagination";
import { formatDuration, formatTime, scanStatusText } from "@/lib/cf";

const statusTone: Record<ScanJob["status"], string> = {
  running: "border-blue-200 bg-blue-50 text-blue-700",
  completed: "border-emerald-200 bg-emerald-50 text-emerald-700",
  cancelled: "border-slate-200 bg-slate-50 text-slate-600",
  interrupted: "border-amber-200 bg-amber-50 text-amber-800",
  waiting: "border-slate-200 bg-slate-50 text-slate-600",
};

export function CFProgressView({
  initial,
  pool,
}: {
  initial: ScanJob | null;
  pool: PageResult<CFIP>;
}) {
  const [job, setJob] = useState(initial);
  const [error, setError] = useState("");
  const [stopping, setStopping] = useState(false);
  const status = job?.status;
  useEffect(() => {
    let disposed = false;
    const controller = new AbortController();
    const load = async () => {
      if (document.hidden) return;
      try {
        const response = await fetch("/api/cf/scan", {
          cache: "no-store",
          signal: controller.signal,
        });
        const data = await response.json();
        if (!response.ok) throw new Error(data.error || "读取进度失败");
        if (disposed) return;
        setJob(data);
        setError("");
        if (data?.status !== "running") setStopping(false);
      } catch (e) {
        if (!disposed && !(e instanceof DOMException && e.name === "AbortError"))
          setError(e instanceof Error ? e.message : "读取进度失败");
      }
    };
    const timer = window.setInterval(load, status === "running" ? 1500 : 10000);
    return () => {
      disposed = true;
      controller.abort();
      window.clearInterval(timer);
    };
  }, [status]);

  const running = job?.status === "running";
  const done = job?.total
    ? Math.round((job.completed / job.total) * 100)
    : 0;
  const failed = job ? job.completed - job.success : 0;

  return (
    <div className="space-y-6">
      <div>
        <Link
          href="/cf"
          className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-primary"
        >
          ← 返回 CF 优选
        </Link>
        <div className="mt-3 flex flex-wrap items-start justify-between gap-4">
          <div className="min-w-0">
            <h1 className="text-2xl font-semibold">统一检测进度</h1>
            <p className="mt-2 max-w-2xl text-sm leading-6 text-muted-foreground">
              CF 优选只运行一个共享检测任务：按所选网段逐个请求 HTTPS
              地址，结果汇入所有域名共用的 IP 池。离开页面不会中断检测。
            </p>
          </div>
          {running && job && (
            <Button
              variant="outline"
              disabled={stopping}
              onClick={async () => {
                setStopping(true);
                try {
                  const result = await cancelScan(job.id);
                  if (!result.ok) {
                    toast.error(result.error);
                    setStopping(false);
                  } else {
                    toast.success("正在停止检测");
                  }
                } catch {
                  toast.error("停止失败，请重试");
                  setStopping(false);
                }
              }}
            >
              {stopping ? "正在停止…" : "停止检测"}
            </Button>
          )}
        </div>
      </div>

      {error && (
        <p
          role="alert"
          className="rounded-lg border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900"
        >
          {error}。正在自动重试，已保存的结果不受影响。
        </p>
      )}

      {job ? (
        <section className="space-y-5 rounded-xl border bg-white p-5 sm:p-6">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="min-w-0">
              <p className="flex flex-wrap items-center gap-2 text-sm font-medium">
                <span
                  className={`inline-flex items-center rounded-full border px-2.5 py-0.5 text-xs ${statusTone[job.status]}`}
                >
                  {running && (
                    <span className="mr-1.5 size-1.5 animate-pulse rounded-full bg-current" />
                  )}
                  {scanStatusText[job.status]}
                </span>
                <span className="break-all">{job.url}</span>
              </p>
              <p className="mt-2 text-xs text-muted-foreground">
                开始于 {formatTime(job.started_at)}
                {job.finished_at && ` · 结束于 ${formatTime(job.finished_at)}`}
                {` · 用时 ${formatDuration(job.started_at, job.finished_at)}`}
              </p>
            </div>
            <p className="text-2xl font-semibold tabular-nums">
              {job.completed}
              <span className="text-base font-normal text-muted-foreground">
                {" "}
                / {job.total}
              </span>
            </p>
          </div>

          <progress
            aria-label="统一检测进度"
            className="h-2 w-full accent-blue-600"
            max={job.total || 1}
            value={job.completed}
          />

          <div className="grid grid-cols-2 gap-3 xl:grid-cols-4">
            <Stat label="已检测" value={String(job.completed)} hint={`${done}%`} />
            <Stat
              label="本轮可用"
              value={String(job.success)}
              hint="写入共用 IP 池"
              tone="text-emerald-700"
            />
            <Stat
              label="本轮失败"
              value={String(failed)}
              hint="HTTP 错误、证书或超时"
              tone="text-slate-600"
            />
            <Stat
              label="未检测"
              value={String(Math.max(0, job.total - job.completed))}
              hint={running ? "检测仍在进行" : "本轮未覆盖"}
            />
          </div>

          <div className="grid gap-2 rounded-lg bg-slate-50 p-4 text-xs text-muted-foreground sm:grid-cols-3">
            <p>
              {job.scope === "incremental" ? "增量检测" : "全量检测"} · 网段 <span className="tabular-nums">{job.cidrs.length}</span>{" "}
              个
            </p>
            <p>
              超时{" "}
              <span className="tabular-nums">{job.input?.timeout_ms ?? "—"}</span>{" "}
              毫秒
            </p>
            <p>
              并发{" "}
              <span className="tabular-nums">{job.input?.concurrency ?? "—"}</span>
            </p>
            {!!job.cidrs.length && (
              <p className="break-all sm:col-span-3">{job.cidrs.join("、")}</p>
            )}
          </div>

          {job.error && (
            <p role="alert" className="text-sm text-destructive">
              {job.error}
            </p>
          )}
          <p className="text-xs leading-5 text-muted-foreground">
            进度快照保存在 Redis 中 24 小时；检测结果按轮次写入数据库，长期保留。
          </p>
        </section>
      ) : (
        <div className="rounded-xl border border-dashed bg-white/60 px-6 py-14 text-center">
          <Activity className="mx-auto size-8 text-slate-300" />
          <p className="mt-3 text-sm font-medium">还没有检测记录</p>
          <p className="mt-1 text-sm text-muted-foreground">
            回到 CF 优选，通过「网段与检测配置」开始一次统一检测。
          </p>
          <Button className="mt-4" variant="outline" asChild>
            <Link href="/cf">返回 CF 优选</Link>
          </Button>
        </div>
      )}

      <PoolTable pool={pool} />
    </div>
  );
}

function PoolTable({ pool }: { pool: PageResult<CFIP> }) {
  const [search, setSearch] = useState("");
  const {data,setPage,loading,error}=usePagedList("cf-pool",pool,{search},10000);
  const items=data.items;
  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      toast.success("已复制 IP");
    } catch {
      toast.error("复制失败，请手动复制");
    }
  }
  return (
    <section className="space-y-4 rounded-xl border bg-white p-5 sm:p-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="flex items-center gap-2 font-semibold">
            共用 IP 池
            <Badge variant="secondary">{data.total}</Badge>
          </h2>
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
          <p className="mt-1 text-xs leading-5 text-muted-foreground">
            所有域名通用，每轮检测结束后更新；复测失败的 IP 会移除，未检测到的
            IP 保留上次结果。
          </p>
        </div>
        <Button
          variant="outline"
          disabled={!items.length}
          onClick={() => copy(items.map((item) => item.ip).join("\n"))}
        >
          <Copy />
          复制当前页 IP
        </Button>
      </div>
      <div className="relative max-w-sm">
        <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          aria-label="搜索 IP"
          className="pl-9"
          placeholder="搜索 IP"
          value={search}
          onChange={(event) => {
            setSearch(event.target.value);
            setPage(1);
          }}
        />
      </div>
      {items.length ? (
        <>
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>IP 地址</TableHead>
                <TableHead className="hidden sm:table-cell">
                  ASN / 服务商
                </TableHead>
                <TableHead className="w-16">HTTP</TableHead>
                <TableHead className="hidden w-44 md:table-cell">
                  最近检测
                </TableHead>
                <TableHead className="w-16 text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((item) => (
                <TableRow key={item.ip}>
                  <TableCell className="max-w-52 whitespace-normal">
                    <p className="font-mono break-all">{item.ip}</p>
                    <p className="mt-1 text-[11px] break-words text-muted-foreground sm:hidden">
                      {item.asn || "—"} · {item.provider || "未填写"}
                      <span className="block">
                        {formatTime(item.checked_at)}
                      </span>
                    </p>
                  </TableCell>
                  <TableCell className="hidden max-w-44 whitespace-normal text-xs sm:table-cell">
                    <p className="font-mono">{item.asn || "—"}</p>
                    <p className="mt-1 break-words text-muted-foreground">
                      {item.provider || "未填写"}
                    </p>
                  </TableCell>
                  <TableCell className="tabular-nums">
                    {item.http_status}
                  </TableCell>
                  <TableCell className="hidden text-sm tabular-nums text-muted-foreground md:table-cell">
                    {formatTime(item.checked_at)}
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      variant="ghost"
                      size="sm"
                      aria-label={`复制 ${item.ip}`}
                      onClick={() => copy(item.ip)}
                    >
                      复制
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <ListPagination data={data} onPage={setPage} loading={loading}/>
        </>
      ) : (
        <p className="py-10 text-center text-sm text-muted-foreground">
          {search ? "没有匹配的 IP" : "共用 IP 池为空，完成一轮检测后会自动填充。"}
        </p>
      )}
    </section>
  );
}

function Stat({
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
    <div className="rounded-lg border bg-white p-3.5">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className={`mt-1 text-xl font-semibold tabular-nums ${tone ?? ""}`}>
        {value}
      </p>
      <p className="mt-0.5 text-[11px] text-muted-foreground">{hint}</p>
    </div>
  );
}
