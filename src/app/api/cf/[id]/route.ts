import { api, ApiError } from "@/lib/api";
export async function GET(
  _request: Request,
  { params }: { params: Promise<{ id: string }> },
) {
  const { id } = await params;
  try {
    return Response.json(
      (await api(`/cf-targets/${encodeURIComponent(id)}`)).data,
    );
  } catch (error) {
    return Response.json(
      { error: error instanceof ApiError ? error.message : "刷新失败，请重试" },
      { status: error instanceof ApiError ? error.status : 503 },
    );
  }
}
