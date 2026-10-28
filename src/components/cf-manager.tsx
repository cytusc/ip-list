"use client";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useRef, useState, useTransition } from "react";
import { toast } from "sonner";
import {
  Activity,
  ArrowRight,
  Cloud,
  Copy,
  Plus,
  Search,
  Settings2,
} from "lucide-react";
import { createCFTarget, deleteCFTarget, startCFScan } from "@/app/actions";
import { ListPagination } from "@/components/list-pagination";
import { usePagedList } from "@/components/use-paged-list";
import type { PageResult } from "@/lib/pagination";
import { NetworkSelector } from "@/components/network-selector";
import { useListOptions } from "@/components/use-list-options";
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
import type { CFDetail, CFIP, CFTarget, Network, ScanJob } from "@/lib/types";
import { networkAddressCount } from "@/lib/network";
import { availabilityTone, formatTime, percent, scanStatusText } from "@/lib/cf";

/** 统一检测任务由所有域名共用，列表与进度页都从同一接口读取。 */
function useCFScan() {
  const router = useRouter();
  const [job, setJob] = useState<ScanJob | null>(null);
  const [error, setError] = useState("");
  const [stopping, setStopping] = useState(false);
  const active = useRef(false);
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
        if (active.current && data?.status !== "running") router.refresh();
        active.current = data?.status === "running";
      } catch (e) {
        if (!disposed && !(e instanceof DOMException && e.name === "AbortError"))
          setError(e instanceof Error ? e.message : "读取进度失败");
      }
    };
    void load();
    const timer = window.setInterval(load, status === "running" ? 1500 : 8000);
    return () => {
      disposed = true;
      controller.abort();
      window.clearInterval(timer);
    };
  }, [status, router]);
  return { job, error, stopping, setStopping };
}

export function CFTargetList({
  initial,
  networks,
}: {
  initial: PageResult<CFTarget>;
  networks: Network[];
}) {
  const {data,setPage,loading,error}=usePagedList("cf-targets",initial);
  const items=data.items;
  const { job } = useCFScan();
  const running = job?.status === "running";
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0 sm:flex-1">
          <h1 className="text-2xl font-semibold">CF 优选</h1>
          <p className="mt-2 max-w-2xl text-sm leading-6 text-muted-foreground">
            配置检测域名，从已配置网段中筛选 HTTPS 可用 IP。所有域名共用同一份检测结果，新增域名无需重新检测。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" asChild>
            <Link href="/cf/progress">
              <Activity />
              检测进度
              {running && (
                <span className="ml-1 inline-flex items-center gap-1 text-xs text-primary">
                  <span className="size-1.5 animate-pulse rounded-full bg-primary" />
                  {job.completed}/{job.total}
                </span>
              )}
            </Link>
          </Button>
          <CFScanConfig items={items} networks={networks} job={job} />
          <AddDomain />
        </div>
      </div>

      {job && <ScanBanner job={job} />}
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}

      {items.length ? (
        <CFTargetTable items={items} />
      ) : (
        <div className="rounded-xl border border-dashed bg-white/60 px-6 py-14 text-center">
          <Cloud className="mx-auto size-8 text-slate-300" />
          <p className="mt-3 text-sm font-medium">还没有检测域名</p>
          <p className="mt-1 text-sm text-muted-foreground">
            添加一个 HTTPS 地址后，即可选择网段开始统一检测。
          </p>
        </div>
      )}
      <ListPagination data={data} onPage={setPage} loading={loading}/>
    </div>
  );
}

