"use client";

import Link from "next/link";
import { CertificateIssuesTable } from "@/components/certificate-issues-table";
import { usePagedList } from "@/components/use-paged-list";
import { emptyPage } from "@/lib/paged-query";
import type { CertificateIssue } from "@/lib/types";

const initial = emptyPage<CertificateIssue>();

export default function CertificateIssuesPage() {
  const { data, setPage, loading, error } = usePagedList(
    "certificate-issues",
    initial,
  );
  const pages = Math.max(1, Math.ceil(data.total / data.pageSize));
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">证书异常 IP</h1>
          <p className="mt-2 max-w-3xl text-sm leading-6 text-muted-foreground">
            这些 IP 的入口证书未通过节点 SNI 校验，已从对应 SNI 的可用池中隔离。扫描会进行 3 次独立 TLS 握手，并检测实际代理请求；重新检测通过后自动恢复。
          </p>
        </div>
        <Link href="/probes" className="rounded-lg border bg-white px-4 py-2 text-sm">
          前往连通性测试
        </Link>
      </div>
      {error && <p className="text-sm text-red-600">{error}</p>}
      <p className="text-sm text-muted-foreground">
        {loading && !data.items.length
          ? "正在加载证书异常记录…"
          : `共 ${data.total} 条异常记录，按最近检测时间排序，每页 ${data.pageSize} 条。证书有效性与 SNI 有关；隔离不会加入手动黑名单。偶发网络超时不会被标记为证书异常。`}
      </p>
      <CertificateIssuesTable items={data.items} />
      <nav
        aria-label="证书异常列表分页"
        className="flex flex-wrap items-center justify-between gap-3"
      >
        <p className="text-sm text-muted-foreground">
          第 {data.page} / {pages} 页 ·{" "}
          {data.total ? (data.page - 1) * data.pageSize + 1 : 0}–
          {Math.min(data.page * data.pageSize, data.total)} 条
        </p>
        <div className="flex items-center gap-2">
          <button
            type="button"
            disabled={data.page <= 1}
            onClick={() => setPage(data.page - 1)}
            className="rounded-lg border bg-white px-4 py-2 text-sm disabled:opacity-50"
          >
            上一页
          </button>
          <button
            type="button"
            disabled={data.page >= pages}
            onClick={() => setPage(data.page + 1)}
            className="rounded-lg border bg-white px-4 py-2 text-sm disabled:opacity-50"
          >
            下一页
          </button>
        </div>
      </nav>
    </div>
  );
}
