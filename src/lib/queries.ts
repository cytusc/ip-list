import "server-only";
import { api } from "@/lib/api";
import { requireUser } from "@/lib/auth";
import type { CertificateIssue, LatencyProbeResult } from "@/lib/types";
export async function getLatencyProbeHistory(
  id: string,
  ip: string,
  hours = 24,
): Promise<LatencyProbeResult[]> {
  await requireUser();
  return (
    await api<LatencyProbeResult[]>(
      `/latency-probes/${encodeURIComponent(id)}/history?ip=${encodeURIComponent(ip)}&hours=${hours}`,
    )
  ).data;
}
export async function getCertificateIssues(page = 1): Promise<{
  items: CertificateIssue[]; total: number; page: number; pageSize: number;
}> {
  await requireUser();
  const { data, response } = await api<CertificateIssue[]>(`/certificate-issues?page=${page}`);
  return {
    items: data,
    total: Number(response.headers.get("X-Total-Count")),
    page: Number(response.headers.get("X-Page")),
    pageSize: Number(response.headers.get("X-Page-Size")),
  };
}
