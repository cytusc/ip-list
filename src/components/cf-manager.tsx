"use client";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState, useTransition } from "react";
import { toast } from "sonner";
import {
  createCFTarget,
  deleteCFTarget,
  startCFScan,
  cancelScan,
} from "@/app/actions";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableHeader,
  TableHead,
  TableBody,
  TableRow,
  TableCell,
} from "@/components/ui/table";
import type { CFTarget, CFDetail, Network } from "@/lib/types";

export function CFTargetList({ items }: { items: CFTarget[] }) {
  const router = useRouter();
  const [pending, start] = useTransition();
  const [error, setError] = useState("");
  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold">CF 优选</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          添加目标网站，从已配置网段中筛选 HTTPS 可用 IP。
        </p>
      </div>
      <form
        className="grid gap-4 rounded-xl border bg-white p-5 sm:grid-cols-2"
        onSubmit={(e) => {
          e.preventDefault();
          const form = new FormData(e.currentTarget);
          setError("");
          start(async () => {
            try {
              const result = await createCFTarget({
                name: String(form.get("name")),
                url: String(form.get("url")),
              });
              if (!result.ok) {
                setError(result.error);
                return;
              }
              router.push(`/cf/${result.id}`);
            } catch {
              setError("添加失败，请重试");
            }
          });
        }}
      >
        <div className="space-y-2">
          <Label htmlFor="cf-name">名称</Label>
          <Input
            id="cf-name"
            name="name"
            placeholder="订阅站点"
            maxLength={100}
            required
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="cf-url">HTTPS 地址</Label>
          <Input
            id="cf-url"
            name="url"
            type="url"
            defaultValue="https://sub.bycn.de/"
            maxLength={2048}
            required
          />
        </div>
        {error && (
          <p role="alert" className="text-sm text-destructive sm:col-span-2">
            {error}
          </p>
        )}
        <Button className="w-fit" disabled={pending} type="submit">
          {pending ? "添加中…" : "添加地址"}
        </Button>
      </form>
      <div className="grid gap-4 lg:grid-cols-2">
        {items.map((item) => (
          <Link
            key={item.id}
            href={`/cf/${item.id}`}
            className="rounded-xl border bg-white p-5 transition-colors hover:border-primary focus-visible:outline-primary"
          >
            <div className="flex items-center justify-between gap-3">
              <h2 className="font-medium">{item.name}</h2>
              <Badge variant="secondary">
                {item.available_count ?? 0} 个可用 IP
              </Badge>
            </div>
            <p className="mt-3 break-all text-sm text-muted-foreground">
              {item.url}
            </p>
            <p className="mt-4 text-sm text-primary">查看详情与检测 →</p>
          </Link>
        ))}
      </div>
      {!items.length && (
        <p className="rounded-xl border border-dashed p-10 text-center text-sm text-muted-foreground">
          还没有目标地址，添加后即可选择网段检测。
        </p>
      )}
    </div>
  );
}

