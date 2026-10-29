import { api, ApiError } from "@/lib/api";
import type { IPLocation } from "@/lib/types";

export async function GET(request: Request) {
  const ips = [...new Set(new URL(request.url).searchParams.getAll("ip"))];
  if (ips.length > 20) {
    return Response.json({ error: "每次最多查询 20 个 IP" }, { status: 422 });
  }

  const results: { ip: string; location: IPLocation | null }[] = [];
  let next = 0;
  async function worker() {
    while (next < ips.length) {
      const ip = ips[next++];
      try {
        const { data } = await api<IPLocation>(
          `/available-ips/location?ip=${encodeURIComponent(ip)}`,
        );
        results.push({ ip, location: data });
      } catch (error) {
        if (error instanceof ApiError && error.status === 401) throw error;
        results.push({ ip, location: null });
      }
    }
  }

  try {
    await Promise.all(Array.from({ length: Math.min(4, ips.length) }, worker));
    return Response.json(results, { headers: { "Cache-Control": "no-store" } });
  } catch (error) {
    return Response.json(
      { error: "查询 IP 归属地失败" },
      { status: error instanceof ApiError ? error.status : 503 },
    );
  }
}
