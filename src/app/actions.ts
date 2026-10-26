"use server";
import { revalidatePath } from "next/cache";
import { redirect } from "next/navigation";
import { cookies } from "next/headers";
import { api, ApiError, forwardSessionCookie } from "@/lib/api";
import type { ActionResult, LatencyProbe } from "@/lib/types";
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
export async function submitLogin(
  _previous: ActionResult | null,
  formData: FormData,
): Promise<ActionResult> {
  return login({
    username: formData.get("username"),
    password: formData.get("password"),
  });
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
    const { data } = await api<{ warning?: string }>(
      `/${kind}${id ? `/${encodeURIComponent(id)}` : ""}`,
      {
        method: id ? "PUT" : "POST",
        body: JSON.stringify(input),
      },
    );
    revalidatePath("/nodes");
    revalidatePath("/networks");
    revalidatePath("/available-ips");
    revalidatePath("/cf", "layout");
    return { ok: true, warning: data.warning };
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
export async function saveNetworks(
  inputCidrs: string[],
  enabled: boolean,
  description: string,
): Promise<
  | { ok: true; created: number; skipped: number; failures: string[] }
  | { ok: false; error: string }
> {
  if (
    !Array.isArray(inputCidrs) ||
    inputCidrs.some((cidr) => typeof cidr !== "string")
  )
    return { ok: false, error: "网段列表格式无效" };
  if (inputCidrs.length > 256)
    return { ok: false, error: "一次最多批量添加 256 个网段" };

  const unique: string[] = [];
  const seen = new Set<string>();
  let skipped = 0;
  for (const value of inputCidrs) {
    const cidr = value.trim();
    if (!cidr) continue;
    if (seen.has(cidr)) {
      skipped++;
      continue;
    }
    seen.add(cidr);
    unique.push(cidr);
  }
  if (!unique.length) return { ok: false, error: "请至少输入一个网段" };

  let created = 0;
  const failures: string[] = [];
  for (const cidr of unique) {
    try {
      await api("/networks", {
        method: "POST",
        body: JSON.stringify({
          name: cidr,
          cidr,
          enabled,
          description,
          asn: "",
          provider: "",
        }),
      });
      created++;
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) {
        skipped++;
        continue;
      }
      const detail =
        error instanceof ApiError
          ? error.fields?.cidr || error.message
          : "请求失败，请稍后重试";
      failures.push(`${cidr}：${detail}`);
      if (
        error instanceof ApiError &&
        (error.status === 401 || error.status >= 500)
      )
        break;
    }
  }

  if (created) {
    revalidatePath("/nodes");
    revalidatePath("/networks");
    revalidatePath("/available-ips");
    revalidatePath("/cf", "layout");
  }
  if (!created && !skipped && failures.length)
    return { ok: false, error: failures.slice(0, 5).join("；") };
  return { ok: true, created, skipped, failures };
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
    revalidatePath("/available-ips");
    revalidatePath("/cf", "layout");
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
export async function addBlacklist(ips: string[]): Promise<
  | { ok: true; added: number; skipped: number }
  | { ok: false; error: string }
> {
  try {
    const { data } = await api<{ added: number; skipped: number }>("/blacklist", {
      method: "POST",
      body: JSON.stringify({ ips }),
    });
    revalidatePath("/blacklist");
    revalidatePath("/available-ips");
    revalidatePath("/cf", "layout");
    return { ok: true, ...data };
  } catch (error) {
    return { ok: false, error: error instanceof ApiError ? error.fields?.ip || error.message : "添加失败，请重试" };
  }
}
export async function removeBlacklist(ip: string): Promise<ActionResult> {
  try {
    await api("/blacklist", { method: "DELETE", body: JSON.stringify({ ip }) });
    revalidatePath("/blacklist");
    revalidatePath("/available-ips");
    revalidatePath("/cf", "layout");
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
    revalidatePath("/probes");
    return { ok: true, job: data };
  } catch (error) {
    return {
      ok: false,
      error: error instanceof ApiError ? error.message : "任务启动失败，请重试",
    };
  }
}
export async function createLatencyProbe(input: {
  name: string;
  interval_seconds: number;
  timeout_ms: number;
  concurrency: number;
  interface_name: string;
  mode: "icmp" | "tcp";
  tcp_port: number;
}): Promise<
  | { ok: true; probe: LatencyProbe; token: string }
  | { ok: false; error: string }
> {
  try {
    const { data } = await api<{ probe: LatencyProbe; token: string }>(
      "/latency-probes",
      { method: "POST", body: JSON.stringify(input) },
    );
    revalidatePath("/latency-probes");
    revalidatePath("/available-ips");
    return { ok: true, ...data };
  } catch (error) {
    return failure(error);
  }
}
export async function updateLatencyProbe(
  id: string,
  input: {
    name: string;
    interval_seconds: number;
    timeout_ms: number;
    concurrency: number;
    interface_name: string;
  mode: "icmp" | "tcp";
  tcp_port: number;
    enabled: boolean;
  },
): Promise<ActionResult> {
  try {
    await api(`/latency-probes/${encodeURIComponent(id)}`, {
      method: "PUT",
      body: JSON.stringify(input),
    });
    revalidatePath("/latency-probes");
    return { ok: true };
  } catch (error) {
    return failure(error);
  }
}
export async function deleteLatencyProbe(id: string): Promise<ActionResult> {
  try {
    await api(`/latency-probes/${encodeURIComponent(id)}`, { method: "DELETE" });
    revalidatePath("/latency-probes");
    revalidatePath("/available-ips");
    return { ok: true };
  } catch (error) {
    return failure(error);
  }
}
export async function getProbeDeployment(id: string): Promise<
  { ok: true; token: string; platform: string } | { ok: false; error: string }
> {
  try {
    const { data } = await api<{ token: string; platform: string }>(`/latency-probes/${encodeURIComponent(id)}/deployment`);
    return { ok: true, ...data };
  } catch (error) { return failure(error); }
}
export async function saveProbeDeployment(id: string, token: string, platform: string): Promise<ActionResult> {
  try {
    await api(`/latency-probes/${encodeURIComponent(id)}/deployment`, {
      method: "PUT", body: JSON.stringify({ token, platform }),
    });
    return { ok: true };
  } catch (error) { return failure(error); }
}
export async function rotateLatencyProbeToken(
  id: string,
): Promise<{ ok: true; token: string } | { ok: false; error: string }> {
  try {
    const { data } = await api<{ token: string }>(
      `/latency-probes/${encodeURIComponent(id)}/token`,
      { method: "POST", body: "{}" },
    );
    return { ok: true, token: data.token };
  } catch (error) {
    return failure(error);
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
  input: { network_ids: string[]; timeout_ms: number; concurrency: number; incremental?: boolean },
) {
  try {
    const { data } = await api<import("@/lib/types").ScanJob>(
      `/cf-targets/${encodeURIComponent(id)}/scans`,
      { method: "POST", body: JSON.stringify(input) },
    );
    revalidatePath("/cf");
    revalidatePath("/cf/progress");
    return { ok: true as const, job: data };
  } catch (error) {
    return failure(error);
  }
}

export async function generateIPLinks(ip: string) {
  try {
    const { data } = await api<import("@/lib/types").GeneratedLink[]>(
      `/available-ips/links?ip=${encodeURIComponent(ip)}`,
    );
    return { ok: true as const, links: data };
  } catch (error) {
    return failure(error);
  }
}

export async function restoreProtocolNetwork(
  id: string,
  protocol: string,
): Promise<ActionResult> {
  try {
    await api(
      `/protocol-networks/${encodeURIComponent(id)}?protocol=${encodeURIComponent(protocol)}`,
      { method: "DELETE" },
    );
    revalidatePath("/probes");
    revalidatePath("/networks");
    return { ok: true };
  } catch (error) {
    return failure(error);
  }
}

export async function recheckNetwork(id: string) {
  try {
    const { data } = await api<{
      protocol: string;
      job_id?: string;
      error?: string;
    }[]>(`/networks/${encodeURIComponent(id)}/recheck`, { method: "POST" });
    revalidatePath("/probes");
    return { ok: true as const, items: data };
  } catch (error) {
    return failure(error);
  }
}
