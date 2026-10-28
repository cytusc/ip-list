import Link from "next/link";
import { CertificateIssuesTable } from "@/components/certificate-issues-table";
import { getCertificateIssues } from "@/lib/queries";

export default async function CertificateIssuesPage({ searchParams }: {
  searchParams: Promise<{ page?: string | string[] }>;
}) {
  const rawPage = (await searchParams).page;
  const requestedPage = typeof rawPage === "string" && /^\d+$/.test(rawPage) ? Number(rawPage) : 1;
  const { items, total, page, pageSize } = await getCertificateIssues(
    Number.isSafeInteger(requestedPage) && requestedPage > 0 ? requestedPage : 1,
  );
  const pages = Math.max(1, Math.ceil(total / pageSize));
  return <div className="space-y-6">
    <div className="flex flex-wrap items-center justify-between gap-4">
      <div><h1 className="text-2xl font-semibold tracking-tight">证书异常 IP</h1>
        <p className="mt-2 max-w-3xl text-sm leading-6 text-muted-foreground">这些 IP 的入口证书未通过节点 SNI 校验，已从对应 SNI 的可用池中隔离。扫描会进行 3 次独立 TLS 握手，并检测实际代理请求；重新检测通过后自动恢复。</p>
      </div>
      <Link href="/probes" className="rounded-lg border bg-white px-4 py-2 text-sm">前往连通性测试</Link>
    </div>
    <p className="text-sm text-muted-foreground">共 {total} 条异常记录，按最近检测时间排序，每页 {pageSize} 条。证书有效性与 SNI 有关；隔离不会加入手动黑名单。偶发网络超时不会被标记为证书异常。</p>
    <CertificateIssuesTable items={items} />
    <nav aria-label="证书异常列表分页" className="flex flex-wrap items-center justify-between gap-3">
      <p className="text-sm text-muted-foreground">第 {page} / {pages} 页 · {total ? (page - 1) * pageSize + 1 : 0}–{Math.min(page * pageSize, total)} 条</p>
      <div className="flex items-center gap-2">
        {page > 1 ? <Link prefetch={false} href={`/certificate-issues?page=${page - 1}`} className="rounded-lg border bg-white px-4 py-2 text-sm">上一页</Link>
          : <span aria-disabled="true" className="rounded-lg border px-4 py-2 text-sm text-muted-foreground opacity-50">上一页</span>}
        {page < pages ? <Link prefetch={false} href={`/certificate-issues?page=${page + 1}`} className="rounded-lg border bg-white px-4 py-2 text-sm">下一页</Link>
          : <span aria-disabled="true" className="rounded-lg border px-4 py-2 text-sm text-muted-foreground opacity-50">下一页</span>}
      </div>
    </nav>
  </div>;
}
