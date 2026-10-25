import { api, ApiError } from "@/lib/api";
export async function GET(request: Request) {
  const params = new URL(request.url).searchParams;
  const query = new URLSearchParams();
  const limit = params.get("limit");
  const protocol = params.get("protocol");
  if (limit && /^\d{1,3}$/.test(limit)) query.set("limit", limit);
  if (protocol) query.set("protocol", protocol);
  try {
    const { data } = await api(
      `/scan-records${query.size ? `?${query.toString()}` : ""}`,
    );
    return Response.json(data, { headers: { "Cache-Control": "no-store" } });
  } catch (error) {
    return Response.json(
      { error: error instanceof ApiError ? error.message : "获取检测记录失败" },
      { status: error instanceof ApiError ? error.status : 503 },
    );
  }
}
