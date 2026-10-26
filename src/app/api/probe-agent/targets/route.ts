export async function GET(request: Request) {
  return proxy(request, "GET");
}

async function proxy(request: Request, method: string) {
  const authorization = request.headers.get("authorization");
  if (!authorization) return Response.json({ error: "缺少探针密钥" }, { status: 401 });
  try {
    const response = await fetch(
      `${process.env.API_URL || "http://127.0.0.1:8081"}/probe-agent/targets`,
      {
        method,
        headers: { Authorization: authorization, Accept: "application/json", "X-Probe-Features": request.headers.get("x-probe-features") || "" },
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
