"use client";

import { useEffect, useState, useTransition } from "react";
import { Globe, Loader2, RefreshCw, Search } from "lucide-react";
import { toast } from "sonner";
import { getDNSStatus, requestDNSRun, saveDNSConfig } from "@/app/dns-actions";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import type { DNSConfig, DNSStatus } from "@/lib/dns";
import type { LatencyProbe } from "@/lib/types";
import { protocolLabels } from "@/lib/protocol";

const actionNames: Record<string, string> = { create: "新增", update: "更新", delete: "移除", hold: "保留", unchanged: "一致" };
const stateNames: Record<string, string> = { healthy: "符合条件", failed: "不可用", unknown: "待确认" };
const timeText = (value?: string) => value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "—";
function countryName(code: string) {
  try { return new Intl.DisplayNames(["zh-CN"], { type: "region" }).of(code) || code; }
  catch { return code || "未知"; }
}

export function DNSManager({ initialStatus, probes }: { initialStatus: DNSStatus; probes: LatencyProbe[] }) {
  const [status, setStatus] = useState(initialStatus);
  const [config, setConfig] = useState(initialStatus.config);
  const [pending, startTransition] = useTransition();
  const [pollError, setPollError] = useState("");
  const busy = status.running || !!status.requested;
  const dirty = JSON.stringify(config) !== JSON.stringify(status.config);
  const report = status.report;
  const reportOutdated = !!report.domain && (report.domain !== status.config.domain || !!report.config && JSON.stringify(report.config) !== JSON.stringify(status.config));

  useEffect(() => {
    let active = true;
    const timer = setInterval(async () => {
      try { const next = await getDNSStatus(); if (active) { setStatus(next); setPollError(""); } }
      catch { if (active) setPollError("状态刷新失败，请检查连接；下次自动重试。"); }
    }, 5000);
    return () => { active = false; clearInterval(timer); };
  }, []);

  function update<K extends keyof DNSConfig>(key: K, value: DNSConfig[K]) { setConfig(c => ({ ...c, [key]: value })); }
  function save() {
    startTransition(async () => {
      try {
        const result = await saveDNSConfig(config);
        if (!result.ok) { toast.error(result.error); return; }
        const next = await getDNSStatus(); setStatus(next); setConfig(next.config);
        toast.success("DNS 策略已保存");
      } catch { toast.error("保存或读取配置失败，请刷新确认"); }
    });
  }
  function run(mode: "preview" | "sync") {
    startTransition(async () => {
      try {
        const result = await requestDNSRun(mode);
        if (!result.ok) { toast.error(result.error); return; }
        setStatus(s => ({ ...s, requested: mode }));
        toast.success(mode === "preview" ? "正在生成预览，完成后自动显示" : "已提交同步任务，完成后自动显示结果");
      } catch { toast.error("提交任务失败，请刷新确认"); }
    });
  }

  return <div className="space-y-6">
    <div className="flex flex-wrap items-start justify-between gap-4">
      <div><h1 className="text-2xl font-semibold tracking-tight">域名解析</h1>
        <p className="mt-2 max-w-3xl text-sm leading-6 text-muted-foreground">自动将探针验证可用的 IP 按国家或地区发布到华为云 DNS，例如 hk.{config.domain}、jp.{config.domain}。发现新地区时自动创建 A / AAAA 记录。</p>
      </div>
      <Badge variant="outline">华为云 DNS · {status.credentials_ready ? "凭据已配置" : "待配置凭据"}</Badge>
    </div>
    {!status.credentials_ready && <div className="rounded-xl border border-amber-200 bg-amber-50 p-5 text-sm leading-6">
      请在 Go 服务的环境变量配置 <code>HUAWEICLOUD_SDK_AK</code>、<code>HUAWEICLOUD_SDK_SK</code> 并重启。账号需要拥有所选域名的 DNS 查询及记录增删改权限。密钥仅在服务器读取。
    </div>}
    <form className="space-y-5 rounded-xl border bg-white p-5" onSubmit={event => { event.preventDefault(); save(); }}>
      <div className="flex flex-wrap items-center justify-between gap-4">
        <h2 className="font-semibold">自动解析策略</h2>
        <div className="flex items-center gap-3"><Label htmlFor="dns-enabled">定时同步</Label><Switch id="dns-enabled" checked={config.enabled} disabled={pending || busy} onCheckedChange={value => update("enabled", value)} /></div>
      </div>
      <fieldset disabled={pending || busy} className="grid gap-5 sm:grid-cols-2 lg:grid-cols-3">
        <div className="space-y-2"><Label htmlFor="dns-domain">托管域名</Label><Input id="dns-domain" value={config.domain} required placeholder="reiiv.com" onChange={e => update("domain", e.target.value)} /></div>
        {([
          ["interval_seconds", "更新间隔（秒）", 60, 86400], ["ttl", "TTL（秒）", 60, 86400],
          ["max_ips", "每个地区每种记录最多 IP 数", 1, 50], ["window_minutes", "探针观察窗口（分钟）", 2, 60],
          ["min_success_percent", "最低采样成功率（%，0 为仅排除持续失败）", 0, 100],
        ] as const).map(([key, label, min, max]) => <div key={key} className="space-y-2"><Label htmlFor={`dns-${key}`}>{label}</Label><Input id={`dns-${key}`} type="number" required min={min} max={max} value={config[key]} onChange={e => update(key, Number(e.target.value))} /></div>)}
        <div className="space-y-2"><Label htmlFor="dns-probe">依据探针</Label>
          <Select value={config.probe_id || "any"} onValueChange={v => update("probe_id", v === "any" ? "" : v)} disabled={pending || busy}>
            <SelectTrigger id="dns-probe"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="any">任一在线探针验证通过</SelectItem>
              {probes.map(p => <SelectItem key={p.id} value={p.id} disabled={!p.enabled}>{p.name}{!p.enabled ? "（停用）" : !p.online ? "（离线）" : ""}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-2"><Label htmlFor="dns-protocol">可用 IP 协议池</Label>
          <Select value={config.protocol || "any"} onValueChange={v => update("protocol", v === "any" ? "" : v)} disabled={pending || busy}>
            <SelectTrigger id="dns-protocol"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="any">任一协议可用</SelectItem>
              {Object.entries(protocolLabels).map(([v, label]) => <SelectItem key={v} value={v}>{label}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>
      </fieldset>
      <p className="text-xs leading-6 text-muted-foreground">仅发布当前可用池中未屏蔽、通过证书检查的 IP。探针需有至少两次、跨度不少于 1 分钟的新鲜采样。成功率设为 0 时，近期测通且未连续失败满 60 秒即可加入，容忍短暂丢包；设为正数时，需最新采样成功且成功率达标。同一地区的所有达标 IP 都会加入，超过数量上限时优先选择平均延迟较低的 IP。</p>
      <p className="text-xs leading-6 text-muted-foreground">接管对应国家或地区的「全网默认」A / AAAA 记录，保留其他线路和其他子域名。已接管记录没有合格 IP 时会移除；采样待确认的已有 IP 暂时保留；探针整体离线或归属地查询失败时保留原解析。关闭定时同步会保留当前解析。</p>
      <div className="flex flex-wrap items-center justify-between gap-3"><span className="text-xs text-muted-foreground">{dirty ? "配置有修改，请先保存再预览或同步。" : config.enabled ? `下次同步：${timeText(status.next_run_at)}` : "定时同步已关闭，可手动预览或同步。"}</span>
        <Button type="submit" disabled={pending || busy || !dirty}>{pending && <Loader2 className="animate-spin" />}保存策略</Button>
      </div>
    </form>
    <section className="overflow-hidden rounded-xl border bg-white">
      <div className="flex flex-wrap items-center justify-between gap-4 border-b p-5"><div><h2 className="font-semibold">{report.mode === "preview" ? "同步预览" : "最近同步结果"}</h2><p className="mt-1 text-xs text-muted-foreground">{report.started_at ? `${report.domain} · ${timeText(report.started_at)}` : "保存策略后，点击预览查看将发布的 IP。"}</p></div>
        <div className="flex flex-wrap gap-2"><Button variant="outline" disabled={pending || busy || dirty || !status.credentials_ready} onClick={() => run("preview")}><Search />预览</Button><Button disabled={pending || busy || dirty || !status.credentials_ready} onClick={() => run("sync")}><RefreshCw />立即同步</Button></div>
      </div>
      <div aria-live="polite" className="space-y-2 px-5 pt-4 text-sm">
        {busy && <p className="flex items-center gap-2"><Loader2 className="size-4 animate-spin" />{status.running ? "正在检查探针与云端解析…" : "任务已排队…"}</p>}
        {pollError && <p role="alert" className="text-destructive">{pollError}</p>}
        {reportOutdated && <p className="text-amber-700">这是旧策略的结果，请重新预览当前策略。</p>}
        {report.error && <p role="alert" className="text-destructive">{report.error}</p>}
        {report.warning && <p className="text-amber-700">{report.warning}</p>}
      </div>
      <Table><TableHeader><TableRow><TableHead className="pl-5">域名 / 地区</TableHead><TableHead>类型</TableHead><TableHead>云端 IP</TableHead><TableHead>计划 IP</TableHead><TableHead>处理结果</TableHead></TableRow></TableHeader><TableBody>
        {report.records?.map(r => <TableRow key={`${r.name}/${r.type}`}><TableCell className="pl-5 align-top"><p className="font-mono">{r.name}</p><p className="mt-1 text-xs text-muted-foreground">{countryName(r.country)}</p></TableCell><TableCell className="align-top">{r.type}</TableCell>
          <TableCell className="align-top font-mono text-xs">{r.current.length ? r.current.map(ip => <p key={ip}>{ip}</p>) : "—"}</TableCell><TableCell className="align-top font-mono text-xs">{r.desired.length ? r.desired.map(ip => <p key={ip}>{ip}</p>) : "—"}</TableCell>
          <TableCell className="max-w-64 whitespace-normal align-top"><Badge variant="outline">{actionNames[r.action] || r.action}{r.applied && r.action !== "unchanged" ? " · 已提交" : report.mode === "preview" ? " · 预览" : !r.applied && ["create", "update", "delete"].includes(r.action) ? " · 未执行" : ""}</Badge>{r.reason && <p className="mt-2 text-xs text-muted-foreground">{r.reason}</p>}</TableCell>
        </TableRow>)}
        {!report.records?.length && <TableRow><TableCell colSpan={5} className="py-10 text-center text-muted-foreground"><Globe className="mx-auto mb-3 size-6" />{report.finished_at ? "本轮没有可发布的地区记录。可在下方查看 IP 筛选结果。" : "尚无同步结果"}</TableCell></TableRow>}
      </TableBody></Table>
      <p className="p-5 text-xs text-muted-foreground">云端写入后仍需等待 DNS 缓存过期；当前 TTL 为 {status.config.ttl} 秒。预览仅查询，不修改解析。</p>
    </section>
    {!!report.candidates?.length && <details className="rounded-xl border bg-white p-5"><summary className="cursor-pointer text-sm font-medium">IP 筛选明细（{report.candidates.length} 个）</summary><Table className="mt-4"><TableHeader><TableRow><TableHead>IP</TableHead><TableHead>地区</TableHead><TableHead>状态</TableHead><TableHead>说明</TableHead></TableRow></TableHeader><TableBody>{report.candidates.map(ip => <TableRow key={ip.ip}><TableCell className="font-mono">{ip.ip}</TableCell><TableCell>{ip.country ? countryName(ip.country) : "—"}</TableCell><TableCell>{stateNames[ip.state] || ip.state}</TableCell><TableCell className="whitespace-normal text-xs">{ip.reason}{ip.state === "healthy" && ` · 平均 ${ip.delay_ms.toFixed(1)} ms`}</TableCell></TableRow>)}</TableBody></Table></details>}
    <section className="rounded-xl border bg-white p-5"><h2 className="mb-4 font-semibold">最近任务</h2>{status.history.length ? <ul className="divide-y">{status.history.map(h => <li key={h.id} className="flex flex-wrap justify-between gap-2 py-3 text-xs"><span>{timeText(h.started_at)} · {h.report.domain} · {h.report.mode === "preview" ? "预览" : "同步"}</span><span className={h.report.error ? "text-destructive" : "text-muted-foreground"}>{h.report.error || h.report.warning || "已完成"}</span></li>)}</ul> : <p className="text-sm text-muted-foreground">尚无任务记录</p>}</section>
  </div>;
}
