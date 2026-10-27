import Link from "next/link";
import { CertificateIssuesTable } from "@/components/certificate-issues-table";
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
    <CertificateIssuesTable items={items} />
  </div>;
}
