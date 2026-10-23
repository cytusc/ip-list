import { api, ApiError } from "@/lib/api";
import { getNetworks } from "@/lib/queries";
import { CFDetailView } from "@/components/cf-manager";
import { notFound } from "next/navigation";
import type { CFDetail } from "@/lib/types";
export default async function Page({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;
  let detail;
  const networks = await getNetworks();
  try {
    detail = await api<CFDetail>(`/cf-targets/${encodeURIComponent(id)}`);
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) notFound();
    throw error;
  }
  return <CFDetailView initial={detail.data} networks={networks} />;
}
