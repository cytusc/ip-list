"use client";

import type { CertificateIssue } from "@/lib/types";
import { Button } from "@/components/ui/button";
import {
  Dialog, DialogClose, DialogContent, DialogDescription,
  DialogFooter, DialogHeader, DialogTitle, DialogTrigger,
} from "@/components/ui/dialog";

function formatTime(value?: string) {
  return value ? new Date(value).toLocaleString("zh-CN", {
    timeZone: "Asia/Shanghai", hour12: false,
  }) : "—";
}

function IssueDetails({ item }: { item: CertificateIssue }) {
  const cert = item.certificate;
  const fields = [
    ["IP 地址", item.ip],
    ["节点", item.node_name],
    ["协议", item.protocol],
    ["预期 SNI", cert.server_name || "未配置"],
    ["证书主体", cert.subject || "未收到证书"],
    ["颁发机构", cert.issuer || "—"],
    ["证书域名（SAN）", cert.dns_names?.join("\n") || "—"],
    ["证书到期时间", formatTime(cert.not_after)],
    ["证书检测时间", formatTime(cert.checked_at)],
    ["结果更新时间", formatTime(item.checked_at)],
    ["检测握手次数", String(cert.attempts)],
  ];
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm" aria-label={`查看 ${item.ip} ${item.protocol} 的证书详情`}>查看详情</Button>
      </DialogTrigger>
      <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader className="min-w-0 text-left">
          <DialogTitle>证书异常详情</DialogTitle>
          <DialogDescription className="break-all">{item.ip} · {item.protocol} · 已从对应 SNI 的可用池隔离</DialogDescription>
        </DialogHeader>
        <div className="min-w-0 rounded-lg border border-red-200 bg-red-50 p-4">
          <h3 className="text-sm font-medium text-red-800">失败原因</h3>
          <p className="mt-2 whitespace-pre-wrap break-all text-sm leading-6 text-red-700">{cert.error || "入口证书未通过校验"}</p>
        </div>
        <dl className="min-w-0 divide-y rounded-lg border px-4">
          {fields.map(([label, value]) => (
            <div key={label} className="grid min-w-0 gap-1 py-3 sm:grid-cols-[8rem_minmax(0,1fr)] sm:gap-4">
              <dt className="text-sm text-muted-foreground">{label}</dt>
              <dd className="min-w-0 whitespace-pre-wrap break-all text-sm leading-6">{value}</dd>
            </div>
          ))}
        </dl>
        <p className="text-xs leading-5 text-muted-foreground">重新检测通过后自动恢复；此隔离不影响手动黑名单。</p>
        <DialogFooter><DialogClose asChild><Button variant="outline">关闭</Button></DialogClose></DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function CertificateIssuesTable({ items }: { items: CertificateIssue[] }) {
  return (
    <div className="overflow-x-auto rounded-xl border bg-white">
      <table className="w-full text-left text-sm">
        <thead className="border-b bg-slate-50">
          <tr>
            <th className="px-4 py-3 font-medium">IP / 协议</th>
            <th className="hidden px-4 py-3 font-medium sm:table-cell">节点</th>
            <th className="px-4 py-3 font-medium">状态</th>
            <th className="hidden px-4 py-3 font-medium lg:table-cell">检测时间</th>
            <th className="px-4 py-3 text-right font-medium">操作</th>
          </tr>
        </thead>
        <tbody>
          {items.map(item => (
            <tr key={`${item.ip}/${item.protocol}`} className="border-b last:border-0 hover:bg-slate-50/50">
              <td className="px-4 py-3"><span className="whitespace-nowrap font-mono font-medium">{item.ip}</span><div className="mt-1 text-xs text-muted-foreground">{item.protocol}</div></td>
              <td className="hidden max-w-56 px-4 py-3 sm:table-cell"><div className="truncate" title={item.node_name}>{item.node_name}</div></td>
              <td className="px-4 py-3"><span className="inline-flex whitespace-nowrap rounded-full bg-red-50 px-2 py-1 text-xs font-medium text-red-700">证书异常</span></td>
              <td className="hidden whitespace-nowrap px-4 py-3 text-muted-foreground lg:table-cell">{formatTime(item.checked_at)}</td>
              <td className="px-4 py-3 text-right"><IssueDetails item={item} /></td>
            </tr>
          ))}
          {!items.length && <tr><td colSpan={5} className="p-12 text-center text-muted-foreground">暂无证书异常记录。完成连通性扫描后，证书异常 IP 会显示在这里。</td></tr>}
        </tbody>
      </table>
    </div>
  );
}
