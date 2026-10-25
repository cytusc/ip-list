import { api, ApiError } from "@/lib/api";
export async function GET(
  _request: Request,
  { params }: { params: Promise<{ id: string }> },
) {
  const { id } = await params;
  if (!/^\d+$/.test(id))
    return Response.json({ error: "检测记录无效" }, { status: 400 });
  try {
    const { data } = await api(`/scan-records/${id}`);
    return Response.json(data, { headers: { "Cache-Control": "no-store" } });
  } catch (error) {
    return Response.json(
      { error: error instanceof ApiError ? error.message : "获取检测记录失败" },
      { status: error instanceof ApiError ? error.status : 503 },
    );
  }
}
