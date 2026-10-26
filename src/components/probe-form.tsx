"use client";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState, useTransition } from "react";
import { Loader2, Play, Plus } from "lucide-react";
import { toast } from "sonner";
import { startScan, restoreProtocolNetwork } from "@/app/actions";
import { nodeProtocol, protocolLabels } from "@/lib/protocol";
import { networkAddressCount as size } from "@/lib/network";
import { NetworkSelector } from "@/components/network-selector";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
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
import type { ActiveScan, Node, Network, ScanInput } from "@/lib/types";

/** 检测配置收在弹窗里，列表只负责展示历史记录。 */
export function NewProbeDialog({
  nodes,
  networks,
  defaults,
  active,
  onStarted,
}: {
  nodes: Node[];
  networks: Network[];
  defaults?: ScanInput | null;
  active: ActiveScan | null;
  onStarted?: () => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button>
          <Plus />
          新建检测
        </Button>
      </DialogTrigger>
      <DialogContent className="max-h-[90dvh] overflow-x-hidden overflow-y-auto [&>*]:min-w-0 sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>新建连通性检测</DialogTitle>
          <DialogDescription>
            选择一个样本节点与若干网段，后台逐 IP
            检测；同一协议池的节点共用结果，检测记录可在列表中回看。
          </DialogDescription>
        </DialogHeader>
        {active && (
          <p className="rounded-lg border border-blue-200 bg-blue-50 p-3 text-sm text-blue-900">
            {active.node_name}（
            {protocolLabels[active.protocol] || active.protocol}）正在
            {active.status === "waiting" ? "等待下一轮" : "检测"}
            。同一协议池同时只能运行一个任务，选择其他协议的节点仍可开始新检测。
          </p>
        )}
        <ProbeForm
          nodes={nodes}
          networks={networks}
          defaults={defaults}
          activeProtocol={active?.protocol}
          onStarted={onStarted}
          onDone={() => setOpen(false)}
        />
      </DialogContent>
    </Dialog>
  );
}

