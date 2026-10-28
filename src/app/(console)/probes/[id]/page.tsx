import { ApiError } from "@/lib/api";
import { notFound } from "next/navigation";
import { pagedQuery } from "@/lib/paged-query";
import { ProbeDetail } from "@/components/probe-detail";
import type { ScanRecord, ScanResult } from "@/lib/types";
export const dynamic = "force-dynamic";
export default async function Page({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;
  if (!/^\d+$/.test(id)) notFound();
  let record;
  try {
    record = await pagedQuery<ScanResult,ScanRecord>(`/scan-records/${id}?paged=1`);
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) notFound();
    throw error;
  }
  return <ProbeDetail initial={record} />;
}
