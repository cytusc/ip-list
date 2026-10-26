import { api, ApiError } from "@/lib/api";
import type { LatencyProbe } from "@/lib/types";
export async function GET() {
  try {
    const { data } = await api<LatencyProbe[]>("/latency-probes");
    return Response.json(data, { headers: { "Cache-Control": "no-store" } });
  } catch (error) {
    return Response.json({ error: error instanceof ApiError ? error.message : "获取探针状态失败" },
      { status: error instanceof ApiError ? error.status : 503 });
  }
}
