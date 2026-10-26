import "server-only";
import { api } from "@/lib/api";
import { requireUser } from "@/lib/auth";
import type {
  Node,
  Network,
  AvailableIP,
  ScanRecordList,
  BlacklistedIP,
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
export async function getScanRecords(): Promise<ScanRecordList> {
  await requireUser();
  return (await api<ScanRecordList>("/scan-records")).data;
}
