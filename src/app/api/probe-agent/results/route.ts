export async function POST(request: Request) {
  const authorization = request.headers.get("authorization");
  if (!authorization) return Response.json({ error: "缺少探针密钥" }, { status: 401 });
  const contentLength = Number(request.headers.get("content-length") || 0);
  if (contentLength > 2 * 1024 * 1024)
    return Response.json({ error: "上报数据过大" }, { status: 413 });
  try {
    const body = await request.text();
    if (body.length > 2 * 1024 * 1024)
      return Response.json({ error: "上报数据过大" }, { status: 413 });
    const response = await fetch(
      `${process.env.API_URL || "http://127.0.0.1:8081"}/probe-agent/results`,
      {
        method: "POST",
        headers: {
          Authorization: authorization,
          "Content-Type": "application/json",
          Accept: "application/json",
        },
        body,
        cache: "no-store",
        signal: AbortSignal.timeout(120000),
      },
    );
    return new Response(response.body, {
      status: response.status,
      headers: {
        "Content-Type": response.headers.get("content-type") || "application/json",
        "Cache-Control": "no-store",
      },
    });
  } catch {
    return Response.json({ error: "探针服务暂时不可用" }, { status: 503 });
  }
}
