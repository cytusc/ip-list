"use client";

import Link from "next/link";
import { NodeLinksDialog } from "@/components/node-links-dialog";
import { countryName, IPLocationCell } from "@/components/ip-location";
import { useEffect, useRef, useState } from "react";
import { Ban, Check, Copy, RefreshCw, Search } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { AvailableIP, IPLocation } from "@/lib/types";
import { toast } from "sonner";
import { addBlacklist } from "@/app/actions";

import { ProbeLatencyStrip } from "@/components/probe-latency-strip";
import { protocolLabels } from "@/lib/protocol";
import type { PageResult } from "@/lib/pagination";
import { usePagedList } from "@/components/use-paged-list";
import { ListPagination } from "@/components/list-pagination";
const protocolBadgeColors: Record<string, string> = {
  "vless/httpupgrade": "border-blue-200 bg-blue-50 text-blue-800",
  "vless/xhttp": "border-violet-200 bg-violet-50 text-violet-800",
  "vless/grpc": "border-amber-200 bg-amber-50 text-amber-800",
  "vless/ws": "border-cyan-200 bg-cyan-50 text-cyan-800",
  "vless/tcp": "border-rose-200 bg-rose-50 text-rose-800",
};
const formatTime = (value: string) =>
  new Date(value).toLocaleString("zh-CN", { hour12: false });