const statusText = {
  running: "检测中",
  completed: "检测完成",
  cancelled: "已停止",
  interrupted: "已中断",
  waiting: "等待中",
};
export function CFDetailView({
  initial,
  networks,
}: {
  initial: CFDetail;
  networks: Network[];
}) {
  const router = useRouter();
  const [detail, setDetail] = useState(initial);
  const [selected, setSelected] = useState<string[]>(
    initial.job?.input?.network_ids ?? [],
  );
  const [timeout, setTimeoutValue] = useState(
    initial.job?.input?.timeout_ms ?? 5000,
  );
  const [concurrency, setConcurrency] = useState(
    initial.job?.input?.concurrency ?? 16,
  );
  const [error, setError] = useState("");
  const [pending, start] = useTransition();
  const [stopping, setStopping] = useState(false);
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(1);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const active = detail.job?.status === "running";
  const id = detail.target.id;
  useEffect(() => {
    let disposed = false;
    const controller = new AbortController();
    const refresh = async () => {
      if (document.hidden) return;
      try {
        const response = await fetch(`/api/cf/${id}`, {
          cache: "no-store",
          signal: controller.signal,
        });
        const data = await response.json();
        if (!response.ok) throw new Error(data.error);
        if (!disposed) {
          setDetail(data);
          setError("");
          if (data.job?.status !== "running") setStopping(false);
        }
      } catch (e) {
        if (!disposed)
          setError(e instanceof Error ? e.message : "刷新失败，请重试");
      }
    };
    const timer = window.setInterval(refresh, active ? 1500 : 10000);
    return () => {
      disposed = true;
      controller.abort();
      window.clearInterval(timer);
    };
  }, [id, active]);
  const items = detail.items.filter((item) => item.ip.includes(search.trim()));
  const pages = Math.max(1, Math.ceil(items.length / 20));
  const current = Math.min(page, pages);
  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      toast.success("已复制 IP");
    } catch {
      toast.error("复制失败，请手动复制");
    }
  }
  return (
    <div className="space-y-6">
      <Link href="/cf" className="text-sm text-primary">
        ← 返回 CF 优选
      </Link>
      <div>
        <h1 className="text-2xl font-semibold">{detail.target.name}</h1>
        <p className="mt-2 break-all text-sm text-muted-foreground">
          {detail.target.url}
        </p>
      </div>
      <form
        className="space-y-5 rounded-xl border bg-white p-5"
        onSubmit={(e) => {
          e.preventDefault();
          setError("");
          start(async () => {
            try {
              const result = await startCFScan(id, {
                network_ids: selected,
                timeout_ms: timeout,
                concurrency,
              });
              if (!result.ok) {
                setError(result.error);
                return;
              }
              setDetail((old) => ({ ...old, job: result.job }));
              setStopping(false);
            } catch {
              setError("启动失败，请重试");
            }
          });
        }}
      >
        <fieldset disabled={active || pending} className="min-w-0 space-y-3">
          <legend className="mb-3 text-sm font-medium">检测网段</legend>
          <div className="max-h-60 min-w-0 overflow-y-auto rounded-lg border p-3">
            <div className="grid auto-rows-max gap-2 sm:grid-cols-2">
              {networks
                .filter((n) => n.enabled)
                .map((n) => (
                  <label
                    key={n.id}
                    className="flex min-w-0 cursor-pointer items-start gap-3 rounded-md border p-3 text-sm hover:bg-muted/50 has-checked:border-primary/40 has-checked:bg-primary/5 has-disabled:cursor-not-allowed has-disabled:opacity-60"
                  >
                    <input
                      className="mt-0.5 size-4 shrink-0 accent-primary"
                      type="checkbox"
                      checked={selected.includes(n.id)}
                      onChange={(e) =>
                        setSelected((old) =>
                          e.target.checked
                            ? [...old, n.id]
                            : old.filter((v) => v !== n.id),
                        )
                      }
                    />
                    <span className="min-w-0 flex-1 [overflow-wrap:anywhere]">
                      {n.name}
                      <span className="block font-mono text-xs text-muted-foreground">
                        {n.cidr}
                      </span>
                    </span>
                  </label>
                ))}
            </div>
          </div>
          {!networks.some((n) => n.enabled) && (
            <p className="text-sm text-muted-foreground">
              暂无启用网段，请先到
              <Link className="text-primary" href="/networks">
                网段配置
              </Link>
              添加。
            </p>
          )}
          <p className="text-xs text-muted-foreground">
            已选 {selected.length} 个网段，每次最多检测 4096 个去重 IP。
          </p>
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="cf-timeout">超时（毫秒）</Label>
              <Input
                id="cf-timeout"
                type="number"
                min={1000}
                max={15000}
                required
                value={timeout}
                onChange={(e) => setTimeoutValue(Number(e.target.value))}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="cf-concurrency">并发数</Label>
              <Input
                id="cf-concurrency"
                type="number"
                min={1}
                max={32}
                required
                value={concurrency}
                onChange={(e) => setConcurrency(Number(e.target.value))}
              />
            </div>
          </div>
        </fieldset>
        <p className="text-xs leading-5 text-muted-foreground">
          保留目标域名与证书校验，通过候选 IP 发起 HTTPS 请求。HTTP 2xx / 3xx
          计为可用，不跟随跳转。仅检测可用性。
        </p>
        <div className="flex flex-wrap gap-3">
          <Button
            type="submit"
            disabled={active || pending || !selected.length}
          >
            {pending ? "处理中…" : "开始检测"}
          </Button>
          {active && (
            <Button
              type="button"
              variant="outline"
              disabled={pending || stopping}
              onClick={() =>
                start(async () => {
                  try {
                    const result = await cancelScan(detail.job!.id);
                    if (!result.ok) setError(result.error);
                    else setStopping(true);
                  } catch {
                    setError("停止失败，请重试");
                  }
                })
              }
            >
              {stopping ? "正在停止…" : "停止检测"}
            </Button>
          )}
        </div>
      </form>
      {error && (
        <p
          role="alert"
          className="rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive"
        >
          {error}
        </p>
      )}
      {detail.job && (
        <div className="space-y-3 rounded-xl border bg-white p-5">
          <div className="flex flex-wrap justify-between gap-2 text-sm">
            <span role="status">
              {statusText[detail.job.status]} · {detail.job.completed} /{" "}
              {detail.job.total}
            </span>
            <span>
              本轮可用 {detail.job.success} · 失败{" "}
              {detail.job.completed - detail.job.success}
            </span>
          </div>
          <progress
            aria-label="检测进度"
            className="h-2 w-full accent-primary"
            max={detail.job.total || 1}
            value={detail.job.completed}
          />
          {detail.job.error && (
            <p className="text-sm text-destructive">{detail.job.error}</p>
          )}
        </div>
      )}
      <section className="space-y-4 rounded-xl border bg-white p-5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 className="font-semibold">
            可用 IP <Badge variant="secondary">{detail.items.length}</Badge>
          </h2>
          <Button
            variant="outline"
            disabled={!items.length}
            onClick={() => copy(items.map((i) => i.ip).join("\n"))}
          >
            复制筛选结果
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          按 IP 地址排序。每轮结束后更新；复测失败的 IP 会移除，未检测的 IP
          保留上次结果。
        </p>
        <Input
          aria-label="搜索 IP"
          placeholder="搜索 IP"
          value={search}
          onChange={(e) => {
            setSearch(e.target.value);
            setPage(1);
          }}
        />
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>IP 地址</TableHead>
              <TableHead>ASN / 服务商</TableHead>
              <TableHead>HTTP</TableHead>
              <TableHead>检测时间</TableHead>
              <TableHead>操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.slice((current - 1) * 20, current * 20).map((item) => (
              <TableRow key={item.ip}>
                <TableCell className="max-w-52 break-all whitespace-normal font-mono">
                  {item.ip}
                </TableCell>
                <TableCell className="max-w-44 whitespace-normal text-xs">
                  <p className="font-mono">{item.asn || "—"}</p>
                  <p className="mt-1 break-words text-muted-foreground">
                    {item.provider || "未填写"}
                  </p>
                </TableCell>
                <TableCell>{item.http_status}</TableCell>
                <TableCell>
                  {new Date(item.checked_at).toLocaleString("zh-CN")}
                </TableCell>
                <TableCell>
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
        {!items.length && (
          <p className="py-8 text-center text-sm text-muted-foreground">
            {search
              ? "没有匹配的 IP"
              : active
                ? "正在检测，结果将在本轮结束后保存。"
                : "暂无可用 IP，请选择网段开始检测。"}
          </p>
        )}
        {pages > 1 && (
          <div className="flex items-center justify-end gap-3 text-sm">
            <Button
              variant="outline"
              disabled={current === 1}
              onClick={() => setPage(current - 1)}
            >
              上一页
            </Button>
            {current} / {pages}
            <Button
              variant="outline"
              disabled={current === pages}
              onClick={() => setPage(current + 1)}
            >
              下一页
            </Button>
          </div>
        )}
      </section>
      <div className="flex flex-wrap items-center gap-3">
        {confirmDelete ? (
          <>
            <p className="text-sm">确定删除此地址及其检测结果？</p>
            <Button
              variant="destructive"
              disabled={pending || active}
              onClick={() =>
                start(async () => {
                  try {
                    const result = await deleteCFTarget(id);
                    if (!result.ok) setError(result.error);
                    else {
                      router.push("/cf");
                      router.refresh();
                    }
                  } catch {
                    setError("删除失败，请重试");
                  }
                })
              }
            >
              确认删除
            </Button>
            <Button variant="outline" onClick={() => setConfirmDelete(false)}>
              取消
            </Button>
          </>
        ) : (
          <Button
            variant="ghost"
            disabled={active || pending}
            onClick={() => setConfirmDelete(true)}
          >
            删除地址
          </Button>
        )}
      </div>
    </div>
  );
}
