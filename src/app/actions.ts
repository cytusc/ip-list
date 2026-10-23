"use server";
import { revalidatePath } from "next/cache";
import { redirect } from "next/navigation";
import { cookies } from "next/headers";
import { api, ApiError, forwardSessionCookie } from "@/lib/api";
import type { ActionResult } from "@/lib/types";
function failure(error: unknown): Extract<ActionResult, { ok: false }> {
  if (error instanceof ApiError)
    return { ok: false, error: error.message, fields: error.fields };
  console.error("请求失败", error);
  return { ok: false, error: "请求失败，请稍后重试" };
}
export async function login(input: unknown): Promise<ActionResult> {
  try {
    const { response } = await api("/auth/login", {
      method: "POST",
      body: JSON.stringify(input),
    });
    await forwardSessionCookie(response);
  } catch (error) {
    return failure(error);
  }
  redirect("/nodes");
}
export async function logout(): Promise<ActionResult> {
  try {
    const { response } = await api("/auth/logout", { method: "POST" });
    await forwardSessionCookie(response);
  } catch (error) {
    if (!(error instanceof ApiError && error.status === 401))
      return failure(error);
    (await cookies()).delete("ipfilter_session");
  }
  redirect("/login");
}
async function save(
  kind: "nodes" | "networks",
  id: string | null,
  input: unknown,
): Promise<ActionResult> {
  try {
    await api(`/${kind}${id ? `/${encodeURIComponent(id)}` : ""}`, {
      method: id ? "PUT" : "POST",
      body: JSON.stringify(input),
    });
    revalidatePath("/nodes");
    revalidatePath("/networks");
    return { ok: true };
  } catch (error) {
    return failure(error);
  }
}
export async function saveNode(id: string | null, input: unknown) {
  return save("nodes", id, input);
}
export async function saveNetwork(id: string | null, input: unknown) {
  return save("networks", id, input);
}
export async function deleteConfig(
  kind: "nodes" | "networks",
  id: string,
): Promise<ActionResult> {
  if (kind !== "nodes" && kind !== "networks")
    return { ok: false, error: "无效的配置类型" };
  try {
    await api(`/${kind}/${encodeURIComponent(id)}`, { method: "DELETE" });
    revalidatePath("/nodes");
    revalidatePath("/networks");
    return { ok: true };
  } catch (error) {
    return failure(error);
  }
}

export async function importNodes(links: string): Promise<ActionResult> {
  try {
    await api("/nodes/import", {
      method: "POST",
      body: JSON.stringify({ links }),
    });
    revalidatePath("/nodes");
    return { ok: true };
  } catch (error) {
    return failure(error);
  }
}
export async function startScan(
  input: unknown,
): Promise<
  | { ok: true; job: import("@/lib/types").ScanJob }
  | { ok: false; error: string }
> {
  try {
    const { data } = await api<import("@/lib/types").ScanJob>("/scans", {
      method: "POST",
      body: JSON.stringify(input),
    });
    return { ok: true, job: data };
  } catch (error) {
    return {
      ok: false,
      error: error instanceof ApiError ? error.message : "任务启动失败，请重试",
    };
  }
}
export async function cancelScan(id: string): Promise<ActionResult> {
  try {
    await api(`/scans/${encodeURIComponent(id)}/cancel`, { method: "POST" });
    return { ok: true };
  } catch (error) {
    return failure(error);
  }
}

export async function createCFTarget(input: { name: string; url: string }) {
  try {
    const { data } = await api<{ id: string }>("/cf-targets", {
      method: "POST",
      body: JSON.stringify(input),
    });
    revalidatePath("/cf");
    return { ok: true as const, id: data.id };
  } catch (error) {
    return failure(error);
  }
}
export async function deleteCFTarget(id: string): Promise<ActionResult> {
  try {
    await api(`/cf-targets/${encodeURIComponent(id)}`, { method: "DELETE" });
    revalidatePath("/cf");
    return { ok: true };
  } catch (error) {
    return failure(error);
  }
}
export async function startCFScan(
  id: string,
  input: { network_ids: string[]; timeout_ms: number; concurrency: number },
) {
  try {
    const { data } = await api<import("@/lib/types").ScanJob>(
      `/cf-targets/${encodeURIComponent(id)}/scans`,
      { method: "POST", body: JSON.stringify(input) },
    );
    return { ok: true as const, job: data };
  } catch (error) {
    return failure(error);
  }
}
