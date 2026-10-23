import { api, ApiError } from "@/lib/api";
import type { AvailableIP } from "@/lib/types";

export async function GET() {
  try {
    const { data } = await api<AvailableIP[]>("/available-ips");
    return Response.json(data, { headers: { "Cache-Control": "no-store" } });
  } catch (error) {
    return Response.json(
      {
        error:
          error instanceof ApiError
            ? error.message
            : "获取可用 IP 失败，请重试",
      },
      { status: error instanceof ApiError ? error.status : 503 },
    );
  }
}
