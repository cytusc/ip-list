"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { ApiError } from "@/lib/api";
import { pagedQuery } from "@/lib/paged-query";
import { CFDetailView } from "@/components/cf-manager";
import { Button } from "@/components/ui/button";
import type { CFDetail, CFIP } from "@/lib/types";

type Detail = CFDetail & { items: CFIP[]; total: number; page: number; pageSize: number };

export default function Page() {
  const [id, setId] = useState("");
  const [detail, setDetail] = useState<Detail | null>(null);
  const [missing, setMissing] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    const target = new URLSearchParams(window.location.search).get("id") || "";
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setId(target);
    if (!target) {
      setMissing(true);
      return;
    }
    pagedQuery<CFIP, CFDetail>(`/cf-targets/${encodeURIComponent(target)}`)
      .then((value) => setDetail(value as Detail))
      .catch((caught) => {
        if (caught instanceof ApiError && caught.status === 404) setMissing(true);
        else setError(caught instanceof Error ? caught.message : "加载失败");
      });
  }, []);

  if (missing)
    return (
      <div className="rounded-xl border border-dashed bg-white p-8 text-center">
        <p className="font-medium">CF 目标不存在或已删除</p>
        <Button asChild variant="link" className="mt-2">
          <Link href="/cf">返回 CF 优选</Link>
        </Button>
      </div>
    );
  if (error)
    return <p className="py-16 text-center text-sm text-red-600">{error}</p>;
  if (!detail || !detail.target)
    return (
      <p className="py-16 text-center text-sm text-muted-foreground">
        正在加载{id ? ` ${id} ` : ""}的检测结果…
      </p>
    );
  return <CFDetailView initial={detail} />;
}
