"use client";

import Link from "next/link";
import { useState, useTransition } from "react";
import { Activity, Copy, KeyRound, Loader2, Plus, Radio, Save, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  createLatencyProbe,
  deleteLatencyProbe,
  rotateLatencyProbeToken,
  updateLatencyProbe,
} from "@/app/actions";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { LatencyProbe } from "@/lib/types";

type Config = {
  name: string;
  interval_seconds: number;
  timeout_ms: number;
  concurrency: number;
  interface_name: string;
  enabled: boolean;
};

const blank: Config = {
  name: "",
  interval_seconds: 300,
  timeout_ms: 1000,
  concurrency: 64,
  interface_name: "",
  enabled: true,
};

export function LatencyProbeManager({ initial }: { initial: LatencyProbe[] }) {
  const [items, setItems] = useState(initial);
  const [draft, setDraft] = useState(blank);
  const [credential, setCredential] = useState<{
    name: string;
    token: string;
    server: string;
  } | null>(null);
  const [pending, startTransition] = useTransition();

  function updateDraft<K extends keyof Config>(key: K, value: Config[K]) {
    setDraft((current) => ({ ...current, [key]: value }));
  }

  function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    startTransition(async () => {
      const result = await createLatencyProbe({ ...draft });
      if (!result.ok) {
        toast.error(result.error);
        return;
      }
      setItems((current) => [result.probe, ...current]);
      setDraft(blank);
      setCredential({
        name: result.probe.name,
        token: result.token,
        server: window.location.origin,
      });
      toast.success("探针已创建，请在电脑上启动探针程序");
    });
  }

  async function copyCommand() {
    if (!credential) return;
    const command = `cd backend && PROBE_SERVER='${credential.server}' PROBE_TOKEN='${credential.token}' go run ./cmd/latency-probe`;
    try {
      await navigator.clipboard.writeText(command);
      toast.success("启动命令已复制");
    } catch {
      toast.error("复制失败，请手动选择命令复制");
    }
  }

  async function save(item: LatencyProbe, config: Config) {
    const result = await updateLatencyProbe(item.id, config);
    if (!result.ok) {
      toast.error(result.error);
      return;
    }
    setItems((current) =>
      current.map((value) =>
        value.id === item.id ? { ...value, ...config } : value,
      ),
    );
    toast.success("探针配置已保存");
  }

  async function rotate(item: LatencyProbe) {
    const result = await rotateLatencyProbeToken(item.id);
    if (!result.ok) {
      toast.error(result.error);
      return;
    }
    setCredential({
      name: item.name,
      token: result.token,
      server: window.location.origin,
    });
    toast.success("密钥已更新，正在运行的旧探针需要重新配置");
  }

  async function remove(item: LatencyProbe) {
    if (!window.confirm(`删除探针“${item.name}”？其历史延迟记录也会删除。`))
      return;
    const result = await deleteLatencyProbe(item.id);
    if (!result.ok) {
      toast.error(result.error);
      return;
    }
    setItems((current) => current.filter((value) => value.id !== item.id));
    toast.success("探针及其历史记录已删除");
  }

  return (
    <div className="space-y-6">
      <div>
        <div className="mb-2 flex items-center gap-2 text-xs text-muted-foreground">
          <Radio className="size-4" />
          本机 ICMP 探针
        </div>
        <h1 className="text-2xl font-semibold tracking-tight">延迟探针</h1>
        <p className="mt-2 max-w-3xl text-sm leading-6 text-muted-foreground">
          在电脑运行探针后，它会直接用系统 ping 测量当前可用 IP，并定期上报。ICMP
          不使用 HTTP 或 SOCKS 代理；若电脑启用了 VPN/TUN，请填写真实网卡名来指定出口。
          每个探针可使用不同名称和检测间隔。
        </p>
      </div>

      {credential && (
        <section className="space-y-3 rounded-xl border border-blue-200 bg-blue-50 p-4 sm:p-5">
          <div className="flex items-center gap-2 font-medium text-blue-950">
            <KeyRound className="size-4" />
            {credential.name} 的启动密钥
          </div>
          <p className="text-sm leading-6 text-blue-900">
            密钥只在此处显示。请复制并妥善保存；刷新或离开页面后无法再次查看，可重新生成。
          </p>
          <div className="overflow-x-auto rounded-md border bg-white p-3 font-mono text-xs whitespace-nowrap">
            {credential.token}
          </div>
          <div className="overflow-x-auto rounded-md bg-slate-950 p-3 font-mono text-xs whitespace-nowrap text-slate-100">
            cd /path/to/ip_list/backend &amp;&amp; PROBE_SERVER=&apos;{credential.server}&apos; PROBE_TOKEN=&apos;{credential.token}&apos; go run ./cmd/latency-probe
          </div>
          <Button size="sm" onClick={copyCommand}>
            <Copy />复制启动命令
          </Button>
          <p className="text-xs leading-5 text-blue-900">
            电脑需要安装 Go。把项目代码放到电脑后，在 backend 目录运行上面的命令；探针会在终端持续运行。
          </p>
        </section>
      )}

      <form onSubmit={create} className="space-y-4 rounded-xl border bg-white p-4 sm:p-5">
        <div>
          <h2 className="font-semibold">添加探针</h2>
          <p className="mt-1 text-xs text-muted-foreground">
            名称会显示在可用 IP 列表，例如“电信”“家里宽带”。
          </p>
        </div>
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <Field label="探针名称">
            <Input
              value={draft.name}
              maxLength={80}
              required
              placeholder="例如：电信"
              onChange={(event) => updateDraft("name", event.target.value)}
            />
          </Field>
          <Field label="检测间隔（秒）">
            <Input
              type="number"
              min={15}
              max={86400}
              required
              value={draft.interval_seconds}
              onChange={(event) => updateDraft("interval_seconds", Number(event.target.value))}
            />
          </Field>
          <Field label="单 IP 超时（毫秒）">
            <Input
              type="number"
              min={250}
              max={10000}
              required
              value={draft.timeout_ms}
              onChange={(event) => updateDraft("timeout_ms", Number(event.target.value))}
            />
          </Field>
          <Field label="并发 ping 数">
            <Input
              type="number"
              min={1}
              max={512}
              required
              value={draft.concurrency}
              onChange={(event) => updateDraft("concurrency", Number(event.target.value))}
            />
          </Field>
          <Field label="出口网卡（可选）">
            <Input
              value={draft.interface_name}
              maxLength={100}
              placeholder="留空使用系统路由，例如 en0 / eth0"
              onChange={(event) => updateDraft("interface_name", event.target.value)}
            />
          </Field>
        </div>
        <Button disabled={pending}>
          {pending ? <Loader2 className="animate-spin" /> : <Plus />}
          创建探针
        </Button>
      </form>

      <section className="space-y-3">
        <div className="flex items-center justify-between">
          <h2 className="font-semibold">已配置的探针</h2>
          <span className="text-xs text-muted-foreground">{items.length} 个</span>
        </div>
        {items.length ? items.map((item) => (
          <ProbeCard
            key={item.id}
            item={item}
            busy={pending}
            onSave={(config) => void save(item, config)}
            onRotate={() => void rotate(item)}
            onDelete={() => void remove(item)}
          />
        )) : (
          <div className="rounded-xl border border-dashed bg-white p-8 text-center text-sm text-muted-foreground">
            还没有探针。创建后复制启动命令，在电脑上运行即可开始上报。
          </div>
        )}
      </section>

      <div className="rounded-lg border bg-slate-50 p-4 text-xs leading-5 text-muted-foreground">
        <div className="mb-1 flex items-center gap-2 font-medium text-foreground">
          <Activity className="size-4" />延迟口径
        </div>
        记录的是探针电脑发出的 ICMP 往返时间，单位毫秒，不经过网站服务器代测。部分网络或目标会屏蔽 ICMP；这类地址会显示超时，而不代表其代理连接一定不可用。绑定网卡可避免 VPN/TUN 改写默认出口。
      </div>
    </div>
  );
}

