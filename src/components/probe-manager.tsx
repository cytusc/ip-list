"use client";
import Link from "next/link";
import { useEffect, useRef, useState, useTransition } from "react";
import { Activity, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { startScan, cancelScan } from "@/app/actions";
import type { Node, Network, ScanJob } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
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

const labels = {
  running: "测试中",
  waiting: "等待下轮",
  completed: "已完成",
  cancelled: "已停止",
  interrupted: "已中断",
};
function size(cidr: string): bigint {
  const [address, bits] = cidr.split("/");
  return BigInt(1) << BigInt((address.includes(":") ? 128 : 32) - Number(bits));
}
export function ProbeManager({
  nodes,
  networks,
}: {
  nodes: Node[];
  networks: Network[];
}) {
  const availableNodes = nodes.filter((node) => node.enabled && node.share_uri);
  const availableNetworks = networks.filter((network) => network.enabled);
  const [nodeID, setNodeID] = useState(availableNodes[0]?.id || "");
  const [networkIDs, setNetworkIDs] = useState<string[]>([]);
  const [url, setURL] = useState("https://www.gstatic.com/generate_204");
  const [timeout, setTimeoutMs] = useState(5000);
  const [concurrency, setConcurrency] = useState(5);
  const [interval, setIntervalMinutes] = useState(0);
  const [fullEvery, setFullEvery] = useState(12);
  const restored = useRef(false);
  const [job, setJob] = useState<ScanJob | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [pollError, setPollError] = useState("");
  const [stopping, setStopping] = useState(false);
  const [pending, startTransition] = useTransition();
  const [filter, setFilter] = useState("all");
  const [page, setPage] = useState(1);
  const running = job?.status === "running";
  const active = running || job?.status === "waiting";
  const busy = pending || active || !loaded;
  const jobID = job?.id;
  const jobStatus = job?.status;
  useEffect(() => {
    if (jobStatus && jobStatus !== "running" && jobStatus !== "waiting") return;
    let disposed = false;
    let timer: ReturnType<typeof setTimeout>;
    const controller = new AbortController();
    async function poll() {
      try {
        const response = await fetch(
          `/api/scans${jobID ? `?id=${jobID}` : ""}`,
          { cache: "no-store", signal: controller.signal },
        );
        const data = await response.json();
        if (!response.ok) throw new Error(data.error || "读取进度失败");
        if (!disposed) {
          setJob(data);
          if (!restored.current) {
            restored.current = true;
            if (data?.input?.node_id && Array.isArray(data.input.network_ids)) {
              setNodeID(data.input.node_id);
              setNetworkIDs(data.input.network_ids);
              setURL(data.input.url);
              setTimeoutMs(data.input.timeout_ms);
              setConcurrency(data.input.concurrency);
              setIntervalMinutes(data.input.interval_minutes || 0);
              setFullEvery(data.input.full_every || 12);
            }
          }
          setLoaded(true);
          setPollError("");
          if (data?.status !== "running" && data?.status !== "waiting")
            setStopping(false);
        }
      } catch (error) {
        if (!disposed) {
          setPollError(error instanceof Error ? error.message : "读取进度失败");
          setLoaded(true);
        }
      }
      if (!disposed)
        timer = setTimeout(
          poll,
          document.hidden ? 10000 : jobStatus === "waiting" ? 5000 : 1500,
        );
    }
    void poll();
    return () => {
      disposed = true;
      controller.abort();
      clearTimeout(timer);
    };
  }, [jobID, jobStatus]);
  const selected = availableNetworks.filter((network) =>
    networkIDs.includes(network.id),
  );
  const upperCount = selected.reduce(
    (sum, network) => sum + size(network.cidr),
    BigInt(0),
  );
  const tooLarge = selected.some(
    (network) => size(network.cidr) > BigInt(4096),
  );
  const results = (job?.results || [])
    .filter((row) => filter === "all" || row.status === filter)
    .sort((a, b) => {
      if (a.status !== b.status) return a.status === "success" ? -1 : 1;
      return a.delay_ms - b.delay_ms || a.ip.localeCompare(b.ip);
    });
  const pages = Math.max(1, Math.ceil(results.length / 50));
  const currentPage = Math.min(page, pages);
  return (
    <div className="space-y-6">
      <div>
        <div className="mb-2 flex items-center gap-2 text-xs text-muted-foreground">
          <Activity className="size-4" />
          节点检测
        </div>
        <h1 className="text-[26px] font-semibold">批量连通性测试</h1>
        <p className="mt-2 text-sm leading-6 text-muted-foreground">
          将节点连接地址替换为网段中的 IP，通过代理访问检测网址，找出可用 IP。
        </p>
      </div>
      <form
        className="space-y-5 rounded-xl border bg-white p-5 sm:p-6"
        onSubmit={(event) => {
          event.preventDefault();
          setError("");
          startTransition(async () => {
            try {
              const result = await startScan({
                node_id: nodeID,
                network_ids: networkIDs,
                url,
                timeout_ms: timeout,
                concurrency,
                interval_minutes: interval,
                full_every: fullEvery,
              });
              if (result.ok) {
                setJob(result.job);
                setFilter("all");
                setPage(1);
                setStopping(false);
                toast.success(interval ? "自动巡检已开始" : "测试已开始");
              } else setError(result.error);
            } catch {
              setError("启动请求失败，请稍后重试");
            }
          });
        }}
      >
        <div className="grid gap-5 lg:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="probe-node">测试节点</Label>
            <Select value={nodeID} onValueChange={setNodeID} disabled={busy}>
              <SelectTrigger id="probe-node" className="w-full">
                <SelectValue placeholder="请选择已导入的节点" />
              </SelectTrigger>
              <SelectContent>
                {availableNodes.map((node) => (
                  <SelectItem value={node.id} key={node.id}>
                    {node.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-xs leading-5 text-muted-foreground">
              保留 UUID、端口、SNI、Host、路径与服务名，仅替换连接 IP。
            </p>
            {!availableNodes.length && (
              <Link className="text-sm text-primary underline" href="/nodes">
                前往节点管理导入 VLESS 链接
              </Link>
            )}
          </div>
          <div className="space-y-2">
            <Label htmlFor="probe-url">检测网址</Label>
            <Input
              id="probe-url"
              type="url"
              required
              value={url}
              onChange={(event) => setURL(event.target.value)}
              maxLength={2048}
              disabled={busy}
            />
            <p className="text-xs leading-5 text-muted-foreground">
              通过代理连通检测网址并返回延迟即视为可用，不额外限制 HTTP
              状态码。测试从后端服务器发起。
            </p>
          </div>
        </div>
        <fieldset disabled={busy} className="space-y-3">
          <legend className="mb-2 text-sm font-medium">选择已配置网段</legend>
          <div className="max-h-60 overflow-y-auto rounded-lg border divide-y">
            {availableNetworks.map((network) => (
              <label
                key={network.id}
                className="flex cursor-pointer items-center gap-3 p-3 hover:bg-slate-50"
              >
                <input
                  type="checkbox"
                  checked={networkIDs.includes(network.id)}
                  onChange={(event) =>
                    setNetworkIDs(
                      event.target.checked
                        ? [...networkIDs, network.id]
                        : networkIDs.filter((id) => id !== network.id),
                    )
                  }
                  className="size-4 accent-blue-600"
                />
                <span className="min-w-0 flex-1 text-sm">
                  <span className="font-medium">{network.name}</span>
                  <span className="ml-2 font-mono text-xs text-muted-foreground">
                    {network.cidr}
                  </span>
                  <span className="mt-1 block text-xs text-muted-foreground">
                    所属节点：{network.node_name}
                  </span>
                </span>
                <span className="text-xs tabular-nums text-muted-foreground">
                  {size(network.cidr).toString()} IP
                </span>
              </label>
            ))}
            {!availableNetworks.length && (
              <p className="p-4 text-sm text-muted-foreground">
                暂无已启用的网段。
                <Link href="/networks" className="ml-2 text-primary underline">
                  添加网段
                </Link>
              </p>
            )}
          </div>
          <p className="text-xs leading-5 text-muted-foreground">
            已选 {selected.length} 个网段，地址总数上限 {upperCount.toString()}
            （重叠地址自动去重）。每次最多 4096 个
            IP，包含网段内全部地址；网段的允许/阻止策略不影响此次检测。
          </p>
          {tooLarge && (
            <p role="alert" className="text-sm text-destructive">
              所选网段过大，请在网段配置中拆分为更小的网段。
            </p>
          )}
        </fieldset>
        <div className="space-y-3 rounded-lg bg-slate-50 p-4">
          <Label htmlFor="patrol-interval">巡检方式</Label>
          <Select
            value={String(interval)}
            onValueChange={(value) => setIntervalMinutes(Number(value))}
            disabled={busy}
          >
            <SelectTrigger id="patrol-interval" className="w-full sm:w-64">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="0">单次检测</SelectItem>
              <SelectItem value="1">自动巡检 · 间隔 1 分钟</SelectItem>
              <SelectItem value="5">自动巡检 · 间隔 5 分钟</SelectItem>
              <SelectItem value="15">自动巡检 · 间隔 15 分钟</SelectItem>
              <SelectItem value="30">自动巡检 · 间隔 30 分钟</SelectItem>
              <SelectItem value="60">自动巡检 · 间隔 60 分钟</SelectItem>
            </SelectContent>
          </Select>
          {interval > 0 && (
            <>
              <Label htmlFor="patrol-full">每隔多少轮重新扫描全部网段</Label>
              <Input
                id="patrol-full"
                type="number"
                min={1}
                max={288}
                required
                value={fullEvery}
                disabled={busy}
                className="w-32"
                onChange={(event) => setFullEvery(Number(event.target.value))}
              />
              <p className="text-xs leading-5 text-muted-foreground">
                首轮全量扫描，之后优先复测可用 IP，每隔 {fullEvery}{" "}
                轮再次全量扫描；设为 1 则每轮全量。没有可用 IP
                时自动全量扫描。每轮完成后等待 {interval} 分钟，不会重叠执行。
              </p>
              <p className="text-xs leading-5 text-muted-foreground">
                巡检使用启动时的节点和网段配置，离开页面仍会继续；停止后可调整配置。服务重启后需手动重新启动巡检。
              </p>
            </>
          )}
        </div>
        <div className="flex flex-wrap items-end gap-4">
          <div className="space-y-2">
            <Label htmlFor="probe-timeout">单 IP 超时（毫秒）</Label>
            <Input
              id="probe-timeout"
              className="w-44"
              type="number"
              min={1000}
              max={15000}
              required
              value={timeout}
              disabled={busy}
              onChange={(event) => setTimeoutMs(Number(event.target.value))}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="probe-concurrency">并发数</Label>
            <Input
              id="probe-concurrency"
              className="w-28"
              type="number"
              min={1}
              max={32}
              required
              value={concurrency}
              disabled={busy}
              onChange={(event) => setConcurrency(Number(event.target.value))}
            />
          </div>
          <Button
            type="submit"
            disabled={
              busy || !loaded || !nodeID || !networkIDs.length || tooLarge
            }
          >
            {pending ? <Loader2 className="animate-spin" /> : <Activity />}
            {pending
              ? "正在启动…"
              : active
                ? "任务进行中"
                : interval
                  ? "开始巡检"
                  : "开始测试"}
          </Button>
        </div>
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
      </form>
      {pollError && (
        <p
          role="alert"
          className="rounded-lg border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900"
        >
          {pollError}。正在自动重试，现有结果已保留。
        </p>
      )}
      {!loaded && (
        <p role="status" className="text-sm text-muted-foreground">
          正在读取最近一次任务…
        </p>
      )}
      {!job && loaded && (
        <div className="rounded-xl border border-dashed p-10 text-center text-sm text-muted-foreground">
          选择节点与网段后开始测试。结果保留 24 小时，刷新页面可继续查看。
        </div>
      )}
      {job && (
        <section className="overflow-hidden rounded-xl border bg-white">
          <div className="space-y-4 border-b p-5 sm:p-6">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div>
                <h2 className="font-semibold">
                  {job.node_name}
                  <Badge variant="secondary" className="ml-3">
                    {labels[job.status]}
                  </Badge>
                </h2>
                <p className="mt-2 break-all text-xs text-muted-foreground">
                  {job.cidrs.join("、")} · {job.url}
                </p>
              </div>
              {active && (
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
                      }
                    } catch {
                      toast.error("停止失败，请重试");
                      setStopping(false);
                    }
                  }}
                >
                  {stopping
                    ? "正在停止…"
                    : job.input?.interval_minutes
                      ? "停止巡检"
                      : "停止测试"}
                </Button>
              )}
            </div>
            <p className="text-xs leading-5 text-muted-foreground">
              第 {job.round || 1} 轮 ·{" "}
              {job.scope === "available" ? "复测可用 IP" : "全量扫描"}
              {job.started_at &&
                ` · 开始于 ${new Date(job.started_at).toLocaleString()}`}
              {job.next_run_at &&
                ` · 下轮 ${new Date(job.next_run_at).toLocaleString()}`}
            </p>
            <div
              role="status"
              aria-live="polite"
              className="flex flex-wrap gap-4 text-sm tabular-nums"
            >
              <span>
                已测试 {job.completed} / {job.total}
              </span>
              <span className="text-emerald-700">可用 {job.success}</span>
              <span className="text-red-700">
                失败 {job.completed - job.success}
              </span>
              {job.input?.interval_minutes ? (
                <span className="text-muted-foreground">
                  新增{" "}
                  {job.results.filter((row) => row.change === "new").length} ·
                  恢复{" "}
                  {
                    job.results.filter((row) => row.change === "recovered")
                      .length
                  }{" "}
                  · 本轮失效{" "}
                  {job.results.filter((row) => row.change === "lost").length}
                </span>
              ) : null}
              <span className="text-muted-foreground">
                未测试 {job.total - job.completed}
              </span>
            </div>
            <progress
              className="h-2 w-full accent-blue-600"
              value={job.completed}
              max={job.total}
              aria-label="测试进度"
            />
            {job.error && (
              <p role="alert" className="text-sm text-destructive">
                {job.error}
              </p>
            )}
            <p className="text-xs text-muted-foreground">
              结果仅代表当前轮已测试的
              IP，按可用优先、延迟升序排列。离开页面不会停止任务；停止后的结果保留
              24 小时。
            </p>
          </div>
          <div className="flex flex-wrap items-center justify-between gap-3 p-4">
            <Select
              value={filter}
              onValueChange={(value) => {
                setFilter(value);
                setPage(1);
              }}
            >
              <SelectTrigger className="w-36" aria-label="筛选测试结果">
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
              disabled={!job.success}
              onClick={async () => {
                try {
                  await navigator.clipboard.writeText(
                    job.results
                      .filter((row) => row.status === "success")
                      .sort((a, b) => a.delay_ms - b.delay_ms)
                      .map((row) => row.ip)
                      .join("\n"),
                  );
                  toast.success("可用 IP 已复制");
                } catch {
                  toast.error("复制失败，请手动选择表格中的 IP");
                }
              }}
            >
              复制可用 IP
            </Button>
          </div>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-6">连接 IP</TableHead>
                <TableHead>结果</TableHead>
                <TableHead>延迟</TableHead>
                <TableHead>变化 / 说明</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {results
                .slice((currentPage - 1) * 50, currentPage * 50)
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
          {!results.length && (
            <p className="p-10 text-center text-sm text-muted-foreground">
              {running ? "等待检测结果…" : "暂无符合筛选条件的结果"}
            </p>
          )}
          <div className="flex items-center justify-between border-t p-4 text-xs text-muted-foreground">
            <span>
              {results.length} 条结果 · {currentPage} / {pages}
            </span>
            <div className="flex gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={currentPage === 1}
                onClick={() => setPage(currentPage - 1)}
              >
                上一页
              </Button>
              <Button
                variant="outline"
                size="sm"
                disabled={currentPage === pages}
                onClick={() => setPage(currentPage + 1)}
              >
                下一页
              </Button>
            </div>
          </div>
        </section>
      )}
    </div>
  );
}
