import { api, ApiError } from "@/lib/api";
import { notFound } from "next/navigation";
import { ProbeDetail } from "@/components/probe-detail";
import type { ScanRecord } from "@/lib/types";
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
    record = await api<ScanRecord>(`/scan-records/${id}`);
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) notFound();
    throw error;
  }
  return <ProbeDetail initial={record.data} />;
}
