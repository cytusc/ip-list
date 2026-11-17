import "server-only";
import { cookies } from "next/headers";

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    public fields?: Record<string, string>,
  ) {
    super(message);
  }
}
export async function api<T>(
  path: string,
  options: RequestInit = {},
): Promise<{ data: T; response: Response }> {
  let response: Response;
  try {
    response = await fetch(
      `${process.env.API_URL || "http://127.0.0.1:8081"}${path}`,
      {
        ...options,
        headers: {
          "Content-Type": "application/json",
          Cookie: (await cookies()).toString(),
          Origin: process.env.APP_ORIGIN || "http://localhost:3000",
          ...options.headers,
        },
        cache: "no-store",
        signal: options.signal ? AbortSignal.any([options.signal,AbortSignal.timeout(12000)]) : AbortSignal.timeout(12000),
      },
    );
  } catch {
    throw new ApiError(503, "无法连接后端服务，请检查 Go API 是否已启动后重试");
  }
  const data = await response.json();
  if (!response.ok)
    throw new ApiError(
      response.status,
      data.error || "请求失败，请稍后重试",
      data.fields,
    );
  return { data: data as T, response };
}
// Cookie 由 Go 的 SCS 会话库创建，此处只负责传递到前端域名。
export async function forwardSessionCookie(response: Response) {
  const header = response.headers
    .getSetCookie()
    .find((value) => value.startsWith("ipfilter_session="));
  if (!header) return;
  const value = header.split(";", 1)[0].slice("ipfilter_session=".length);
  const maxAge = Number(header.match(/Max-Age=(-?\d+)/i)?.[1] || "86400");
  (await cookies()).set("ipfilter_session", value, {
    httpOnly: true,
    secure: process.env.NODE_ENV === "production",
    sameSite: "lax",
    path: "/",
    maxAge: Math.max(0, maxAge),
  });
}