function CFTargetTable({ items }: { items: CFTarget[] }) {
  return (
    <>
      <div className="divide-y overflow-hidden rounded-xl border bg-white sm:hidden">
        {items.map((item) => (
          <Link
            key={item.id}
            href={`/cf/${item.id}`}
            className="block px-4 py-3 transition-colors active:bg-muted/50"
          >
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <p className="truncate font-medium">{item.name}</p>
                <p className="mt-0.5 truncate text-xs text-muted-foreground">
                  {item.url}
                </p>
              </div>
              <span className="shrink-0 text-sm text-primary">查看 IP →</span>
            </div>
            <dl className="mt-2.5 grid grid-cols-2 gap-x-3 gap-y-1 text-xs text-muted-foreground">
              <div className="flex gap-1">
                <dt>可用 IP</dt>
                <dd className="font-medium text-foreground tabular-nums">
                  {item.available_count ?? 0}
                </dd>
              </div>
              <div className="flex gap-1">
                <dt>上轮</dt>
                <dd
                  className={`tabular-nums ${availabilityTone(item.availability)}`}
                >
                  {item.availability == null ? "—" : `${item.availability}%`}
                </dd>
              </div>
              <div className="flex gap-1">
                <dt>最后检测</dt>
                <dd className="tabular-nums">
                  {item.last_checked_at
                    ? formatTime(item.last_checked_at)
                    : "未检测"}
                </dd>
              </div>
              <div className="flex gap-1">
                <dt>添加</dt>
                <dd className="tabular-nums">{formatTime(item.created_at)}</dd>
              </div>
            </dl>
          </Link>
        ))}
      </div>
      <div className="hidden overflow-hidden rounded-xl border bg-white sm:block">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="pl-5">域名</TableHead>
              <TableHead className="w-28">可用 IP</TableHead>
              <TableHead className="w-28">上轮可用率</TableHead>
              <TableHead className="w-44">最后检测</TableHead>
              <TableHead className="w-40">添加时间</TableHead>
              <TableHead className="w-28 pr-5 text-right">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((item) => (
              <TableRow key={item.id} className="group relative">
                <TableCell className="pl-5">
                  <Link
                    href={`/cf/${item.id}`}
                    className="font-medium after:absolute after:inset-0 after:content-[''] group-hover:text-primary"
                  >
                    {item.name}
                  </Link>
                  <p className="mt-1 max-w-[22rem] truncate text-xs text-muted-foreground">
                    {item.url}
                  </p>
                </TableCell>
                <TableCell>
                  <span className="text-sm font-medium tabular-nums">
                    {item.available_count ?? 0}
                  </span>
                  {!item.available_count && !!item.shared_count && (
                    <p className="mt-1 text-[11px] text-muted-foreground">
                      共用池 {item.shared_count}
                    </p>
                  )}
                </TableCell>
                <TableCell>
                  {item.availability == null ? (
                    <span className="text-sm text-muted-foreground">—</span>
                  ) : (
                    <span
                      className={`text-sm tabular-nums ${availabilityTone(item.availability)}`}
                    >
                      {item.availability}%
                    </span>
                  )}
                </TableCell>
                <TableCell className="text-sm tabular-nums text-muted-foreground">
                  {item.last_checked_at ? (
                    formatTime(item.last_checked_at)
                  ) : (
                    <span className="text-slate-400">未检测</span>
                  )}
                </TableCell>
                <TableCell className="text-sm tabular-nums text-muted-foreground">
                  {formatTime(item.created_at)}
                </TableCell>
                <TableCell className="pr-5 text-right">
                  <span className="inline-flex items-center gap-1 text-sm text-primary group-hover:underline">
                    查看 IP
                    <ArrowRight className="size-3.5" />
                  </span>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </>
  );
}

function ScanBanner({ job }: { job: ScanJob }) {
  const running = job.status === "running";
  const done = job.total ? Math.round((job.completed / job.total) * 100) : 0;
  return (
    <div className="flex flex-wrap items-center gap-4 rounded-xl border bg-white px-5 py-4">
      <span
        className={`flex size-9 shrink-0 items-center justify-center rounded-full ${running ? "bg-blue-50 text-primary" : "bg-slate-100 text-slate-500"}`}
      >
        <Activity className={`size-4 ${running ? "animate-pulse" : ""}`} />
      </span>
      <div className="min-w-0 flex-1">
        <p className="flex flex-wrap items-center gap-2 text-sm font-medium">
          统一检测 · {scanStatusText[job.status]}
          <span className="font-normal text-muted-foreground tabular-nums">
            {job.completed} / {job.total}（{done}%）
          </span>
        </p>
        <p className="mt-1 truncate text-xs text-muted-foreground">
          {job.url} · 本轮可用 {job.success} 个
          {job.error ? ` · ${job.error}` : ""}
        </p>
        {running && (
          <progress
            aria-label="统一检测进度"
            className="mt-2 h-1.5 w-full accent-blue-600"
            max={job.total || 1}
            value={job.completed}
          />
        )}
      </div>
      <Link
        href="/cf/progress"
        className="shrink-0 text-sm text-primary hover:underline"
      >
        查看进度 →
      </Link>
    </div>
  );
}

function AddDomain() {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [pending, start] = useTransition();
  const [error, setError] = useState("");
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button>
          <Plus />
          添加域名
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>添加检测域名</DialogTitle>
          <DialogDescription>
            域名只作为统一检测的样本，检测结果进入共用 IP 池，所有域名都可直接使用。
          </DialogDescription>
        </DialogHeader>
        <form
          className="space-y-4"
          onSubmit={(event) => {
            event.preventDefault();
            const form = new FormData(event.currentTarget);
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
                setOpen(false);
                router.refresh();
                toast.success("已添加域名");
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
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              variant="outline"
              onClick={() => setOpen(false)}
            >
              取消
            </Button>
            <Button type="submit" disabled={pending}>
              {pending ? "添加中…" : "添加地址"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function CFScanConfig({
  items,
  networks,
  job,
}: {
  items: CFTarget[];
  networks: Network[];
  job: ScanJob | null;
}) {
  const active = job?.status === "running";
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant="outline">
          <Settings2 />
          网段与检测配置
        </Button>
      </DialogTrigger>
      <DialogContent className="max-h-[90dvh] overflow-x-hidden overflow-y-auto [&>*]:min-w-0 sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>统一检测配置</DialogTitle>
          <DialogDescription>
            配置一次，所有域名共用筛选出的可用 IP。检测进度在独立页面查看。
          </DialogDescription>
        </DialogHeader>
        {active && (
          <p className="rounded-lg border border-blue-200 bg-blue-50 p-3 text-sm text-blue-900">
            已有统一检测正在运行，请先
            <Link href="/cf/progress" className="mx-1 underline">
              查看进度
            </Link>
            或停止后再开始新的检测。
          </p>
        )}
        {items.length ? (
          <ScanConfigForm items={items} networks={networks} job={job} />
        ) : (
          <p className="text-sm text-muted-foreground">
            请先在列表添加一个检测域名，再配置检测网段。
          </p>
        )}
      </DialogContent>
    </Dialog>
  );
}

/** 弹窗关闭即卸载，因此每次打开都会用上一轮检测的配置初始化表单。 */
function ScanConfigForm({
  items: initialItems,
  job,
}: {
  items: CFTarget[];
  networks: Network[];
  job: ScanJob | null;
}) {
  const router = useRouter();
  const targetOptions=useListOptions<CFTarget>("cf-targets");
  const networkOptions=useListOptions<Network>("networks",job?.input?.network_ids);
  const networks=networkOptions.items;
  const [targetID, setTargetID] = useState(
    () => initialItems.find((item) => item.url === job?.url)?.id ?? initialItems[0]?.id ?? "",
  );
  const items=[...targetOptions.data.items];
  const selectedTarget=[...targetOptions.items,...initialItems].find(item=>item.id===targetID);
  if(selectedTarget && !items.some(item=>item.id===targetID))items.push(selectedTarget);
  const [selected, setSelected] = useState<string[]>(() =>
    job?.input?.network_ids ?? [],
  );
  const [incremental, setIncremental] = useState(true);
  const [timeout, setTimeoutValue] = useState(() => job?.input?.timeout_ms ?? 5000);
  const [concurrency, setConcurrency] = useState(
    () => job?.input?.concurrency ?? 16,
  );
  const [pending, start] = useTransition();
  const [error, setError] = useState("");
  const active = job?.status === "running";
  const selectedNetworks = networks.filter(
    (network) => network.enabled && selected.includes(network.id),
  );
  const addressUpperCount = selectedNetworks.reduce(
    (sum, network) => sum + networkAddressCount(network.cidr),
    BigInt(0),
  );
  return (
    <form
      className="space-y-4"
      onSubmit={(event) => {
        event.preventDefault();
        setError("");
        start(async () => {
          try {
            const result = await startCFScan(targetID, {
              network_ids: selected,
              incremental,
              timeout_ms: timeout,
              concurrency,
            });
            if (!result.ok) {
              setError(result.error);
              return;
            }
            router.push("/cf/progress");
            router.refresh();
          } catch {
            setError("启动失败，请重试");
          }
        });
      }}
    >
      <fieldset disabled={active || pending} className="min-w-0 space-y-3">
        <div className="space-y-2">
          <Label htmlFor="cf-target">检测域名</Label>
          <Select
            value={targetID}
            onValueChange={setTargetID}
            disabled={active || pending}
          >
            <SelectTrigger
              id="cf-target"
              className="w-full min-w-0 [&_[data-slot=select-value]]:block [&_[data-slot=select-value]]:truncate"
            >
              <SelectValue placeholder="请先添加域名" />
            </SelectTrigger>
            <SelectContent>
              <Input aria-label="搜索检测域名" value={targetOptions.search} onChange={event=>targetOptions.setSearch(event.target.value)} placeholder="搜索域名" onKeyDown={event=>event.stopPropagation()}/>
              {items.map((item) => (
                <SelectItem key={item.id} value={item.id}>
                  {item.name} · {item.url}
                </SelectItem>
              ))}
              <ListPagination data={targetOptions.data} onPage={targetOptions.setPage} loading={targetOptions.loading}/>
            </SelectContent>
          </Select>
          <p className="text-xs leading-5 text-muted-foreground">
            该域名只作为检测样本，决定请求的 Host 与证书校验目标。
          </p>
        </div>
        <div className="space-y-2">
          <Label htmlFor="cf-mode">检测方式</Label>
          <Select value={incremental ? "incremental" : "full"} onValueChange={(value) => setIncremental(value === "incremental")} disabled={active || pending}>
            <SelectTrigger id="cf-mode" className="w-full"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="incremental">增量检测 · 仅检测新增 IP</SelectItem>
              <SelectItem value="full">全量检测 · 复测全部所选地址</SelectItem>
            </SelectContent>
          </Select>
          <p className="text-xs leading-5 text-muted-foreground">
            增量检测跳过共用池中已检测的 IP（含失败结果），补测新增和上次未完成的地址，保留已有结果。
          </p>
        </div>
        <p className="text-sm font-medium">检测网段</p>
        <NetworkSelector
          networks={networks}
          options={networkOptions}
          value={selected}
          onChange={setSelected}
          disabled={active || pending}
        />
        <p className="text-xs leading-5 text-muted-foreground">
          已选 {selectedNetworks.length} 个网段，地址总数上限{" "}
          {addressUpperCount.toString()}
          （重叠地址自动去重）。{incremental ? "实际仅检测其中未检测的地址。" : "检测覆盖所选网段的全部地址。"}
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
              onChange={(event) => setTimeoutValue(Number(event.target.value))}
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
              onChange={(event) => setConcurrency(Number(event.target.value))}
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
          {pending ? "处理中…" : incremental ? "开始增量检测" : "开始全量检测"}
        </Button>
        {active && (
          <Button type="button" variant="outline" asChild>
            <Link href="/cf/progress">前往检测进度</Link>
          </Button>
        )}
      </div>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
    </form>
  );
}

export function CFDetailView({ initial }: { initial: CFDetail & PageResult<CFIP> }) {
  const router = useRouter();
  const [search,setSearch]=useState("");
  const {data:detail,setPage,loading,error:loadError}=usePagedList("cf-detail",initial,{id:initial.target.id,search},15000);
  const [error,setError]=useState("");
  const id=initial.target.id;
  const [pending, start] = useTransition();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const {target,items,history}=detail as typeof initial;
  const filtered=items;
  const latest = history[0];
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
      <Link
        href="/cf"
        className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-primary"
      >
        ← 返回 CF 优选
      </Link>
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <h1 className="text-2xl font-semibold">{target.name}</h1>
          <p className="mt-2 break-all text-sm text-muted-foreground">
            {target.url}
          </p>
          <p className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
            <span>添加于 {formatTime(target.created_at)}</span>
            <span>最后检测 {formatTime(target.last_checked_at)}</span>
            <span>共用池 {target.shared_count ?? 0} 个可用 IP</span>
          </p>
        </div>
        <Button variant="outline" asChild>
          <Link href="/cf/progress">
            <Activity />
            统一检测进度
          </Link>
        </Button>
      </div>
      {(error || loadError) && (
        <p
          role="alert"
          className="rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive"
        >
          {error || loadError}
        </p>
      )}

      <div className="grid grid-cols-2 gap-3 xl:grid-cols-4">
        <Metric
          label="该域名可用 IP"
          value={String(target.available_count ?? 0)}
          hint="对该域名返回 2xx/3xx，含未复测的历史结果"
        />
        <Metric
          label="上轮可用率"
          value={
            target.availability == null ? "—" : `${target.availability}%`
          }
          hint={
            latest ? `上轮检测 ${latest.total} 个 IP` : "还没有检测记录"
          }
          tone={availabilityTone(target.availability)}
        />
        <Metric
          label="累计可用率"
          value={target.overall == null ? "—" : `${target.overall}%`}
          hint={`累计检测 ${target.checked_total ?? 0} 次`}
          tone={availabilityTone(target.overall)}
        />
        <Metric
          label="检测轮次"
          value={String(target.rounds ?? 0)}
          hint={
            latest
              ? `最近一轮 ${latest.success} / ${latest.total} · ${scanStatusText[latest.status] ?? latest.status}`
              : "每次统一检测计一轮"
          }
        />
      </div>

      <section className="space-y-4 rounded-xl border bg-white p-5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 className="font-semibold">该域名返回的可用 IP</h2>
            <p className="mt-1 text-xs leading-5 text-muted-foreground">
              仅列出检测时该域名实际返回 2xx / 3xx
              的地址；共用池中由其他域名测出的 IP 不在此列。
            </p>
          </div>
          <Button
            variant="outline"
            disabled={!filtered.length}
            onClick={() => copy(filtered.map((item) => item.ip).join("\n"))}
          >
            <Copy />
            复制当前页
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
        {filtered.length ? (
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
                    检测时间
                  </TableHead>
                  <TableHead className="w-16 text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {filtered.map((item) => (
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
            <ListPagination data={detail} onPage={setPage} loading={loading}/>
          </>
        ) : (
          <div className="rounded-lg border border-dashed px-6 py-10 text-center text-sm text-muted-foreground">
            {search ? (
              "没有匹配的 IP"
            ) : (
              <>
                该域名还没有检测记录。统一检测只会记录实际参与检测的域名，
                <Link href="/cf/progress" className="mx-1 text-primary underline">
                  前往检测进度
                </Link>
                了解共用 IP 池情况。
              </>
            )}
          </div>
        )}
      </section>

      <section className="space-y-3 rounded-xl border bg-white p-5">
        <div>
          <h2 className="font-semibold">最近检测记录</h2>
          <p className="mt-1 text-xs text-muted-foreground">
            可用率 = 本轮该域名返回可用的 IP 数 / 本轮检测 IP 数，保留最近 12
            轮。
          </p>
        </div>
        {history.length ? (
          <ul className="divide-y text-sm">
            {history.map((round) => {
              const rate = percent(round.success, round.total) ?? 0;
              return (
                <li
                  key={`${round.finished_at}-${round.total}`}
                  className="flex flex-wrap items-center justify-between gap-3 py-2.5"
                >
                  <span className="tabular-nums text-muted-foreground">
                    {formatTime(round.finished_at)}
                  </span>
                  <span className="flex items-center gap-4 tabular-nums">
                    <span className="text-xs text-muted-foreground">
                      可用 {round.success} / {round.total}
                    </span>
                    <span
                      className={`w-12 text-right ${availabilityTone(rate)}`}
                    >
                      {rate}%
                    </span>
                    <Badge variant="outline" className="w-16 justify-center">
                      {scanStatusText[round.status] ?? round.status}
                    </Badge>
                  </span>
                </li>
              );
            })}
          </ul>
        ) : (
          <p className="py-6 text-center text-sm text-muted-foreground">
            暂无检测记录
          </p>
        )}
      </section>

      <div className="flex flex-wrap items-center gap-3">
        {confirmDelete ? (
          <>
            <p className="text-sm">确定删除此域名？共用 IP 池将保留。</p>
            <Button
              variant="destructive"
              disabled={pending}
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
            className="text-muted-foreground"
            onClick={() => setConfirmDelete(true)}
          >
            删除域名
          </Button>
        )}
      </div>
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
