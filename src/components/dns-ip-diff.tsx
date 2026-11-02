"use client";

import { useState } from "react";
import { ArrowRight, Check, Cloud, Copy, GitCompareArrows, ListFilter, Plus, Minus } from "lucide-react";
import { toast } from "sonner";
import type { DNSRecord } from "@/lib/dns";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";

export function DNSIPDiff({ record, preview, timestamp }: { record: DNSRecord; preview: boolean; timestamp?: string }) {
  const [snapshot, setSnapshot] = useState({ record, preview, timestamp });
  const [changesOnly, setChangesOnly] = useState(false);
  const r = snapshot.record;
  const current = [...new Set(r.current)];
  const desired = [...new Set(r.desired)];
  const currentSet = new Set(current);
  const desiredSet = new Set(desired);
  const sort = (a: string, b: string) => a.localeCompare(b, "en", { numeric: true });
  const removed = current.filter(ip => !desiredSet.has(ip)).sort(sort);
  const added = desired.filter(ip => !currentSet.has(ip)).sort(sort);
  const common = current.filter(ip => desiredSet.has(ip)).sort(sort);
  const rows = Array.from({ length: Math.max(removed.length, added.length) }, (_, i) => ({
    left: removed[i], right: added[i], changed: true,
  })).concat(changesOnly ? [] : common.map(ip => ({ left: ip, right: ip, changed: false })));

  async function copy(ips: string[], side: string) {
    try { await navigator.clipboard.writeText(ips.join("\n")); toast.success(`已复制${side} IP`); }
    catch { toast.error("复制失败，请选择 IP 文本手动复制"); }
  }

  return <Dialog onOpenChange={open => {
    if (open) setSnapshot({ record: { ...record, current: [...record.current], desired: [...record.desired] }, preview, timestamp });
    else setChangesOnly(false);
  }}>
    <DialogTrigger asChild>
      <Button variant="outline" size="sm" className="h-9 gap-2 border-slate-200 text-slate-600 hover:border-blue-200 hover:bg-blue-50 hover:text-blue-700" aria-label={`对比 ${record.name} ${record.type} 的 IP`}>
        <GitCompareArrows className="size-3.5" />IP 对比
        <span className="flex items-center gap-1 font-mono text-xs tabular-nums"><span>{record.current.length}</span><ArrowRight className="size-3" /><span>{record.desired.length}</span></span>
      </Button>
    </DialogTrigger>
    <DialogContent className="max-h-[90dvh] gap-0 overflow-hidden rounded-2xl p-0 sm:max-w-4xl">
      <DialogHeader className="border-b px-5 py-5 text-left sm:px-6">
        <div className="mb-2 flex items-center gap-2 text-xs font-medium text-blue-700"><GitCompareArrows className="size-4" />解析 IP 对比<Badge variant="outline" className="font-mono">{r.type}</Badge></div>
        <DialogTitle className="break-all pr-5 font-mono text-base leading-6 sm:text-lg">{r.name}</DialogTitle>
        <DialogDescription className="text-xs leading-5">
          {snapshot.preview ? "预览快照" : "同步任务快照"}{snapshot.timestamp ? ` · ${new Date(snapshot.timestamp).toLocaleString("zh-CN", { hour12: false })}` : ""}。左侧为本轮读取的云端 IP，右侧为本轮计划；打开对比不会修改解析。
        </DialogDescription>
      </DialogHeader>
      <div className="flex flex-wrap items-center justify-between gap-3 bg-slate-50/80 px-5 py-3 sm:px-6">
        <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
          <span className="flex items-center gap-1 text-rose-700"><Minus className="size-3.5" />仅云端 {removed.length}</span>
          <span className="flex items-center gap-1 text-emerald-700"><Plus className="size-3.5" />仅计划 {added.length}</span>
          <span className="flex items-center gap-1 text-slate-500"><Check className="size-3.5" />共有 {common.length}</span>
        </div>
        <Label className="flex cursor-pointer items-center gap-2 text-xs"><ListFilter className="size-3.5" />仅看差异<Switch size="sm" checked={changesOnly} onCheckedChange={setChangesOnly} /></Label>
      </div>
      <div className="max-h-[52dvh] overflow-auto" tabIndex={0} aria-label="云端与计划 IP 并排对比">
        <div className="min-w-[320px]">
          <div className="sticky top-0 z-10 grid grid-cols-2 border-y bg-white text-sm">
            {[{ title: "云端 IP", ips: current, Icon: Cloud }, { title: "计划 IP", ips: desired, Icon: GitCompareArrows }].map(({ title, ips, Icon }, i) => <div key={title} className={cn("flex items-center justify-between gap-1 px-2 py-3 sm:gap-2 sm:px-4", !i && "border-r")}>
              <span className="flex items-center gap-1 whitespace-nowrap font-medium sm:gap-2"><Icon className="hidden size-4 text-slate-400 sm:block" />{title}<span className="rounded-md bg-slate-100 px-1.5 py-0.5 text-xs font-normal tabular-nums text-slate-500">{ips.length}</span></span>
              <Button variant="ghost" size="icon-sm" disabled={!ips.length} aria-label={`复制${title}`} onClick={() => copy(ips, title)}><Copy className="size-3.5" /></Button>
            </div>)}
          </div>
          {rows.map((row, index) => <div key={`${row.left || ""}/${row.right || ""}`} className="grid grid-cols-2 border-b border-slate-100 font-mono text-xs last:border-0 sm:text-sm">
            {[row.left, row.right].map((ip, side) => <div key={side} className={cn("flex min-h-10 items-start gap-1 px-2 py-2.5 sm:gap-3 sm:px-3", !side && "border-r border-slate-200", row.changed && ip ? side ? "bg-emerald-50 text-emerald-900" : "bg-rose-50 text-rose-900" : "text-slate-600", !ip && "bg-slate-50/80")}>
              <span aria-hidden="true" className="w-5 shrink-0 select-none text-right text-[11px] text-slate-400">{ip ? row.changed ? index + 1 : index - Math.max(removed.length, added.length) + (side ? added.length : removed.length) + 1 : ""}</span>
              <span className="w-3 shrink-0 select-none" aria-label={ip ? row.changed ? side ? "仅计划" : "仅云端" : "两侧共有" : undefined}>{ip ? row.changed ? side ? "+" : "−" : " " : ""}</span>
              <span className="break-all leading-5">{ip || " "}</span>
            </div>)}
          </div>)}
          {!rows.length && <div className="px-6 py-12 text-center text-sm text-slate-500"><Check className="mx-auto mb-3 size-7 text-emerald-600" />{current.length || desired.length ? "两侧 IP 完全一致，没有差异。" : "云端与计划均无 IP。"}</div>}
        </div>
      </div>
      <div className="border-t bg-slate-50/60 px-5 py-3 text-xs leading-5 text-muted-foreground sm:px-6">
        {r.action === "hold" ? `本轮保留云端解析。${r.reason || ""}` : r.applied ? "本轮变更已提交；左侧保留执行前快照，实际解析生效仍需等待 DNS 缓存更新。" : snapshot.preview ? "本轮仅预览，差异尚未提交到云端。" : "此处显示任务计划，是否已执行请以记录的处理结果为准。"}
      </div>
    </DialogContent>
  </Dialog>;
}
