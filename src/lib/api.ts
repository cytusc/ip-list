"use client";

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    public fields?: Record<string, string>,
  ) {
    super(message);
  }
}

// Same-origin only: the Rust gateway serves the static UI and exposes the
// control API below /api, so the browser carries the session cookie directly.
const API_BASE = "/api";

export async function api<T>(
  path: string,
  options: RequestInit & { timeoutMs?: number } = {},
): Promise<{ data: T; response: Response }> {
  const { timeoutMs = 12000, ...init } = options;
  let response: Response;
  try {
    response = await fetch(`${API_BASE}${path}`, {
      ...init,
      credentials: "include",
      headers: {
        "Content-Type": "application/json",
        Origin: window.location.origin,
        ...init.headers,
      },
      cache: "no-store",
      signal: init.signal
        ? AbortSignal.any([init.signal, AbortSignal.timeout(timeoutMs)])
        : AbortSignal.timeout(timeoutMs),
    });
  } catch {
    throw new ApiError(503, "无法连接服务端，请稍后重试");
  }
  const data = await response.json().catch(() => ({}));
  if (!response.ok)
    throw new ApiError(
      response.status,
      data.error || "请求失败，请稍后重试",
      data.fields,
    );
  return { data: data as T, response };
}