export function AvailableIPList({
  initialItems,
}: {
  initialItems: PageResult<AvailableIP>;
}) {
  const [linkIP, setLinkIP] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const [protocol, setProtocol] = useState("all");
  const [region, setRegion] = useState("all");
  const [provider, setProvider] = useState("all");
  const {data,setPage,refresh:reload,loading:refreshing,error}=usePagedList("available-ips",initialItems,{search:search.trim(),protocol,region,provider},10000);
  const items=data.items;
  const [locationVersion, setLocationVersion] = useState(0);
  const [copied, setCopied] = useState(false);
  const [locations, setLocations] = useState<Record<string, IPLocation | null>>({});
  const locationCache = useRef<Record<string, IPLocation | null>>({});
  const locationIPs = items.map(item => item.ip).join(",");

  // Resolve every page with bounded concurrency; telemetry refreshes do not
  // restart lookups for addresses whose location has already been resolved.
  useEffect(() => {
    const controller = new AbortController();
    const pending = locationIPs.split(",").filter(ip => ip && locationCache.current[ip] === undefined);
    if (pending.length === 0) return () => controller.abort();

    async function loadLocations() {
      try {
        const params = new URLSearchParams();
        pending.forEach(ip => params.append("ip", ip));
        const response = await fetch(`/api/available-ips/locations?${params}`, { signal: controller.signal });
        if (!response.ok) throw new Error("查询失败");
        const results: { ip: string; location: IPLocation | null }[] = await response.json();
        if (controller.signal.aborted) return;
        const resolved: Record<string, IPLocation | null> = {};
        for (const { ip, location } of results) {
          const validLocation = location && /^[A-Z]{2}$/.test(location.country) ? location : null;
          locationCache.current[ip] = validLocation;
          resolved[ip] = validLocation;
        }
        setLocations(current => ({ ...current, ...resolved }));
      } catch {
        // Failed lookups remain available under unknown region.
      }
    }

    void loadLocations();
    return () => controller.abort();
  }, [locationIPs, locationVersion]);

  const countries = [...new Set([
    ...(data.countries || []),
    ...items.flatMap(item => locations[item.ip]?.country ? [locations[item.ip]!.country] : []),
    ...(region !== "all" && region !== "unknown" ? [region] : []),
  ])].sort((a, b) => countryName(a).localeCompare(countryName(b), "zh-CN"));
  const providers = [...new Set([
    ...(data.providers || []),
    ...(provider.startsWith("provider:") ? [provider.slice(9)] : []),
  ])].sort((a, b) => a.localeCompare(b, "zh-CN"));
  const locationsResolved = items.filter(item => locations[item.ip] !== undefined).length;
  const filtersActive = search !== "" || protocol !== "all" || region !== "all" || provider !== "all";
  function clearFilters() {
    setSearch(""); setProtocol("all"); setRegion("all"); setProvider("all"); setPage(1);
  }
  const visible=items;
  function refresh() {
      reload();
      locationCache.current = {};
      setLocations({});
      setLocationVersion((version) => version + 1);
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(
        items.map((item) => item.ip).join("\n"),
      );
      setCopied(true);
      toast.success(`已复制当前页 ${items.length} 个 IP`);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      toast.error("复制失败，请手动选择 IP 复制");
    }
  }

  async function copyIP(ip: string) {
    try {
      await navigator.clipboard.writeText(ip);
      toast.success(`已复制 ${ip}`);
    } catch {
      toast.error("复制失败，请手动选择 IP 复制");
    }
  }

  async function blacklist(ip: string) {
    const result = await addBlacklist([ip]);
    if (!result.ok) {
      toast.error(result.error);
      return;
    }
    reload();
    toast.success(`${ip} 已加入黑名单`);
  }

  return (
    <div className="space-y-6">
      {linkIP && (
        <NodeLinksDialog
          key={linkIP}
          ip={linkIP}
          onClose={() => setLinkIP(null)}
        />
      )}
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">可用 IP</h1>
          <p className="mt-2 max-w-2xl text-sm leading-6 text-muted-foreground">
            仅显示通过代理请求检测的 IP；TLS 节点还必须通过 3 次入口证书校验。证书异常地址已隔离，历史数据会自动补检。每轮结束后更新，页面每 10 秒自动刷新。
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button asChild variant="outline"><Link href="/certificate-issues">证书异常 IP</Link></Button>
          <Button asChild variant="outline"><Link href="/probes">前往连通性测试</Link></Button>
        </div>
      </div>
      {error && (
        <div
          role="alert"
          className="rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive"
        >
          {error}
        </div>
      )}
      <div className="overflow-hidden rounded-xl border bg-white">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b p-4 sm:p-5">
          <div>
            <h2 className="font-semibold">
              可用地址{" "}
              <span className="ml-2 text-sm font-normal text-muted-foreground">
                {data.allTotal ?? data.total} 个 IP
              </span>
            </h2>
            <p className="mt-1 text-xs leading-5 text-muted-foreground">
              色条每格代表 1 分钟的平均延迟，忽略偶发超时；左旧右新，显示最近 16 分钟。当前分钟持续更新，悬停查看详情。
            </p>
          </div>
          <div className="flex gap-2">
            <Button
              variant="outline"
              size="sm"
              disabled={refreshing}
              onClick={refresh}
            >
              <RefreshCw className={refreshing ? "animate-spin" : ""} />
              {refreshing ? "刷新中" : "刷新"}
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={!items.length}
              onClick={copy}
            >
              {copied ? <Check /> : <Copy />}复制当前页 IP
            </Button>
          </div>
        </div>
        <div className="flex flex-col flex-wrap gap-3 p-4 sm:flex-row sm:p-5">
          <div className="relative min-w-48 flex-1 sm:max-w-sm">
            <Search className="pointer-events-none absolute top-3 left-3 size-4 text-muted-foreground" />
            <Input
              aria-label="搜索 IP、节点、ASN 或服务商"
              placeholder="搜索 IP、节点、ASN 或服务商"
              className="pl-9"
              value={search}
              onChange={(e) => {
                setSearch(e.target.value);
                setPage(1);
              }}
            />
          </div>
          <Select
            value={protocol}
            onValueChange={(value) => {
              setProtocol(value);
              setPage(1);
            }}
          >
            <SelectTrigger aria-label="协议筛选" className="w-full sm:w-44">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部协议</SelectItem>
              {Object.entries(protocolLabels).map(([value, label]) => (
                <SelectItem key={value} value={value}>
                  {label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={region} onValueChange={value => { setRegion(value); setPage(1); }}>
            <SelectTrigger aria-label="地区筛选" className="w-full sm:w-44"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部地区</SelectItem>
              {countries.map(code => <SelectItem key={code} value={code}>{countryName(code)}</SelectItem>)}
              <SelectItem value="unknown">未知地区</SelectItem>
            </SelectContent>
          </Select>
          <Select value={provider} onValueChange={value => { setProvider(value); setPage(1); }}>
            <SelectTrigger aria-label="服务商筛选" className="w-full sm:w-56"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部服务商</SelectItem>
              {providers.map(value => <SelectItem key={value} value={`provider:${value}`}>{value}</SelectItem>)}
              <SelectItem value="missing">未填写服务商</SelectItem>
            </SelectContent>
          </Select>
          {filtersActive && <Button variant="ghost" onClick={clearFilters}>清除筛选</Button>}
        </div>
        <p className="px-5 pb-3 text-xs text-muted-foreground" role="status">
          匹配 {data.total} / {data.allTotal ?? data.total} 个 IP
          {locationsResolved < items.length ? ` · 地区查询中 ${locationsResolved}/${items.length}，筛选结果会随查询更新` : " · 地区信息已加载"}
        </p>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 px-5 pb-4 text-xs text-muted-foreground" aria-label="探针色条图例">
          <span className="inline-flex items-center gap-1.5"><i className="size-2.5 rounded-sm bg-emerald-500" />低延迟</span>
          <span className="inline-flex items-center gap-1.5"><i className="size-2.5 rounded-sm bg-amber-400" />中等延迟</span>
          <span className="inline-flex items-center gap-1.5"><i className="size-2.5 rounded-sm bg-rose-500" />高延迟 / 持续不可用</span>
          <span className="inline-flex items-center gap-1.5"><i className="size-2.5 rounded-sm bg-slate-200" />待确认 / 缺失 / 过期</span>
          <span>按 IP 地区分级 · 悬停查看阈值</span>
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="pl-5">IP 地址</TableHead>
              <TableHead>归属地</TableHead>
              <TableHead>ASN / 服务商</TableHead>
              <TableHead>支持的协议</TableHead>
              <TableHead>探针延迟</TableHead>
              <TableHead className="hidden pr-5 md:table-cell">
                最近测通
              </TableHead>
              <TableHead>操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {visible.map((item) => {
              const protocols = [
                ...new Set(item.checks.map((c) => c.protocol)),
              ];
              const latest = item.checks.reduce((a, b) =>
                a.checked_at > b.checked_at ? a : b,
              );
              return (
                <TableRow key={item.ip}>
                  <TableCell className="max-w-44 break-all whitespace-normal pl-5 align-top font-mono text-sm">
                    <button
                      type="button"
                      className="cursor-pointer rounded-sm text-left hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      onClick={() => copyIP(item.ip)}
                      aria-label={`复制 ${item.ip}`}
                      title="点击复制 IP"
                    >
                      {item.ip}
                    </button>
                  </TableCell>
                  <TableCell className="max-w-44 whitespace-normal align-top text-xs">
                    <IPLocationCell location={locations[item.ip]} />
                  </TableCell>
                  <TableCell className="max-w-44 whitespace-normal align-top text-xs">
                    <p className="font-mono">{item.asn || "—"}</p>
                    <p className="mt-1 break-words text-muted-foreground">
                      {item.provider || "未填写"}
                    </p>
                  </TableCell>
                  <TableCell className="whitespace-normal">
                    <div className="flex flex-wrap gap-2">
                      {protocols.map((type) => {
                        return (
                          <Badge
                            key={type}
                            variant="outline"
                            className={protocolBadgeColors[type] || ""}
                          >
                            {protocolLabels[type] || type}
                          </Badge>
                        );
                      })}
                    </div>
                    <p className="mt-2 text-xs text-muted-foreground md:hidden">
                      {formatTime(latest.checked_at)}
                    </p>
                  </TableCell>
                  <TableCell className="min-w-36 align-top">
                    {item.probe_delays?.length ? (
                      <div className="flex flex-wrap gap-1.5">
                        {item.probe_delays.map((probe) => (
                          <ProbeLatencyStrip key={probe.probe_id} probe={probe} ip={item.ip} location={locations[item.ip]} />
                        ))}
                      </div>
                    ) : (
                      <span className="text-xs text-muted-foreground">尚未配置</span>
                    )}
                  </TableCell>
                  <TableCell className="hidden pr-5 text-xs text-muted-foreground md:table-cell">
                    {formatTime(latest.checked_at)}
                  </TableCell>
                  <TableCell className="align-top">
                    <div className="flex flex-wrap gap-2">
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => setLinkIP(item.ip)}
                      aria-label={`生成 ${item.ip} 节点`}
                    >
                      生成节点
                    </Button>
                    <Button variant="ghost" size="sm" onClick={() => blacklist(item.ip)} aria-label={`屏蔽 ${item.ip}`}>
                      <Ban />屏蔽
                    </Button>
                    </div>
                  </TableCell>
                </TableRow>
              );
            })}
            {!visible.length && (
              <TableRow>
                <TableCell colSpan={7} className="h-44 text-center">
                  <p className="font-medium">
                    {data.allTotal ? "没有匹配的 IP" : "暂无可用 IP"}
                  </p>
                  <p className="mt-2 text-sm text-muted-foreground">
                    {data.allTotal
                      ? "调整搜索词、地区、服务商或协议筛选后重试。"
                      : "先完成连通性测试，检测通过的 IP 会显示在这里；TLS 节点还需通过 3 次入口证书校验。"}
                  </p>
                  {filtersActive && <Button variant="outline" size="sm" className="mt-3" onClick={clearFilters}>清除筛选</Button>}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
        <div className="flex flex-wrap items-center justify-between gap-3 border-t p-4 text-xs text-muted-foreground">
          <span aria-live="polite">
            共 {data.total} 个 IP
          </span>
          <ListPagination data={data} onPage={setPage} loading={refreshing}/>
        </div>
      </div>
    </div>
  );
}
