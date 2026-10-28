"use client";

import Link from "next/link";
import { useId, useState, useTransition, cloneElement, type ReactNode, type ReactElement } from "react";
import { Activity, Copy, Download, KeyRound, Loader2, Plus, Radio, Save, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  createLatencyProbe,
  getProbeDeployment,
  saveProbeDeployment,
  deleteLatencyProbe,
  rotateLatencyProbeToken,
  updateLatencyProbe,
} from "@/app/actions";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ListPagination } from "@/components/list-pagination";
import { usePagedList } from "@/components/use-paged-list";
import type { PageResult } from "@/lib/pagination";
import type { LatencyProbe } from "@/lib/types";

type Config = {
  name: string;
  interval_seconds: number;
  timeout_ms: number;
  concurrency: number;
  interface_name: string;
  mode: "icmp" | "tcp";
  tcp_port: number;
  enabled: boolean;
};

const blank: Config = {
  name: "",
  mode: "tcp",
  tcp_port: 443,
  interval_seconds: 20,
  timeout_ms: 1000,
  concurrency: 64,
  interface_name: "",
  enabled: true,
};

export function LatencyProbeManager({ initial, tlsPin }: { initial: PageResult<LatencyProbe>; tlsPin: string }) {
  const {data,setData,setPage,refresh,loading,error:statusError}=usePagedList("latency-probes",initial,{},10000);
  const items=data.items;
  function setItems(update:(items:LatencyProbe[])=>LatencyProbe[]) {setData(current=>({...current,items:update(current.items)}));refresh();}
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
      setPage(1);
      refresh();
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

  async function openEditor(item: LatencyProbe) {
    setDeploy(item);
    const result = await getProbeDeployment(item.id);
    if (!result.ok) { toast.error(result.error); return; }
    setCredentials((current) => ({ ...current, [item.id]: result.token }));
    setPlatforms((current) => ({ ...current, [item.id]: result.platform }));
  }

  async function copyCommand(item: LatencyProbe) {
    try {
      const result = await getProbeDeployment(item.id);
      if (!result.ok) { toast.error(result.error); return; }
      if (!result.token) {
        toast.error("旧探针密钥尚未保存，请等待探针连接自动补存，或在编辑中填写原密钥保存");
        return;
      }
      await navigator.clipboard.writeText(deploymentCommand(result.token, result.platform));
      toast.success("部署命令已复制，使用已保存的密钥");
    } catch {
      toast.error("复制失败，请在编辑中手动选择命令复制");
    }
  }

  async function saveDeployment() {
    if (!deploy) return;
    const result = await saveProbeDeployment(deploy.id, currentToken, platform);
    if (!result.ok) { toast.error(result.error); return; }
    toast.success("部署配置已保存，刷新后可继续复制，原密钥保持有效");
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
            <Input type="password" autoComplete="off" value={currentToken} placeholder="服务器已保存的密钥；旧探针可填写原密钥补存" onChange={(event) => deploy && setCredentials((current) => ({ ...current, [deploy.id]: event.target.value.trim() }))} />
          </Field>
          {!currentToken && <p className="text-xs text-muted-foreground">旧版探针连接成功后会自动补存原密钥，也可填写原密钥后保存，无需重新生成。</p>}
          <Button variant="outline" disabled={pending} onClick={() => deploy && window.confirm("重新生成将使此探针的旧密钥立即失效，确定继续？") && startTransition(() => rotate(deploy))}><KeyRound />重新生成密钥</Button>
          <Button disabled={pending} onClick={() => startTransition(saveDeployment)}><Save />保存部署配置</Button>
          <div className="space-y-3 rounded-lg border p-3">
            <p className="text-sm font-medium">一键部署命令</p>
            <p className="text-xs text-muted-foreground">{currentToken ? "密钥在服务器加密保存，刷新后可继续复制。修改系统或补填原密钥后，请保存部署配置；只有手动重新生成才会更换密钥。" : "等待旧探针连接自动补存，或填写原密钥并保存；下方为命令预览。"}</p>
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
          本机延迟探针
        </div>
        <h1 className="text-2xl font-semibold tracking-tight">延迟探针</h1>
        <p className="mt-2 max-w-3xl text-sm leading-6 text-muted-foreground">
          在电脑运行探针后，可选择 TCP 建连延迟或 ICMP 往返延迟，定期检测当前可用 IP 并上报。检测
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
          <MeasurementFields config={draft} onChange={setDraft} />
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
          <Field label="并发检测数">
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
          <span className="text-xs text-muted-foreground">{data.total} 个</span>
        </div>
        <p className="text-xs text-muted-foreground">在线状态每 10 秒刷新；IP 检测超时不代表探针离线。</p>
        {statusError && <p role="alert" className="text-sm text-amber-700">{statusError}</p>}
        {items.length ? <div className="overflow-x-auto rounded-xl border bg-white"><table className="w-full whitespace-nowrap text-sm">
          <thead className="bg-slate-50 text-left text-muted-foreground"><tr>{["名称", "状态", "检测方式", "检测间隔", "出口网卡", "最后连接", "操作"].map((title) => <th key={title} className="px-4 py-3 font-medium">{title}</th>)}</tr></thead>
          <tbody>{items.map((item) => <tr key={item.id} className="border-t">
            <td className="px-4 py-3 font-medium">{item.name}</td>
            <td className="px-4 py-3"><Badge variant={item.online ? "default" : "secondary"}>{!item.enabled ? "已停用" : item.online ? "在线" : "离线"}</Badge></td>
            <td className="px-4 py-3">{item.mode === "tcp" ? `TCP :${item.tcp_port}` : "ICMP Ping"}</td>
            <td className="px-4 py-3">{item.interval_seconds} 秒</td>
            <td className="px-4 py-3">{item.interface_name || "自动选择物理网卡"}</td>
            <td className="px-4 py-3 text-muted-foreground">{item.last_seen ? new Date(item.last_seen).toLocaleString("zh-CN", { hour12: false }) : "尚未连接"}</td>
            <td className="px-4 py-3"><div className="flex items-center gap-2">
              <Button size="sm" onClick={() => copyCommand(item)}><Copy />一键部署</Button>
              <ProbeCard item={item} open={deploy?.id === item.id} onOpenChange={(open) => open ? startTransition(() => openEditor(item)) : setDeploy(null)} deploymentSettings={deploy?.id === item.id ? deploymentSettings : null} busy={pending} onSave={(config) => startTransition(() => save(item, config))} onDelete={() => startTransition(() => remove(item))} />
              <Link className="text-primary hover:underline" href={`/latency-probes/${item.id}`}>延迟历史</Link>
            </div></td>
          </tr>)}</tbody>
        </table></div> : <div className="rounded-xl border border-dashed bg-white p-8 text-center text-sm text-muted-foreground">还没有探针。创建后可获取一键部署命令。</div>}
        <ListPagination data={data} onPage={setPage} loading={loading}/>
      </section>

      <div className="rounded-lg border bg-slate-50 p-4 text-xs leading-5 text-muted-foreground">
        <div className="mb-1 flex items-center gap-2 font-medium text-foreground">
          <Activity className="size-4" />延迟口径
        </div>
        TCP Ping 测量探针电脑到指定端口的 TCP 建连时间，ICMP 测量回显往返时间，单位毫秒。端口关闭或屏蔽 ICMP 都可能导致检测失败，不代表 IP 完全不可用。两种模式均绑定出口网卡；切换模式或端口后仅显示匹配配置的历史。已有探针请重新执行一键部署命令升级，再切换 TCP。
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
    mode: item.mode,
    tcp_port: item.tcp_port,
    interval_seconds: item.interval_seconds,
    timeout_ms: item.timeout_ms,
    concurrency: item.concurrency,
    interface_name: item.interface_name,
    enabled: item.enabled,
  });
  const changed = config.mode !== item.mode || config.tcp_port !== item.tcp_port || config.name !== item.name ||
    config.interval_seconds !== item.interval_seconds ||
    config.timeout_ms !== item.timeout_ms ||
    config.concurrency !== item.concurrency ||
    config.interface_name !== item.interface_name ||
    config.enabled !== item.enabled;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild><Button size="sm" variant="outline">编辑</Button></DialogTrigger>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
      <DialogHeader><DialogTitle>编辑探针 · {item.name}</DialogTitle><DialogDescription>配置在下一轮检测时生效。切换 TCP 前，请重新执行一键部署命令升级探针。</DialogDescription></DialogHeader>
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
        <MeasurementFields config={config} onChange={setConfig} />
        <Field label="检测间隔（秒）">
          <Input type="number" min={15} max={86400} value={config.interval_seconds} onChange={(e) => setConfig({ ...config, interval_seconds: Number(e.target.value) })} />
        </Field>
        <Field label="单 IP 超时（毫秒）">
          <Input type="number" min={250} max={10000} value={config.timeout_ms} onChange={(e) => setConfig({ ...config, timeout_ms: Number(e.target.value) })} />
        </Field>
        <Field label="并发检测数">
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

function MeasurementFields({ config, onChange }: { config: Config; onChange: (value: Config) => void }) {
  return <>
    <Field label="检测方式"><select className="h-9 w-full rounded-md border bg-white px-3 text-sm" value={config.mode}
      onChange={event => onChange({ ...config, mode: event.target.value as Config["mode"] })}>
      <option value="tcp">TCP Ping</option><option value="icmp">ICMP Ping</option>
    </select></Field>
    {config.mode === "tcp" && <Field label="TCP 目标端口"><Input type="number" required min={1} max={65535} value={config.tcp_port}
      onChange={event => onChange({ ...config, tcp_port: Number(event.target.value) })} /></Field>}
  </>;
}
