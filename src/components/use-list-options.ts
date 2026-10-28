"use client";
import { useEffect, useState } from "react";
import { usePagedList } from "@/components/use-paged-list";
import type { PageResult } from "@/lib/pagination";

const empty: PageResult<never>={items:[],page:1,pageSize:50,total:0};
// Keep selected records across pages; opening a form never fetches every option.
export function useListOptions<T extends {id:string}>(kind:string,selectedIDs:string[]=[]) {
  const [search,setSearchValue]=useState("");
  const list=usePagedList<T>(kind,empty,{search});
  const [cache,setCache]=useState<Record<string,T>>({});
  const ids=selectedIDs.join(",");
  useEffect(()=>{
    const timer=setTimeout(()=>setCache(current=>{
      const next={...current};for(const item of list.data.items)next[item.id]=item;return next;
    }),0);
    return ()=>clearTimeout(timer);
  },[list.data]);
  useEffect(()=>{
    const controller=new AbortController();
    async function selected() {
      const values=ids.split(",").filter(Boolean);
      for(let offset=0;offset<values.length;offset+=50) {
        try {
          const query=new URLSearchParams({ids:values.slice(offset,offset+50).join(",")});
          const response=await fetch(`/api/lists/${kind}?${query}`,{cache:"no-store",signal:controller.signal});
          if(!response.ok)continue;
          const data:PageResult<T>=await response.json();
          if(!controller.signal.aborted)setCache(current=>{
            const next={...current};for(const item of data.items)next[item.id]=item;return next;
          });
        } catch { /* The primary options query provides errors and retry. */ }
      }
    }
    if(ids)void selected();
    return ()=>controller.abort();
  },[ids,kind]);
  return {...list,items:Object.values(cache),search,setSearch:(value:string)=>{setSearchValue(value);list.setPage(1);}};
}