function ProbeForm({
  nodes,
  networks,
  defaults,
  activeProtocol,
  onStarted,
  onDone,
}: {
  nodes: Node[];
  networks: Network[];
  defaults?: ScanInput | null;
  activeProtocol?: string;
  onStarted?: () => void;
  onDone: () => void;
}) {
  const router = useRouter();
  const availableNodes = nodes.filter((node) => node.enabled && node.share_uri);
  const [nodeID, setNodeID] = useState(
    () =>
      availableNodes.find((node) => node.id === defaults?.node_id)?.id ??
      availableNodes[0]?.id ??
      "",
  );
  const [networkIDs, setNetworkIDs] = useState<string[]>(() =>
    (defaults?.network_ids ?? []).filter((id) =>
      networks.some((network) => network.id === id && network.enabled),
    ),
  );
  const [url, setURL] = useState(
    () => defaults?.url || "https://www.gstatic.com/generate_204",
  );
  const [timeout, setTimeoutMs] = useState(() => defaults?.timeout_ms ?? 5000);
  const [concurrency, setConcurrency] = useState(
    () => defaults?.concurrency ?? 5,
  );
  const [interval, setIntervalMinutes] = useState(
    () => defaults?.interval_minutes ?? 0,
  );
  const [incremental, setIncremental] = useState(
    () => defaults?.incremental ?? !defaults?.interval_minutes,
  );
  const [fullEvery, setFullEvery] = useState(() => defaults?.full_every ?? 12);
  const [error, setError] = useState("");
  const [pending, startTransition] = useTransition();
  const protocol = nodeProtocol(
    nodes.find((node) => node.id === nodeID)?.share_uri || "",
  );
  const disabledNetworks = networks.filter(
    (network) =>
      network.enabled && network.disabled_protocols?.includes(protocol),
  );
  const poolNetworks = networks.map((network) => ({
    ...network,
    enabled: network.enabled && !network.disabled_protocols?.includes(protocol),
  }));
  const availableNetworks = poolNetworks.filter((network) => network.enabled);
  const selected = availableNetworks.filter((network) =>
    networkIDs.includes(network.id),
  );
  const upperCount = selected.reduce(
    (sum, network) => sum + size(network.cidr),
    BigInt(0),
  );
  const tooLarge = !incremental && selected.some(
    (network) => size(network.cidr) > BigInt(4096),
  );
  // 同一协议池共用任务锁，其他池可以并行检测。
  const blocked = !!activeProtocol && activeProtocol === protocol;
  const busy = pending || blocked;
  return (
    <form
      className="space-y-5"
      onSubmit={(event) => {
        event.preventDefault();
        setError("");
        startTransition(async () => {
          try {
            const result = await startScan({
              node_id: nodeID,
              network_ids: networkIDs.filter((id) =>
                availableNetworks.some((network) => network.id === id),
              ),
              url,
              timeout_ms: timeout,
              concurrency,
              interval_minutes: interval,
              full_every: fullEvery,
              incremental,
            });
            if (!result.ok) {
              setError(result.error);
              return;
            }
            onDone();
            onStarted?.();
            toast.success(interval ? "自动巡检已开始" : "测试已开始");
          } catch {
            setError("启动请求失败，请稍后重试");
          }
        });
      }}
    >
      <div className="grid gap-5 sm:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor="probe-node">测试节点</Label>
          <Select
            value={nodeID}
            onValueChange={setNodeID}
            disabled={busy}
          >
            <SelectTrigger
              id="probe-node"
              className="w-full min-w-0 [&_[data-slot=select-value]]:block [&_[data-slot=select-value]]:truncate"
            >
              <SelectValue placeholder="请选择已导入的节点" />
            </SelectTrigger>
            <SelectContent>
              {availableNodes.map((node) => (
                <SelectItem value={node.id} key={node.id}>
                  {node.name} ·{" "}
                  {protocolLabels[nodeProtocol(node.share_uri)] ||
                    nodeProtocol(node.share_uri)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p className="text-xs leading-5 text-muted-foreground">
            当前池：{protocolLabels[protocol] || protocol || "请选择节点"}
            。仅替换样本节点的连接 IP，保留其他参数。
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
            通过代理连通检测网址并返回延迟即视为可用，不测试 HTTP 状态码。
          </p>
        </div>
      </div>
      <fieldset disabled={busy} className="space-y-3">
        <legend className="mb-2 text-sm font-medium">选择已配置网段</legend>
        <NetworkSelector
          networks={poolNetworks}
          value={networkIDs}
          onChange={setNetworkIDs}
          disabled={busy}
        />
        <p className="text-xs leading-5 text-muted-foreground">
          已选 {selected.length} 个网段，地址总数上限 {upperCount.toString()}
          （重叠地址自动去重）。每次最多检测 4096 个 IP；增量模式仅计算未检测地址。
        </p>
        {tooLarge && (
          <p role="alert" className="text-sm text-destructive">
            所选网段过大，请在网段配置中拆分为更小的网段。
          </p>
        )}
      </fieldset>
      {!!disabledNetworks.length && (
        <div className="space-y-2 rounded-lg border bg-muted/30 p-3">
          <p className="text-sm font-medium">
            当前协议池已禁用 {disabledNetworks.length} 个空网段
          </p>
          <p className="text-xs text-muted-foreground">
            完整扫描后全部失败，仅影响当前池。恢复后将再次参与扫描。
          </p>
          {disabledNetworks.map((network) => (
            <div
              key={network.id}
              className="flex flex-wrap items-center justify-between gap-2 text-sm"
            >
              <span>
                {network.name} · {network.cidr}
              </span>
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={pending}
                onClick={() =>
                  startTransition(async () => {
                    const result = await restoreProtocolNetwork(
                      network.id,
                      protocol,
                    );
                    if (!result.ok) setError(result.error);
                    else {
                      router.refresh();
                      toast.success("网段已恢复，后续巡检将重新扫描");
                    }
                  })
                }
              >
                恢复扫描
              </Button>
            </div>
          ))}
        </div>
      )}
      <div className="space-y-3 rounded-lg bg-slate-50 p-4">
        <Label htmlFor="patrol-interval">检测方式</Label>
        <Select
          value={incremental ? "incremental" : String(interval)}
          onValueChange={(value) => {
            setIncremental(value === "incremental");
            setIntervalMinutes(value === "incremental" ? 0 : Number(value));
          }}
          disabled={busy}
        >
          <SelectTrigger id="patrol-interval" className="w-full sm:w-64">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="incremental">增量检测 · 仅检测新增 IP</SelectItem>
            <SelectItem value="0">全量检测 · 单次</SelectItem>
            <SelectItem value="1">自动巡检 · 间隔 1 分钟</SelectItem>
            <SelectItem value="5">自动巡检 · 间隔 5 分钟</SelectItem>
            <SelectItem value="15">自动巡检 · 间隔 15 分钟</SelectItem>
            <SelectItem value="30">自动巡检 · 间隔 30 分钟</SelectItem>
            <SelectItem value="60">自动巡检 · 间隔 60 分钟</SelectItem>
          </SelectContent>
        </Select>
        {incremental && (
          <p className="text-xs leading-5 text-muted-foreground">
            跳过当前协议池中已检测的 IP（含失败结果），仅检测新增或上次未完成的地址，保留已有结果。需要复测时选择全量检测。
          </p>
        )}
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
              轮再次全量扫描；没有可用 IP 时自动全量扫描。每轮完成后等待{" "}
              {interval} 分钟，不会重叠执行。
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
      </div>
      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="outline" onClick={onDone}>
          取消
        </Button>
        <Button
          type="submit"
          disabled={busy || !nodeID || !selected.length || tooLarge}
        >
          {pending ? <Loader2 className="animate-spin" /> : <Play />}
          {pending ? "正在启动…" : interval ? "开始巡检" : incremental ? "开始增量检测" : "开始测试"}
        </Button>
      </div>
      {blocked && (
        <p className="text-sm text-amber-700">
          当前协议池已有任务在运行，请先在列表中停止，或改用其他协议的节点。
        </p>
      )}
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
    </form>
  );
}
