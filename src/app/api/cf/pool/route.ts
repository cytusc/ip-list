import { api, ApiError } from "@/lib/api";
export async function GET() {
  try {
    return Response.json((await api("/cf-pool")).data, {
      headers: { "Cache-Control": "no-store" },
    });
  } catch (error) {
    return Response.json(
      { error: error instanceof ApiError ? error.message : "获取共用 IP 池失败" },
      { status: error instanceof ApiError ? error.status : 503 },
    );
  }
}
