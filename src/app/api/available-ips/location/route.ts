import { api, ApiError } from "@/lib/api";
import type { IPLocation } from "@/lib/types";

export async function GET(request: Request) {
  const ip = new URL(request.url).searchParams.get("ip") || "";
  try {
    const { data } = await api<IPLocation>(
      `/available-ips/location?ip=${encodeURIComponent(ip)}`,
    );
    return Response.json(data, { headers: { "Cache-Control": "no-store" } });
  } catch (error) {
    return Response.json(
      { error: "查询 IP 归属地失败" },
      { status: error instanceof ApiError ? error.status : 503 },
    );
  }
}

async function change(request: Request) {
  const ip = new URL(request.url).searchParams.get("ip") || "";
  try {
    const { data } = await api<IPLocation>(`/available-ips/location?ip=${encodeURIComponent(ip)}`, {
      method: request.method,
      ...(request.method === "PUT" ? { body: JSON.stringify(await request.json()) } : {}),
    });
    return Response.json(data, { headers: { "Cache-Control": "no-store" } });
  } catch (error) {
    return Response.json({ error: error instanceof ApiError ? error.message : "保存归属地失败" },
      { status: error instanceof ApiError ? error.status : 503 });
  }
}

export const PUT = change;
export const DELETE = change;
