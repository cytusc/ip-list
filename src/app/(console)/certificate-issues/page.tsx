import Link from "next/link";
import { getCertificateIssues } from "@/lib/queries";

export default async function CertificateIssuesPage() {
  const items = await getCertificateIssues();
  return <div className="space-y-6">
    <div className="flex flex-wrap items-center justify-between gap-4">
      <div><h1 className="text-2xl font-semibold tracking-tight">证书异常 IP</h1>
        <p className="mt-2 max-w-3xl text-sm leading-6 text-muted-foreground">这些 IP 的入口证书未通过节点 SNI 校验，已从对应 SNI 的可用池中隔离。扫描会进行 3 次独立 TLS 握手，并检测实际代理请求；重新检测通过后自动恢复。</p>
      </div>
      <Link href="/probes" className="rounded-lg border bg-white px-4 py-2 text-sm">前往连通性测试</Link>
    </div>
    <p className="text-sm text-muted-foreground">共 {items.length} 条异常记录。证书有效性与 SNI 有关；隔离不会加入手动黑名单。偶发网络超时不会被标记为证书异常。</p>
    <div className="overflow-x-auto rounded-xl border bg-white">
      <table className="w-full text-left text-sm">
        <thead className="border-b bg-slate-50"><tr>{["IP / 协议", "节点 / SNI", "实际证书", "失败原因", "检测时间"].map(label=><th key={label} className="p-4 font-medium">{label}</th>)}</tr></thead>
        <tbody>{items.map(item=><tr key={`${item.ip}/${item.protocol}`} className="border-b last:border-0">
          <td className="p-4"><span className="font-mono">{item.ip}</span><div className="mt-1 text-xs text-muted-foreground">{item.protocol}</div></td>
          <td className="p-4">{item.node_name}<div className="mt-1 text-xs text-muted-foreground">{item.certificate.server_name}</div></td>
          <td className="max-w-72 break-words p-4">{item.certificate.subject || "未收到证书"}<div className="mt-1 text-xs text-muted-foreground">{item.certificate.issuer}</div></td>
          <td className="max-w-96 break-words p-4 text-red-700">{item.certificate.error}<div className="mt-1 text-xs text-muted-foreground">第 {item.certificate.attempts} 次握手发现异常</div></td>
          <td className="whitespace-nowrap p-4">{new Date(item.checked_at).toLocaleString("zh-CN", {timeZone:"Asia/Shanghai",hour12:false})}</td>
        </tr>)}{!items.length&&<tr><td colSpan={5} className="p-12 text-center text-muted-foreground">暂无证书异常记录。完成连通性扫描后，证书异常 IP 会显示在这里。</td></tr>}</tbody>
      </table>
    </div>
  </div>;
}
