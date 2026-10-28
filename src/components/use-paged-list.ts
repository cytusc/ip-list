"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import type { PageResult } from "@/lib/pagination";

// Abort old filters/pages and allow only one poll at a time. Retain the last
// successful page when the server is unavailable.
export function usePagedList<T>(kind:string,initial:PageResult<T>,filters:Record<string,string>={},interval:number | ((data:PageResult<T>)=>number)=0) {
  const [data,setData]=useState(initial);
  const pollInterval=typeof interval==="function" ? interval(data) : interval;
  const [page,setPage]=useState(initial.page);
  const [loading,setLoading]=useState(false);
  const [error,setError]=useState("");
  const [version,setVersion]=useState(0);
  const query=new URLSearchParams({...filters,page:String(page)}).toString();
  const latest=useRef(query);
  const refresh=useCallback(()=>setVersion(v=>v+1),[]);
  useEffect(()=>{
    const controller=new AbortController();
    latest.current=query;
    let inFlight=false;
    let retry: ReturnType<typeof setTimeout> | undefined;
    async function load() {
      if(inFlight) return;
      inFlight=true;setLoading(true);
      try {
        const response=await fetch(`/api/lists/${kind}?${query}`,{cache:"no-store",signal:controller.signal});
        const payload=await response.json();
        if(!response.ok)throw new Error(payload.error || "列表加载失败");
        if(!controller.signal.aborted && latest.current===query) {clearTimeout(retry);setData(payload);setError("");}
      } catch(caught) {
        if(!controller.signal.aborted) {
          setError(caught instanceof Error ? caught.message : "列表加载失败，请重试");
          clearTimeout(retry);
          retry=setTimeout(load,5000);
        }
      } finally {inFlight=false;if(!controller.signal.aborted)setLoading(false);}
    }
    const timer=window.setTimeout(load,250);
    const poll=pollInterval ? window.setInterval(()=>{if(!document.hidden)void load();},pollInterval) : undefined;
    return ()=>{controller.abort();clearTimeout(timer);clearInterval(poll);clearTimeout(retry);};
  },[kind,query,pollInterval,version,initial]);
  return {data,setData,page,setPage,loading,error,refresh};
}
