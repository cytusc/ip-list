import "server-only";
import { api } from "@/lib/api";
import { requireUser } from "@/lib/auth";
import type {
  CertificateIssue,
  Node,
  Network,
  AvailableIP,
  ScanRecordList,
  BlacklistedIP,
  LatencyProbe,
  LatencyProbeResult,
} from "@/lib/types";
export async function getNodes(): Promise<Node[]> {
  await requireUser();
  return (await api<Node[]>("/nodes")).data;
}
export async function getNetworks(): Promise<Network[]> {
  await requireUser();
  return (await api<Network[]>("/networks")).data;
}
export async function getAvailableIPs(): Promise<AvailableIP[]> {
  await requireUser();
  return (await api<AvailableIP[]>("/available-ips")).data;
}
export async function getBlacklist(): Promise<BlacklistedIP[]> {
  await requireUser();
  return (await api<BlacklistedIP[]>("/blacklist")).data;
}
export async function getLatencyProbes(): Promise<LatencyProbe[]> {
  await requireUser();
  return (await api<LatencyProbe[]>("/latency-probes")).data;
}
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
export async function getScanRecords(): Promise<ScanRecordList> {
  await requireUser();
  return (await api<ScanRecordList>("/scan-records")).data;
}

export async function getCertificateIssues(): Promise<CertificateIssue[]> {
  await requireUser();
  return (await api<CertificateIssue[]>("/certificate-issues")).data;
}
