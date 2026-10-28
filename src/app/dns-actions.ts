"use server";

import { api, ApiError } from "@/lib/api";
import { requireUser } from "@/lib/auth";
import type { DNSConfig, DNSStatus } from "@/lib/dns";

export async function getDNSStatus(): Promise<DNSStatus> {
  await requireUser();
  return (await api<DNSStatus>("/dns")).data;
}

export async function saveDNSConfig(config: DNSConfig) {
  await requireUser();
  try {
    await api("/dns", { method: "PUT", body: JSON.stringify(config) });
    return { ok: true as const };
  } catch (error) {
    return { ok: false as const, error: error instanceof ApiError ? error.message : "保存失败，请重试" };
  }
}

export async function requestDNSRun(mode: "preview" | "sync") {
  await requireUser();
  try {
    await api(`/dns/${mode}`, { method: "POST" });
    return { ok: true as const };
  } catch (error) {
    return { ok: false as const, error: error instanceof ApiError ? error.message : "提交任务失败，请重试" };
  }
}
