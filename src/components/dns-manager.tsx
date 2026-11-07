"use client";

import { useEffect, useState, useTransition } from "react";
import { ArrowRight, Check, Clock3, Globe, History, ListFilter, Loader2, RefreshCw, Search, Settings2, ShieldCheck } from "lucide-react";
import { Tabs } from "radix-ui";
import { cn } from "@/lib/utils";
import { DNSIPDiff } from "@/components/dns-ip-diff";
import { toast } from "sonner";
import { getDNSStatus, requestDNSRun, saveDNSConfig } from "@/app/dns-actions";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import type { DNSConfig, DNSStatus, DNSReport, DNSRecord } from "@/lib/dns";
import type { LatencyProbe } from "@/lib/types";
import { protocolLabels } from "@/lib/protocol";
import { useListOptions } from "@/components/use-list-options";
import { ListPagination } from "@/components/list-pagination";
import { usePagedList } from "@/components/use-paged-list";
import type { PageResult } from "@/lib/pagination";

const emptyList:PageResult<never>={items:[],page:1,pageSize:50,total:0};

const actionNames: Record<string, string> = { create: "新增", update: "更新", delete: "移除", hold: "保留", unchanged: "一致" };
const stateNames: Record<string, string> = { healthy: "符合条件", failed: "不可用", unknown: "待确认", pending: "超过延迟门槛" };
const timeText = (value?: string) => value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "—";
function countryName(code: string) {
  try { return new Intl.DisplayNames(["zh-CN"], { type: "region" }).of(code) || code; }
  catch { return code || "未知"; }
}

function sameDNSConfig(a: DNSConfig, b: DNSConfig) {
  return (Object.keys(a) as (keyof DNSConfig)[]).every(key => a[key] === b[key])
    && (Object.keys(b) as (keyof DNSConfig)[]).every(key => a[key] === b[key]);
}

