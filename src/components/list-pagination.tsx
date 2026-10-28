"use client";
import { Button } from "@/components/ui/button";
import type { PageResult } from "@/lib/pagination";

export function ListPagination({data,onPage,loading=false}:{data:Pick<PageResult<unknown>,"total"|"page"|"pageSize">;onPage:(page:number)=>void;loading?:boolean}) {
  const pages=Math.max(1,Math.ceil(data.total/data.pageSize));
  return <div className="flex flex-wrap items-center justify-between gap-3 border-t p-4 text-xs text-muted-foreground" aria-label="列表分页">
    <span role="status">共 {data.total} 条 · 每页 {data.pageSize} 条 · 第 {data.page} / {pages} 页{loading ? " · 加载中…" : ""}</span>
    <div className="flex gap-2">
      <Button type="button" size="sm" variant="outline" disabled={loading || data.page<=1} onClick={()=>onPage(data.page-1)}>上一页</Button>
      <Button type="button" size="sm" variant="outline" disabled={loading || data.page>=pages} onClick={()=>onPage(data.page+1)}>下一页</Button>
    </div>
  </div>;
}
