import { api, ApiError } from "@/lib/api";
import type { LatencyProbeResult } from "@/lib/types";

export async function GET(
  request: Request,
  { params }: { params: Promise<{ id: string }> },
) {
  const { id } = await params;
  const query = new URL(request.url).searchParams;
  const ip = query.get("ip") || "";
  const hours = query.get("hours") || "24";
  try {
    const { data } = await api<LatencyProbeResult[]>(
      `/latency-probes/${encodeURIComponent(id)}/history?ip=${encodeURIComponent(ip)}&hours=${encodeURIComponent(hours)}`,
    );
    return Response.json(data, { headers: { "Cache-Control": "no-store" } });
  } catch (error) {
    return Response.json(
      { error: error instanceof ApiError ? error.message : "读取延迟历史失败" },
      { status: error instanceof ApiError ? error.status : 503 },
    );
  }
}
