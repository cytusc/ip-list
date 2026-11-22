import { api } from "@/lib/api";
import { pageHeaders, type PageResult } from "@/lib/pagination";

export async function pagedQuery<T, Extra extends object = object>(
  path: string,
  signal?: AbortSignal,
  timeoutMs?: number,
): Promise<PageResult<T> & Extra> {
  const { data, response } = await api<
    T[] | { items?: T[]; results?: T[] }
  >(path, { signal, timeoutMs });
  const meta = response.headers.get("X-List-Meta");
  return {
    ...(Array.isArray(data) ? {} : data),
    items: Array.isArray(data) ? data : data.items ?? data.results ?? [],
    ...pageHeaders(response),
    ...(meta ? JSON.parse(decodeBase64(meta)) : {}),
  };
}

function decodeBase64(value: string): string {
  const bytes = Uint8Array.from(atob(value), (char) => char.charCodeAt(0));
  return new TextDecoder().decode(bytes);
}

export function emptyPage<T>(): PageResult<T> {
  return { items: [], total: 0, page: 1, pageSize: 50 };
}
