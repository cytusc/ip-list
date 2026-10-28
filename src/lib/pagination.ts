export type PageResult<T> = {
  items: T[];
  total: number;
  page: number;
  pageSize: number;
  allTotal?: number;
  enabledCount?: number;
  countries?: string[];
  providers?: string[];
};

export function pageHeaders(response: Response) {
  return {
    total: Number(response.headers.get("X-Total-Count")),
    page: Number(response.headers.get("X-Page")) || 1,
    pageSize: Number(response.headers.get("X-Page-Size")) || 50,
    ...(response.headers.has("X-All-Total") ? { allTotal: Number(response.headers.get("X-All-Total")) } : {}),
    ...(response.headers.has("X-Enabled-Count") ? { enabledCount: Number(response.headers.get("X-Enabled-Count")) } : {}),
  };
}
