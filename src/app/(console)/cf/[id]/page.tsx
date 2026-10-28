import { ApiError } from "@/lib/api";
import { pagedQuery } from "@/lib/paged-query";
import { CFDetailView } from "@/components/cf-manager";
import { notFound } from "next/navigation";
import type { CFDetail, CFIP } from "@/lib/types";
export default async function Page({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;
  let detail;
  try {
    detail = await pagedQuery<CFIP, CFDetail>(`/cf-targets/${encodeURIComponent(id)}`);
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) notFound();
    throw error;
  }
  return <CFDetailView initial={detail} />;
}
