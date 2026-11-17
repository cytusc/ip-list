import "server-only";
import { api } from "@/lib/api";
import { pageHeaders, type PageResult } from "@/lib/pagination";

export async function pagedQuery<T, Extra extends object = object>(path: string, signal?: AbortSignal): Promise<PageResult<T> & Extra> {
  const { data, response } = await api<T[] | { items?: T[]; results?: T[] }>(path,{signal});
  const meta = response.headers.get("X-List-Meta");
  return {
    ...(Array.isArray(data) ? {} : data),
    items: Array.isArray(data) ? data : data.items ?? data.results ?? [],
    ...pageHeaders(response),
    ...(meta ? JSON.parse(Buffer.from(meta, "base64").toString("utf8")) : {}),
  };
}
