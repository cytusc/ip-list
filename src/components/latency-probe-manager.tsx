"use client";

import Link from "next/link";
import { useId, useState, useTransition, cloneElement, type ReactNode, type ReactElement } from "react";
import { Activity, Copy, Download, KeyRound, Loader2, Plus, Radio, Save, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  createLatencyProbe,
  deleteLatencyProbe,
  rotateLatencyProbeToken,
  updateLatencyProbe,
} from "@/app/actions";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
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

export function LatencyProbeManager({ initial, tlsPin }: { initial: LatencyProbe[]; tlsPin: string }) {
  const [items, setItems] = useState(initial);
  const [draft, setDraft] = useState(blank);
  const [deploy, setDeploy] = useState<LatencyProbe | null>(null);
  const [platforms, setPlatforms] = useState<Record<string, string>>({});
  const [credentials, setCredentials] = useState<Record<string, string>>({});
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
      setCredentials((current) => ({ ...current, [result.probe.id]: result.token }));
      setDeploy(result.probe);
      toast.success("探针已创建，请在电脑上启动探针程序");
    });
  }

  const currentToken = deploy ? credentials[deploy.id] || "" : "";
  const platform = deploy ? platforms[deploy.id] || "darwin" : "darwin";
  function deploymentCommand(token: string, targetPlatform: string) {
    const origin = typeof window !== "undefined" ? window.location.origin : "";
    const quote = (value: string) => "'" + value.replaceAll("'", targetPlatform === "windows" ? "''" : "'\\''") + "'";
    const pinArgs = tlsPin ? ` --insecure --pinnedpubkey ${quote(`sha256//${tlsPin}`)}` : "";
    return targetPlatform === "windows"
      ? `$p = Join-Path $env:TEMP ('ip-list-' + [guid]::NewGuid() + '.ps1'); curl.exe -fSs${pinArgs} ${quote(origin + "/probe-downloads/install.ps1")} -o $p; if ($LASTEXITCODE -eq 0) { powershell.exe -NoProfile -ExecutionPolicy Bypass -File $p -Server ${quote(origin)} -Token ${quote(token)} ${tlsPin ? `-Pin ${quote(tlsPin)}` : ""}; Remove-Item $p }`
      : `( f=$(mktemp) || exit 1; trap 'rm -f "$f"' EXIT; curl -fSs${pinArgs} ${quote(origin + "/probe-downloads/install.sh")} -o "$f" && ${targetPlatform === "linux" ? "sudo " : ""}sh "$f" ${quote(origin)} ${quote(token)} ${quote(tlsPin)} )`;
  }
  const command = deploymentCommand(currentToken || "请先生成或填写部署密钥", platform);

  async function copyCommand(item: LatencyProbe) {
    const token = credentials[item.id];
    if (!token) {
      toast.error("请先点击编辑，填写或生成部署密钥并选择系统，再复制部署命令");
      return;
    }
    try {
      await navigator.clipboard.writeText(deploymentCommand(token, platforms[item.id] || "darwin"));
      toast.success("部署命令已复制，请在探针电脑上运行");
    } catch {
      toast.error("复制失败，请在编辑中手动选择命令复制");
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
    setCredentials((current) => ({ ...current, [item.id]: result.token }));
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

  const deploymentSettings = (
    <section className="space-y-3 border-t pt-4">
      <h3 className="font-medium">部署配置</h3>
      <p className="text-sm text-muted-foreground">选择探针电脑的系统并配置密钥，随后可在列表点击“一键部署”直接复制命令。命令会自动下载二进制、写入配置并启动后台服务。</p>
          <div className="flex flex-wrap gap-2">
            {[['darwin', 'macOS'], ['linux', 'Linux'], ['windows', 'Windows']].map(([value, label]) => (
              <Button key={value} variant={platform === value ? "default" : "outline"} size="sm" onClick={() => deploy && setPlatforms((current) => ({ ...current, [deploy.id]: value }))}>{label}</Button>
            ))}
          </div>
          <p className="text-sm text-muted-foreground">{platform === "darwin" ? "在终端粘贴运行，自动识别 Intel / Apple 芯片，安装为登录后自动运行的后台探针。" : platform === "linux" ? "在终端粘贴运行，自动识别 amd64 / arm64，使用 sudo 安装为 systemd 服务。需要系统 ping 和 curl。" : "在 PowerShell 粘贴运行，自动下载程序并创建登录后启动的计划任务。"}</p>
          <Field label="部署密钥">
            <Input type="password" autoComplete="off" value={currentToken} placeholder="粘贴已保存的密钥，或点击下方重新生成" onChange={(event) => deploy && setCredentials((current) => ({ ...current, [deploy.id]: event.target.value.trim() }))} />
          </Field>
          {!currentToken && <p className="text-xs text-muted-foreground">服务器只保存密钥哈希，无法找回旧密钥。重新生成后，旧密钥立即失效。</p>}
          <Button variant="outline" disabled={pending} onClick={() => deploy && window.confirm("重新生成将使此探针的旧密钥立即失效，确定继续？") && startTransition(() => rotate(deploy))}><KeyRound />生成新密钥并获取部署命令</Button>
          <div className="space-y-3 rounded-lg border p-3">
            <p className="text-sm font-medium">一键部署命令</p>
            <p className="text-xs text-muted-foreground">{currentToken ? "命令已包含服务器地址和鉴权密钥，复制后直接执行。密钥仅在当前页面保留；刷新后需重新填写，服务器无法找回明文。" : "请先填写已有密钥，或点击上方按钮生成新密钥；下方为命令预览。"}</p>
            <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-all rounded-md bg-slate-950 p-3 text-xs text-slate-100">{command}</pre>
            <Button disabled={!currentToken} onClick={() => deploy && copyCommand(deploy)}><Copy />复制一键部署命令</Button>
          </div>
          <details className="space-y-2 border-t pt-3 text-sm">
            <summary className="cursor-pointer font-medium">手动下载（可选）</summary>
            <div className="flex flex-wrap gap-4">
              {["amd64", "arm64"].map((arch) => <a key={arch} className="text-primary underline" href={`/probe-downloads/latency-probe-${platform}-${arch}${platform === "windows" ? ".exe" : ""}`}><Download className="mr-1 inline size-4" />{arch === "amd64" ? "Intel / AMD x64" : "ARM64 / Apple 芯片"}</a>)}
              <a className="text-primary underline" href={`/probe-downloads/install.${platform === "windows" ? "ps1" : "sh"}`}>下载安装脚本</a>
            </div>
            <p className="text-xs text-muted-foreground">手动运行：程序支持 -server、-token、-tls-pin 或 -config 配置文件。建议使用上方命令，自动配置鉴权和后台运行。每台电脑建议单独创建探针。</p>
          </details>
    </section>
  );

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
          会绑定出口网卡；留空时自动选择已连接的物理网卡，存在多个出口时需手动指定。
          每个探针可使用不同名称和检测间隔。
        </p>
      </div>


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
              placeholder="留空自动选择物理网卡，例如 en0 / eth0"
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
        {items.length ? <div className="overflow-x-auto rounded-xl border bg-white"><table className="w-full whitespace-nowrap text-sm">
          <thead className="bg-slate-50 text-left text-muted-foreground"><tr>{["名称", "状态", "检测间隔", "出口网卡", "最后连接", "操作"].map((title) => <th key={title} className="px-4 py-3 font-medium">{title}</th>)}</tr></thead>
          <tbody>{items.map((item) => <tr key={item.id} className="border-t">
            <td className="px-4 py-3 font-medium">{item.name}</td>
            <td className="px-4 py-3"><Badge variant={item.online ? "default" : "secondary"}>{!item.enabled ? "已停用" : item.online ? "在线" : "离线"}</Badge></td>
            <td className="px-4 py-3">{item.interval_seconds} 秒</td>
            <td className="px-4 py-3">{item.interface_name || "自动选择物理网卡"}</td>
            <td className="px-4 py-3 text-muted-foreground">{item.last_seen ? new Date(item.last_seen).toLocaleString("zh-CN", { hour12: false }) : "尚未连接"}</td>
            <td className="px-4 py-3"><div className="flex items-center gap-2">
              <Button size="sm" onClick={() => copyCommand(item)}><Copy />一键部署</Button>
              <ProbeCard item={item} open={deploy?.id === item.id} onOpenChange={(open) => setDeploy(open ? item : null)} deploymentSettings={deploy?.id === item.id ? deploymentSettings : null} busy={pending} onSave={(config) => startTransition(() => save(item, config))} onDelete={() => startTransition(() => remove(item))} />
              <Link className="text-primary hover:underline" href={`/latency-probes/${item.id}`}>延迟历史</Link>
            </div></td>
          </tr>)}</tbody>
        </table></div> : <div className="rounded-xl border border-dashed bg-white p-8 text-center text-sm text-muted-foreground">还没有探针。创建后可获取一键部署命令。</div>}
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
  open,
  onOpenChange,
  deploymentSettings,
  busy,
  onSave,
  onDelete,
}: {
  item: LatencyProbe;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  deploymentSettings: ReactNode;
  busy: boolean;
  onSave: (config: Config) => void;
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
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild><Button size="sm" variant="outline">编辑</Button></DialogTrigger>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
      <DialogHeader><DialogTitle>编辑探针 · {item.name}</DialogTitle><DialogDescription>间隔、超时和网卡配置会在下一轮检测时生效。</DialogDescription></DialogHeader>
      <form onSubmit={(event) => { event.preventDefault(); onSave(config); }} className="space-y-4">
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
          <Input value={config.interface_name} maxLength={100} placeholder="留空自动选择物理网卡，例如 en0 / eth0" onChange={(e) => setConfig({ ...config, interface_name: e.target.value })} />
        </Field>
        <div className="flex items-end pb-2">
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={config.enabled} onChange={(e) => setConfig({ ...config, enabled: e.target.checked })} />
            启用探针
          </label>
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2 border-t pt-3">
        <Button type="submit" size="sm" disabled={busy || !changed}>
          <Save />保存配置
        </Button>
        <Button size="sm" type="button" variant="ghost" className="text-destructive" disabled={busy} onClick={onDelete}>
          <Trash2 />删除
        </Button>
        <Link className="ml-auto inline-flex items-center gap-1 text-sm text-primary hover:underline" href={`/latency-probes/${item.id}`}>
          查看延迟历史
        </Link>
      </div>
    </form>
    {deploymentSettings}
    </DialogContent></Dialog>
  );
}

function Field({ label, children }: { label: string; children: ReactElement<{ id?: string }> }) {
  const id = useId();
  return (
    <div className="space-y-2">
      <Label htmlFor={id}>{label}</Label>
      {cloneElement(children, { id })}
    </div>
  );
}