function ProbeCard({
  item,
  busy,
  onSave,
  onRotate,
  onDelete,
}: {
  item: LatencyProbe;
  busy: boolean;
  onSave: (config: Config) => void;
  onRotate: () => void;
  onDelete: () => void;
}) {
  const [config, setConfig] = useState<Config>({
    name: item.name,
    interval_seconds: item.interval_seconds,
    timeout_ms: item.timeout_ms,
    concurrency: item.concurrency,
    interface_name: item.interface_name,
    enabled: item.enabled,
  });
  const changed = config.name !== item.name ||
    config.interval_seconds !== item.interval_seconds ||
    config.timeout_ms !== item.timeout_ms ||
    config.concurrency !== item.concurrency ||
    config.interface_name !== item.interface_name ||
    config.enabled !== item.enabled;
  return (
    <article className="space-y-4 rounded-xl border bg-white p-4 sm:p-5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <h3 className="font-medium">{item.name}</h3>
          <Badge variant={item.online ? "default" : "secondary"}>
            {item.online ? "在线" : item.enabled ? "离线" : "已停用"}
          </Badge>
        </div>
        <span className="text-xs text-muted-foreground">
          {item.last_seen ? `上次连接：${new Date(item.last_seen).toLocaleString("zh-CN", { hour12: false })}` : "探针启动后显示连接时间"}
        </span>
      </div>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <Field label="探针名称">
          <Input value={config.name} maxLength={80} onChange={(e) => setConfig({ ...config, name: e.target.value })} />
        </Field>
        <Field label="检测间隔（秒）">
          <Input type="number" min={15} max={86400} value={config.interval_seconds} onChange={(e) => setConfig({ ...config, interval_seconds: Number(e.target.value) })} />
        </Field>
        <Field label="单 IP 超时（毫秒）">
          <Input type="number" min={250} max={10000} value={config.timeout_ms} onChange={(e) => setConfig({ ...config, timeout_ms: Number(e.target.value) })} />
        </Field>
        <Field label="并发 ping 数">
          <Input type="number" min={1} max={512} value={config.concurrency} onChange={(e) => setConfig({ ...config, concurrency: Number(e.target.value) })} />
        </Field>
        <Field label="出口网卡（可选）">
          <Input value={config.interface_name} maxLength={100} placeholder="en0 / eth0" onChange={(e) => setConfig({ ...config, interface_name: e.target.value })} />
        </Field>
        <div className="flex items-end pb-2">
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={config.enabled} onChange={(e) => setConfig({ ...config, enabled: e.target.checked })} />
            启用探针
          </label>
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2 border-t pt-3">
        <Button size="sm" disabled={busy || !changed} onClick={() => onSave(config)}>
          <Save />保存配置
        </Button>
        <Button size="sm" variant="outline" disabled={busy} onClick={onRotate}>
          <KeyRound />重新生成密钥
        </Button>
        <Button size="sm" variant="ghost" className="text-destructive" disabled={busy} onClick={onDelete}>
          <Trash2 />删除
        </Button>
        <Link className="ml-auto inline-flex items-center gap-1 text-sm text-primary hover:underline" href={`/latency-probes/${item.id}`}>
          查看延迟历史
        </Link>
      </div>
    </article>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-2">
      <Label>{label}</Label>
      {children}
    </div>
  );
}
