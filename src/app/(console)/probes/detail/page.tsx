"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { ApiError } from "@/lib/api";
import { pagedQuery } from "@/lib/paged-query";
import { ProbeDetail } from "@/components/probe-detail";
import { Button } from "@/components/ui/button";
import type { ScanRecord, ScanResult } from "@/lib/types";

type Detail = ScanRecord & { items: ScanResult[]; total: number; page: number; pageSize: number };

export default function Page() {
  const [detail, setDetail] = useState<Detail | null>(null);
  const [missing, setMissing] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    const id = new URLSearchParams(window.location.search).get("id") || "";
    if (!/^\d+$/.test(id)) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setMissing(true);
      return;
    }
    pagedQuery<ScanResult, ScanRecord>(`/scan-records/${id}?paged=1`)
      .then((value) => setDetail(value as Detail))
      .catch((caught) => {
        if (caught instanceof ApiError && caught.status === 404) setMissing(true);
        else setError(caught instanceof Error ? caught.message : "加载失败");
      });
  }, []);

  if (missing)
    return (
      <div className="rounded-xl border border-dashed bg-white p-8 text-center">
        <p className="font-medium">检测记录不存在或已删除</p>
        <Button asChild variant="link" className="mt-2">
          <Link href="/probes">返回连通性测试</Link>
        </Button>
      </div>
    );
  if (error)
    return <p className="py-16 text-center text-sm text-red-600">{error}</p>;
  if (!detail)
    return <p className="py-16 text-center text-sm text-muted-foreground">正在加载检测结果…</p>;
  return <ProbeDetail initial={detail} />;
}
