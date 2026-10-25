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
import { NetworkSelector } from "@/components/network-selector";
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
import {
  Dialog,
  DialogTrigger,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import {
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@/components/ui/select";
import type { CFTarget, CFDetail, Network, ScanJob } from "@/lib/types";
import { networkAddressCount } from "@/lib/network";

export function CFTargetList({
  items,
  networks,
  initial,
}: {
  items: CFTarget[];
  networks: Network[];
  initial: CFDetail | null;
}) {
  const router = useRouter();
  const [pending, start] = useTransition();
  const [error, setError] = useState("");
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="w-full min-w-0 sm:min-w-64 sm:flex-1">
          <h1 className="text-2xl font-semibold">CF 优选</h1>
          <p className="mt-2 text-sm text-muted-foreground">
            配置检测域名，从已配置网段中筛选 HTTPS 可用
            IP。所有域名共用检测结果，新增域名可直接使用已有 IP 池。
          </p>
        </div>
        <CFScanConfig items={items} networks={networks} initial={initial} />
      </div>
      <form
        className="grid items-end gap-3 rounded-xl border bg-white p-4 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)_auto]"
        onSubmit={(e) => {
          e.preventDefault();
          const element = e.currentTarget;
          const form = new FormData(element);
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
              element.reset();
              router.refresh();
              toast.success("已添加域名，可直接使用共用 IP 池");
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
          <p
            role="alert"
            className="order-last text-sm text-destructive sm:col-span-3"
          >
            {error}
          </p>
        )}
        <Button className="w-fit" disabled={pending} type="submit">
          {pending ? "添加中…" : "添加地址"}
        </Button>
      </form>
      <div className="divide-y overflow-hidden rounded-xl border bg-white">
        {items.map((item) => (
          <Link
            key={item.id}
            href={`/cf/${item.id}`}
            className="grid gap-2 px-4 py-3 sm:flex sm:flex-wrap sm:items-center sm:justify-between sm:gap-x-6 transition-colors hover:bg-muted/50 focus-visible:outline-primary"
          >
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <h2 className="font-medium">{item.name}</h2>
              <Badge variant="secondary">
                共用 {item.available_count ?? 0} 个 IP
              </Badge>
            </div>
            <p className="min-w-0 flex-1 break-all text-sm text-muted-foreground">
              {item.url}
            </p>
            <span className="text-sm text-primary">查看 IP →</span>
          </Link>
        ))}
      </div>
      {!items.length && (
        <p className="rounded-xl border border-dashed p-6 text-center text-sm text-muted-foreground">
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
function ScanProgress({ job }: { job: ScanJob | null }) {
  return (
    <>
      {job && (
        <div className="space-y-3 rounded-xl border bg-white p-5">
          <p className="break-all text-xs text-muted-foreground">
            共用检测任务 · 检测域名：{job.url}
          </p>
          <div className="flex flex-wrap justify-between gap-2 text-sm">
            <span role="status">
              {statusText[job.status]} · {job.completed} / {job.total}
            </span>
            <span>
              本轮可用 {job.success} · 失败 {job.completed - job.success}
            </span>
          </div>
          <progress
            aria-label="检测进度"
            className="h-2 w-full accent-primary"
            max={job.total || 1}
            value={job.completed}
          />
          {job.error && <p className="text-sm text-destructive">{job.error}</p>}
        </div>
      )}
    </>
  );
}

function CFScanConfig({
  items,
  networks,
  initial,
}: {
  items: CFTarget[];
  networks: Network[];
  initial: CFDetail | null;
}) {
  const router = useRouter();
  const [job, setJob] = useState<ScanJob | null>(initial?.job ?? null);
  const [id, setID] = useState(
    items.find((item) => item.url === initial?.job?.url)?.id ??
      items[0]?.id ??
      "",
  );
  const targetID = items.some((item) => item.id === id)
    ? id
    : (items[0]?.id ?? "");
  const [selected, setSelected] = useState<string[]>(
    initial?.job?.input?.network_ids ?? [],
  );
  const [timeout, setTimeoutValue] = useState(
    initial?.job?.input?.timeout_ms ?? 5000,
  );
  const [concurrency, setConcurrency] = useState(
    initial?.job?.input?.concurrency ?? 16,
  );
  const [pending, start] = useTransition();
  const [stopping, setStopping] = useState(false);
  const [error, setError] = useState("");
  const active = job?.status === "running";
  const selectedNetworks = networks.filter(
    (network) => network.enabled && selected.includes(network.id),
  );
  const addressUpperCount = selectedNetworks.reduce(
    (sum, network) => sum + networkAddressCount(network.cidr),
    BigInt(0),
  );
  useEffect(() => {
    if (!targetID) return;
    const controller = new AbortController();
    let disposed = false;
    const refresh = async () => {
      if (document.hidden) return;
      try {
        const response = await fetch(`/api/cf/${targetID}`, {
          cache: "no-store",
          signal: controller.signal,
        });
        const data = await response.json();
        if (!response.ok) throw new Error(data.error);
        if (!disposed) {
          setJob(data.job);
          if (data.job?.status !== "running") {
            setStopping(false);
            if (active) router.refresh();
          }
        }
      } catch (error) {
        if (!disposed)
          setError(error instanceof Error ? error.message : "刷新失败，请重试");
      }
    };
    void refresh();
    const timer = window.setInterval(refresh, active ? 1500 : 10000);
    return () => {
      disposed = true;
      controller.abort();
      window.clearInterval(timer);
    };
  }, [targetID, active, router]);
  return (
    <div className="shrink-0 space-y-2">
      <Dialog>
        <DialogTrigger asChild>
          <Button variant="outline">网段与检测配置</Button>
        </DialogTrigger>
        <DialogContent className="max-h-[90dvh] overflow-x-hidden overflow-y-auto [&>*]:min-w-0 sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>统一检测配置</DialogTitle>
            <DialogDescription>
              配置一次，所有域名共用筛选出的可用 IP。新增域名无需重新检测。
            </DialogDescription>
          </DialogHeader>
          {!items.length && (
            <p className="text-sm text-muted-foreground">
              请先在列表添加一个检测域名。
            </p>
          )}
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault();
              setError("");
              start(async () => {
                try {
                  const result = await startCFScan(targetID, {
                    network_ids: selected,
                    timeout_ms: timeout,
                    concurrency,
                  });
                  if (!result.ok) {
                    setError(result.error);
                    return;
                  }
                  setJob(result.job);
                  setStopping(false);
                } catch {
                  setError("启动失败，请重试");
                }
              });
            }}
          >
            <fieldset
              disabled={active || pending || !items.length}
              className="min-w-0 space-y-3"
            >
              <div className="space-y-2">
                <Label htmlFor="cf-target">检测域名</Label>
                <Select
                  value={targetID}
                  onValueChange={setID}
                  disabled={active || pending}
                >
                  <SelectTrigger
                    id="cf-target"
                    className="w-full min-w-0 [&_[data-slot=select-value]]:truncate [&_[data-slot=select-value]]:block"
                  >
                    <SelectValue placeholder="请先添加域名" />
                  </SelectTrigger>
                  <SelectContent>
                    {items.map((item) => (
                      <SelectItem key={item.id} value={item.id}>
                        {item.name} · {item.url}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <p className="text-sm font-medium">检测网段</p>
              <NetworkSelector
                networks={networks}
                value={selected}
                onChange={setSelected}
                disabled={active || pending}
              />
              <p className="text-xs text-muted-foreground">
                已选 {selectedNetworks.length} 个网段，地址总数上限{" "}
                {addressUpperCount.toString()}
                （重叠地址自动去重）。检测覆盖所选网段的全部地址。
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
              使用所选域名检测候选 IP，保留域名与证书校验。HTTP 2xx / 3xx
              计为可用，不跟随跳转。结果更新至所有域名共用的 IP 池。
            </p>
            <div className="flex flex-wrap gap-3">
              <Button
                type="submit"
                disabled={active || pending || !selected.length || !targetID}
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
                        const result = await cancelScan(job!.id);
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
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
          <ScanProgress job={job} />
        </DialogContent>
      </Dialog>
      {job && (
        <p role="status" className="text-xs text-muted-foreground">
          共用检测：{statusText[job.status]} · {job.completed} / {job.total}
        </p>
      )}
    </div>
  );
}

export function CFDetailView({ initial }: { initial: CFDetail }) {
  const router = useRouter();
  const [detail, setDetail] = useState(initial);
  const [error, setError] = useState("");
  const [pending, start] = useTransition();
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
    <div className="space-y-4">
      <Link href="/cf" className="text-sm text-primary">
        ← 返回 CF 优选 · 统一检测配置
      </Link>
      <div>
        <h1 className="text-2xl font-semibold">{detail.target.name}</h1>
        <p className="mt-2 break-all text-sm text-muted-foreground">
          {detail.target.url}
        </p>
      </div>
      {error && (
        <p
          role="alert"
          className="rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive"
        >
          {error}
        </p>
      )}
      <ScanProgress job={detail.job} />
      <section className="space-y-4 rounded-xl border bg-white p-5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 className="font-semibold">
            共用可用 IP <Badge variant="secondary">{detail.items.length}</Badge>
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
          所有域名共用此 IP 池，新增域名无需重新检测。按 IP
          地址排序，每轮结束后更新；复测失败的 IP 会移除，未检测的 IP
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
                : "暂无可用 IP，请返回 CF 优选统一配置并开始检测。"}
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
            <p className="text-sm">确定删除此地址？共用 IP 池将保留。</p>
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