export function DNSManager({ initialStatus }: { initialStatus: DNSStatus }) {
  const [status, setStatus] = useState(initialStatus);
  const [config, setConfig] = useState(initialStatus.config);
  const records=usePagedList<DNSRecord>("dns-records",emptyList,{},5000);
  const history=usePagedList<DNSStatus["history"][number]>("dns-history",emptyList,{},10000);
  const [tab, setTab] = useState("records");
  const probeOptions=useListOptions<LatencyProbe>("latency-probes",config.probe_id ? [config.probe_id] : []);
  const probes=[...probeOptions.data.items];
  const selectedProbe=probeOptions.items.find(p=>p.id===config.probe_id);
  if(selectedProbe && !probes.some(p=>p.id===selectedProbe.id))probes.push(selectedProbe);
  const [pending, startTransition] = useTransition();
  const [pollError, setPollError] = useState("");
  const busy = status.running || !!status.requested;
  const dirty = !sameDNSConfig(config, status.config);
  const report = status.report;
  const reportOutdated = !!report.domain && (report.domain !== status.config.domain || !!report.config && !sameDNSConfig(report.config, status.config));

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
    <section className="overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-sm">
      <div className="flex flex-wrap items-start justify-between gap-5 bg-gradient-to-r from-blue-50/80 via-white to-white px-5 py-6 sm:px-6">
        <div className="flex items-start gap-4">
          <div className="hidden size-12 shrink-0 items-center justify-center rounded-2xl border border-blue-100 bg-white text-blue-600 shadow-sm sm:flex"><Globe className="size-6" /></div>
          <div><div className="mb-2 flex flex-wrap items-center gap-2"><span className="text-[11px] font-semibold tracking-widest text-blue-700">DNS CONTROL</span><Badge variant="outline" className={cn("gap-1 border-0 text-[11px]", status.credentials_ready ? "bg-emerald-50 text-emerald-700" : "bg-amber-50 text-amber-800")}><ShieldCheck className="size-3" />{status.credentials_ready ? "华为云凭据已配置" : "待配置华为云"}</Badge></div>
            <h1 className="text-2xl font-semibold tracking-tight">域名解析</h1>
            <p className="mt-2 max-w-xl text-sm leading-6 text-muted-foreground">将探针验证可用的 IP 按地区发布，让每一次解析变化都清晰可见。</p>
          </div>
        </div>
        <div className="flex gap-2 self-center"><Button variant="outline" className="bg-white" disabled={pending || busy || dirty || !status.credentials_ready} onClick={() => run("preview")}><Search />预览变化</Button><Button disabled={pending || busy || dirty || !status.credentials_ready} onClick={() => run("sync")}><RefreshCw className={cn(busy && "animate-spin motion-reduce:animate-none")} />立即同步</Button></div>
      </div>
      <div className="grid grid-cols-2 divide-x divide-slate-100 border-t border-slate-100 bg-slate-50/50 lg:grid-cols-4">
        <div className="px-5 py-4 sm:px-6"><p className="mb-1.5 text-xs text-slate-500">托管域名</p><p className="truncate font-mono text-sm font-medium" title={status.config.domain}>{status.config.domain || "尚未配置"}</p></div>
        <div className="px-5 py-4 sm:px-6"><p className="mb-1.5 text-xs text-slate-500">本轮解析记录</p><p className="text-sm font-medium"><span className="mr-1.5 text-lg tabular-nums">{records.data.total}</span><span className="text-xs font-normal text-slate-500">条记录</span></p></div>
        <div className="border-t border-slate-100 px-5 py-4 sm:px-6 lg:border-t-0"><p className="mb-1.5 text-xs text-slate-500">定时同步</p><p className="flex items-center gap-2 text-sm font-medium"><span className={cn("size-1.5 rounded-full", status.config.enabled ? "bg-emerald-500" : "bg-slate-400")} />{status.config.enabled ? `每 ${status.config.interval_seconds} 秒` : "已关闭"}</p></div>
        <div className="border-t border-slate-100 px-5 py-4 sm:px-6 lg:border-t-0"><p className="mb-1.5 text-xs text-slate-500">最近任务</p><p className="text-sm font-medium">{busy ? "正在处理…" : report.finished_at ? timeText(report.finished_at) : "尚无任务"}</p></div>
      </div>
    </section>
    {!status.credentials_ready && <div className="rounded-xl border border-amber-200 bg-amber-50 p-4 text-sm leading-6 text-amber-900">请先在服务器配置华为云 DNS 凭据，完成连接后即可预览和同步。</div>}
    <div aria-live="polite" className="space-y-2 text-sm">
      {busy && <p className="flex items-center gap-2 rounded-xl border border-blue-100 bg-blue-50 px-4 py-3 text-blue-800"><Loader2 className="size-4 animate-spin motion-reduce:animate-none" />{status.running ? "正在检查探针与云端解析…" : "任务已排队…"}</p>}
      {dirty && <div className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-amber-200 bg-amber-50 px-4 py-2 text-amber-900"><span>策略有未保存的修改，保存后可预览或同步。</span><Button variant="ghost" size="sm" onClick={() => setTab("policy")}>查看策略<ArrowRight className="size-3.5" /></Button></div>}
      {pollError && <p role="alert" className="text-destructive">{pollError}</p>}
      {reportOutdated && <p className="text-amber-700">这是旧策略的结果，请重新预览当前策略。</p>}
      {report.error && <p role="alert" className="text-destructive">{busy ? `上次任务失败：${report.error}` : report.error}</p>}
      {report.warning && <p className="text-amber-700">{report.warning}</p>}
    </div>
    <Tabs.Root value={tab} onValueChange={setTab} className="space-y-5">
      <div className="overflow-x-auto"><Tabs.List aria-label="DNS 管理" className="inline-flex min-w-max gap-1 rounded-xl border border-slate-200 bg-slate-100/80 p-1.5">
        {[
          { value: "records", label: "解析记录", Icon: Globe, count: records.data.total },
          { value: "policy", label: "策略设置", Icon: Settings2, count: dirty ? "未保存" : undefined },
          { value: "candidates", label: "筛选明细", Icon: ListFilter, count: report.candidate_count || 0 },
          { value: "history", label: "任务历史", Icon: History, count: history.data.total },
        ].map(({ value, label, Icon, count }) => <Tabs.Trigger key={value} value={value} className="flex h-11 items-center gap-2 rounded-lg px-3 text-sm font-medium text-slate-500 outline-none transition-colors hover:text-slate-900 focus-visible:ring-2 focus-visible:ring-blue-500 data-[state=active]:bg-white data-[state=active]:text-blue-700 data-[state=active]:shadow-sm sm:px-4">
          <Icon className="size-4" />{label}{count !== undefined && <span className={cn("rounded-md bg-slate-200/60 px-1.5 py-0.5 text-[11px] tabular-nums", value === "policy" && "bg-amber-100 text-amber-800")}>{count}</span>}
        </Tabs.Trigger>)}
      </Tabs.List></div>
      <Tabs.Content value="records" className="outline-none focus-visible:ring-2 focus-visible:ring-blue-500">
        <section className="overflow-hidden rounded-xl border bg-white shadow-sm">
          <div className="flex flex-wrap items-center justify-between gap-3 border-b px-5 py-5 sm:px-6"><div><h2 className="flex items-center gap-2 font-semibold">{report.mode === "preview" ? "同步预览" : "最近同步结果"}<Badge variant="outline" className="font-normal">{report.mode === "preview" ? "仅预览" : "任务快照"}</Badge></h2><p className="mt-1.5 text-xs leading-5 text-muted-foreground">{report.started_at ? `${report.domain} · ${timeText(report.started_at)}` : "点击「预览变化」，查看即将发布的解析。"}</p></div><p className="flex items-center gap-1.5 text-xs text-slate-500"><ArrowRight className="size-3.5" />点击 IP 对比查看两侧差异</p></div>
          {records.error && <p role="alert" className="px-5 py-3 text-sm text-destructive">{records.error}</p>}
          <Table><TableHeader><TableRow className="bg-slate-50/80"><TableHead className="pl-5 sm:pl-6">域名 / 地区</TableHead><TableHead>记录类型</TableHead><TableHead>IP 对比</TableHead><TableHead className="pr-5 sm:pr-6">处理结果</TableHead></TableRow></TableHeader><TableBody>
            {records.data.items.map(r => <TableRow key={`${r.name}/${r.type}`} className="h-24"><TableCell className="pl-5 sm:pl-6"><p className="font-mono text-[13px] font-medium">{r.name}</p><p className="mt-1.5 text-xs text-muted-foreground">{countryName(r.country)}</p></TableCell><TableCell><Badge variant="outline" className="rounded-md border-slate-200 bg-slate-50 font-mono text-slate-600">{r.type}</Badge></TableCell>
              <TableCell><DNSIPDiff record={r} preview={report.mode === "preview"} timestamp={report.started_at} /></TableCell>
              <TableCell className="max-w-72 whitespace-normal pr-5 sm:pr-6"><Badge variant="outline" className={cn("gap-1.5 border-0 font-normal", r.action === "delete" ? "bg-rose-50 text-rose-700" : ["create", "update"].includes(r.action) ? "bg-blue-50 text-blue-700" : r.action === "hold" ? "bg-amber-50 text-amber-800" : "bg-emerald-50 text-emerald-700")}>{r.action === "unchanged" && <Check className="size-3" />}{actionNames[r.action] || r.action}{r.applied && r.action !== "unchanged" ? " · 已提交" : report.mode === "preview" ? " · 预览" : !r.applied && ["create", "update", "delete"].includes(r.action) ? " · 未执行" : ""}</Badge>{r.reason && <p className="mt-2 text-xs leading-5 text-muted-foreground">{r.reason}</p>}</TableCell>
            </TableRow>)}
            {!records.data.items.length && <TableRow><TableCell colSpan={4} className="py-16 text-center"><span className="mx-auto mb-4 flex size-12 items-center justify-center rounded-2xl bg-blue-50 text-blue-500"><Globe className="size-6" /></span><p className="font-medium text-slate-700">{records.loading ? "正在读取解析记录…" : report.error ? "上次任务未完成，未生成解析计划" : report.finished_at ? "本轮没有可发布的地区记录" : "从一次预览开始"}</p><p className="mt-2 text-xs text-muted-foreground">{report.error ? "空快照不代表云端解析为空；请查看错误信息或任务历史。" : report.finished_at ? "切换到「筛选明细」查看 IP 未被选中的原因。" : "保存策略后预览，即可对比云端与计划 IP。"}</p></TableCell></TableRow>}
          </TableBody></Table>
          <ListPagination data={records.data} onPage={records.setPage} loading={records.loading}/>
          <div className="flex items-start gap-2 border-t border-slate-100 bg-slate-50/60 px-5 py-4 text-xs leading-5 text-muted-foreground sm:px-6"><Clock3 className="mt-0.5 size-3.5 shrink-0" />云端写入后仍需等待 DNS 缓存过期；当前 TTL 为 {status.config.ttl} 秒。预览仅查询，不修改解析。</div>
        </section>
      </Tabs.Content>
      <Tabs.Content value="policy" className="outline-none focus-visible:ring-2 focus-visible:ring-blue-500">
    <form className="space-y-5 rounded-xl border bg-white p-5" onSubmit={event => { event.preventDefault(); save(); }}>
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div><h2 className="font-semibold">自动解析策略</h2><p className="mt-1 text-xs text-muted-foreground">选择发布条件，保存后预览解析变化。</p></div>
        <div className="flex items-center gap-3"><Label htmlFor="dns-enabled">定时同步</Label><Switch id="dns-enabled" checked={config.enabled} disabled={pending || busy} onCheckedChange={value => update("enabled", value)} /></div>
      </div>
      <fieldset disabled={pending || busy} className="grid gap-5 sm:grid-cols-2 lg:grid-cols-3">
        <div className="space-y-2"><Label htmlFor="dns-domain">托管域名</Label><Input id="dns-domain" value={config.domain} required placeholder="reiiv.com" onChange={e => update("domain", e.target.value)} /></div>
        {([
          ["interval_seconds", "更新间隔（秒）", 60, 86400], ["ttl", "TTL（秒）", 60, 86400],
          ["max_ips", "每个地区每种记录最多 IP 数", 1, 50], ["min_success_percent", "最低采样成功率（%）", 1, 100], ["window_minutes", "探针观察窗口（分钟）", 2, 60],
        ] as const).map(([key, label, min, max]) => <div key={key} className="space-y-2"><Label htmlFor={`dns-${key}`}>{label}</Label><Input id={`dns-${key}`} type="number" required min={min} max={max} value={config[key]} onChange={e => update(key, Number(e.target.value))} /></div>)}
        <div className="space-y-2"><Label htmlFor="dns-probe">依据探针</Label>
          <Select value={config.probe_id || "any"} onValueChange={v => update("probe_id", v === "any" ? "" : v)} disabled={pending || busy}>
            <SelectTrigger id="dns-probe"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="any">任一在线探针验证通过</SelectItem>
              <Input aria-label="搜索探针" placeholder="搜索探针名称" value={probeOptions.search} onChange={e=>probeOptions.setSearch(e.target.value)} onKeyDown={e=>e.stopPropagation()}/>
              {probes.map(p => <SelectItem key={p.id} value={p.id} disabled={!p.enabled}>{p.name}{!p.enabled ? "（停用）" : !p.online ? "（离线）" : ""}</SelectItem>)}
              {probeOptions.error && <p role="alert" className="text-sm text-destructive">{probeOptions.error}</p>}
              <ListPagination data={probeOptions.data} onPage={probeOptions.setPage} loading={probeOptions.loading}/>
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
      <fieldset disabled={pending || busy} className="space-y-4 rounded-lg border bg-slate-50/50 p-4">
        <div className="flex items-center justify-between gap-4"><div><Label htmlFor="dns-website-enabled">SaaS 网站优选</Label><p className="mt-1 text-xs text-muted-foreground">向 url.{config.domain || "托管域名"} 发布通过 HTTPS 和稳定性验证的 IP。</p></div><Switch id="dns-website-enabled" checked={!!config.website_enabled} onCheckedChange={v => update("website_enabled", v)} /></div>
        {config.website_enabled && <div className="space-y-2"><Label htmlFor="dns-website-url">实际业务 HTTPS 检测地址</Label><Input id="dns-website-url" type="url" required value={config.website_url || ""} placeholder="https://api.example.com/" onChange={e => update("website_url", e.target.value)} /><p className="text-xs leading-5 text-muted-foreground">填写 SaaS 已绑定的业务域名及可访问路径。每轮同步保留该域名的 Host/SNI，指定 IP 验证证书与 HTTP 200–399 响应，不跟随重定向。HTTP 80 端口不在此策略的保障范围内。</p></div>}
        <p className="text-xs leading-5 text-muted-foreground">HTTPS 连续两次失败且存在健康对照 IP 时移出；短暂失败或所有地址同时检测失败时保留已有解析、暂停新增。</p>
      </fieldset>
      <p className="text-xs leading-6 text-muted-foreground">仅发布当前可用池中未屏蔽、通过证书检查的 IP。用成功采样的延迟按 P95 去除峰值后计算平均值，每个国家或地区独立以本地区最快 IP 的平均值为基准：亚太允许本地区基准 +10 ms，其他地区允许本地区基准 +20 ms。例如香港最低 38 ms，则香港选取 ≤48 ms；美国最低 140 ms，则美国选取 ≤160 ms。代理池与 CF 池分别筛选。观察窗口成功率必须达到策略门槛，失败样本不参与延迟计算但计入成功率。连续失败满 5 分钟且探针有健康对照时移出。节点发布前使用 Mihomo 复查已导入的 TLS WS、gRPC、XHTTP、HTTPUpgrade 节点。节点和网站入口均需连续通过 3 轮、每轮间隔至少 5 分钟、覆盖至少 10 分钟才可新增或恢复；存在健康对照时，入口复查持续待确认超过 30 分钟会移出。</p>
      <p className="text-xs leading-6 text-muted-foreground">接管对应国家或地区的「全网默认」A / AAAA 记录，保留其他线路和其他子域名。已接管记录没有合格 IP 时会移除；单个 IP 采样或定位待确认时，仅保留它的已有解析，不阻止其他地址更新。指定依据探针后，仅使用该探针的数据；该探针离线或缺少新鲜采样时，暂停全部 DNS 同步，包括网站解析，保留当前解析。关闭定时同步会保留当前解析。</p>
      <div className="flex flex-wrap items-center justify-between gap-3"><span className="text-xs text-muted-foreground">{dirty ? "配置有修改，请先保存再预览或同步。" : config.enabled ? `下次同步：${timeText(status.next_run_at)}` : "定时同步已关闭，可手动预览或同步。"}</span>
        <Button type="submit" disabled={pending || busy || !dirty}>{pending && <Loader2 className="animate-spin" />}保存策略</Button>
      </div>
    </form>
      </Tabs.Content>
      <Tabs.Content value="candidates" className="rounded-xl border bg-white p-5 outline-none focus-visible:ring-2 focus-visible:ring-blue-500 sm:p-6"><h2 className="font-semibold">IP 筛选明细</h2><p className="mt-1 text-xs leading-5 text-muted-foreground">查看本轮 IP 的采样状态、筛选原因与延迟。</p><DNSCandidates /></Tabs.Content>
      <Tabs.Content value="history" className="rounded-xl border bg-white p-5 outline-none focus-visible:ring-2 focus-visible:ring-blue-500 sm:p-6"><h2 className="font-semibold">任务历史</h2><p className="mt-1 mb-5 text-xs text-muted-foreground">回顾最近的预览和同步任务。</p>{history.data.items.length ? <ul className="divide-y divide-slate-100">{history.data.items.map(h => <li key={h.id} className="flex flex-wrap items-center justify-between gap-3 py-4"><div className="flex items-center gap-3"><span className={cn("flex size-9 items-center justify-center rounded-xl", h.report.error ? "bg-rose-50 text-rose-600" : "bg-blue-50 text-blue-600")}>{h.report.mode === "preview" ? <Search className="size-4" /> : <RefreshCw className="size-4" />}</span><div><p className="text-sm font-medium">{h.report.domain}<span className="ml-2 text-xs font-normal text-muted-foreground">{h.report.mode === "preview" ? "预览" : "同步"}</span></p><p className="mt-1 text-xs text-muted-foreground">{timeText(h.started_at)}</p></div></div><span className={cn("max-w-lg text-xs leading-5", h.report.error ? "text-destructive" : "text-muted-foreground")}>{h.report.error || h.report.warning || "已完成"}</span></li>)}</ul> : <p className="py-12 text-center text-sm text-muted-foreground">{history.loading ? "正在读取任务记录…" : "尚无任务记录，运行预览后会显示在这里。"}</p>}{history.error && <p role="alert" className="text-sm text-destructive">{history.error}</p>}<ListPagination data={history.data} onPage={history.setPage} loading={history.loading}/></Tabs.Content>
    </Tabs.Root>
  </div>;

}


function DNSCandidates() {
  const list=usePagedList<NonNullable<DNSReport["candidates"]>[number]>("dns-candidates",emptyList,{},10000);
  return <div className="mt-4">
    {list.error && <p role="alert" className="text-sm text-destructive">{list.error}</p>}
    <Table><TableHeader><TableRow className="bg-slate-50"><TableHead>IP</TableHead><TableHead>地区</TableHead><TableHead>状态</TableHead><TableHead>说明</TableHead></TableRow></TableHeader><TableBody>{list.data.items.map(ip=><TableRow key={`${ip.pool || "protocol"}/${ip.ip}`}><TableCell className="font-mono">{ip.ip}{ip.pool === "cf" && <span className="mt-1 block font-sans text-xs text-muted-foreground">SaaS 网站池</span>}</TableCell><TableCell>{ip.country ? countryName(ip.country) : "—"}</TableCell><TableCell>{stateNames[ip.state] || ip.state}</TableCell><TableCell className="whitespace-normal text-xs">{ip.reason}{ip.state==="healthy" && ` · 平均 ${ip.delay_ms.toFixed(1)} ms`}{ip.https_state && <p className={cn("mt-1", ip.https_state === "failed" ? "text-destructive" : "text-muted-foreground")} title={ip.https_url}>HTTPS {ip.https_state === "healthy" ? "通过" : ip.https_state === "failed" ? "失败" : "待确认"} · {ip.https_reason}</p>}</TableCell></TableRow>)}{!list.data.items.length && <TableRow><TableCell colSpan={4} className="py-12 text-center text-muted-foreground">{list.loading ? "正在读取筛选明细…" : "本轮暂无 IP 筛选明细。"}</TableCell></TableRow>}</TableBody></Table>
    <ListPagination data={list.data} onPage={list.setPage} loading={list.loading}/>
  </div>;
}
