import { api, ApiError } from "@/lib/api";
import type { AvailableIPSpeedResult } from "@/lib/types";

export async function POST(request: Request) {
  try {
    const { data } = await api<AvailableIPSpeedResult>("/available-ips/speed-test", {
      method: "POST",
      body: JSON.stringify(await request.json()),
      signal: request.signal,
    });
    return Response.json(data, { headers: { "Cache-Control": "no-store" } });
  } catch (error) {
    return Response.json(
      { error: error instanceof ApiError ? error.message : "测速失败，请重试" },
      { status: error instanceof ApiError ? error.status : 503 },
    );
  }
}
