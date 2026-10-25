import { api, ApiError } from "@/lib/api";
export async function GET(request: Request) {
  const params = new URL(request.url).searchParams;
  const id = params.get("id");
  const protocol = params.get("protocol");
  if (id && !/^[a-f0-9]{32}$/.test(id))
    return Response.json({ error: "任务 ID 无效" }, { status: 400 });
  try {
    const { data } = await api(
      `/scans/${id || "latest"}${!id && protocol ? `?protocol=${encodeURIComponent(protocol)}` : ""}`,
    );
    return Response.json(data, { headers: { "Cache-Control": "no-store" } });
  } catch (error) {
    return Response.json(
      { error: error instanceof ApiError ? error.message : "获取进度失败" },
      { status: error instanceof ApiError ? error.status : 503 },
    );
  }
}
