"use client";

import { useState, useTransition } from "react";
import { Ban, Loader2, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { addBlacklist, removeBlacklist } from "@/app/actions";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import type { BlacklistedIP } from "@/lib/types";
import type { PageResult } from "@/lib/pagination";
import { ListPagination } from "@/components/list-pagination";
import { usePagedList } from "@/components/use-paged-list";
import { Input } from "@/components/ui/input";

export function BlacklistManager({ initialItems }: { initialItems: PageResult<BlacklistedIP> }) {
  const [search,setSearch]=useState("");
  const {data,setPage,refresh,loading,error}=usePagedList("blacklist",initialItems,{search});
  const items=data.items;
  const [value, setValue] = useState("");
  const [pending, startTransition] = useTransition();

  function submit() {
    const ips = value.split(/[\s,，;；]+/).map((ip) => ip.trim()).filter(Boolean);
    if (!ips.length) {
      toast.error("请至少输入一个 IP 地址");
      return;
    }
    startTransition(async () => {
      const result = await addBlacklist(ips);
      if (!result.ok) {
        toast.error(result.error);
        return;
      }
      toast.success(`已加入 ${result.added} 个，跳过重复 ${result.skipped} 个`);
      setValue("");
      setPage(1);refresh();
    });
  }

  function remove(ip: string) {
    startTransition(async () => {
      const result = await removeBlacklist(ip);
      if (!result.ok) {
        toast.error(result.error);
        return;
      }
      refresh();
      toast.success(`已从黑名单移除 ${ip}`);
    });
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">IP 黑名单</h1>
        <p className="mt-2 max-w-2xl text-sm leading-6 text-muted-foreground">
          黑名单只屏蔽列出的单个 IP，代理扫描和 CF 检测都会跳过这些地址；同网段的其他 IP 不受影响。
        </p>
      </div>
      <section className="space-y-3 rounded-xl border bg-white p-5">
        <label htmlFor="blacklist-ips" className="text-sm font-medium">添加 IP 地址</label>
        <Textarea
          id="blacklist-ips"
          value={value}
          onChange={(event) => setValue(event.target.value)}
          placeholder={"每行一个 IPv4 或 IPv6 地址\n45.153.245.221"}
          className="min-h-28 font-mono"
          disabled={pending}
        />
        <div className="flex flex-wrap items-center justify-between gap-3">
          <p className="text-xs text-muted-foreground">支持一次添加多个地址；已存在的地址会自动跳过。</p>
          <Button onClick={submit} disabled={pending}>
            {pending ? <Loader2 className="animate-spin" /> : <Ban />}
            添加到黑名单
          </Button>
        </div>
      </section>
      <section className="overflow-hidden rounded-xl border bg-white">
        <div className="border-b p-5">
          <h2 className="font-semibold">已屏蔽地址 <span className="ml-2 text-sm font-normal text-muted-foreground">{data.total} 个</span></h2>
          <Input aria-label="搜索黑名单 IP" placeholder="搜索 IP" className="mt-3 max-w-sm" value={search} onChange={event=>{setSearch(event.target.value);setPage(1);}}/>
          {error && <p role="alert" className="mt-2 text-sm text-destructive">{error}</p>}
        </div>
        {items.length ? (
          <ul className="divide-y">
            {items.map((item) => (
              <li key={item.ip} className="flex items-center justify-between gap-4 px-5 py-3">
                <code className="text-sm">{item.ip}</code>
                <div className="flex items-center gap-3">
                  <time className="hidden text-xs text-muted-foreground sm:block">{new Date(item.created_at).toLocaleString("zh-CN", { hour12: false })}</time>
                  <Button variant="ghost" size="sm" disabled={pending} onClick={() => remove(item.ip)}>
                    <Trash2 />移除
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        ) : (
          <div className="px-5 py-12 text-center text-sm text-muted-foreground">{search ? "没有匹配的 IP" : "黑名单为空"}</div>
        )}
        <ListPagination data={data} onPage={setPage} loading={loading}/>
      </section>
    </div>
  );
}
