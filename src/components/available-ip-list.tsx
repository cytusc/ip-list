"use client";

import Link from "next/link";
import { NodeLinksDialog } from "@/components/node-links-dialog";
import { IPLocationCell } from "@/components/ip-location";
import { useEffect, useRef, useState } from "react";
import { Check, Copy, RefreshCw, Search } from "lucide-react";
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
import type { AvailableIP } from "@/lib/types";
import { toast } from "sonner";

import { protocolLabels } from "@/lib/protocol";
const protocolBadgeColors: Record<string, string> = {
  "vless/httpupgrade": "border-blue-200 bg-blue-50 text-blue-800",
  "vless/xhttp": "border-violet-200 bg-violet-50 text-violet-800",
  "vless/grpc": "border-amber-200 bg-amber-50 text-amber-800",
  "vless/ws": "border-cyan-200 bg-cyan-50 text-cyan-800",
  "vless/tcp": "border-rose-200 bg-rose-50 text-rose-800",
};
const pageSize = 20;
const formatTime = (value: string) =>
  new Date(value).toLocaleString("zh-CN", { hour12: false });

export function AvailableIPList({
  initialItems,
}: {
  initialItems: AvailableIP[];
}) {
  const [linkIP, setLinkIP] = useState<string | null>(null);
  const [items, setItems] = useState(initialItems);
  const [search, setSearch] = useState("");
  const [protocol, setProtocol] = useState("all");
  const [page, setPage] = useState(1);
  const [refreshing, setRefreshing] = useState(false);
  const [locationVersion, setLocationVersion] = useState(0);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState(false);
  const inFlight = useRef(false);
  const filtered = items.filter(
    (item) =>
      (item.ip.includes(search.trim()) ||
        `${item.asn || ""} ${item.provider || ""}`
          .toLowerCase()
          .includes(search.trim().toLowerCase()) ||
        item.checks.some((c) =>
          c.node_name.toLowerCase().includes(search.trim().toLowerCase()),
        )) &&
      (protocol === "all" || item.checks.some((c) => c.protocol === protocol)),
  );
  const pageCount = Math.max(1, Math.ceil(filtered.length / pageSize));
  const currentPage = Math.min(page, pageCount);
  const visible = filtered.slice(
    (currentPage - 1) * pageSize,
    currentPage * pageSize,
  );

  // Refresh without replacing the list with a loading screen or resetting filters.
  useEffect(() => {
    const controller = new AbortController();
    const timer = setInterval(() => {
      if (document.hidden || inFlight.current) return;
      inFlight.current = true;
      fetch("/api/available-ips", {
        cache: "no-store",
        signal: controller.signal,
      })
        .then(async (response) => {
          const data = await response.json();
          if (!response.ok) throw new Error(data.error);
          setItems(data);
          setError("");
        })
        .catch(() => {
          if (!controller.signal.aborted)
            setError("自动更新失败，当前显示上次结果，请刷新重试。");
        })
        .finally(() => {
          inFlight.current = false;
        });
    }, 10000);
    return () => {
      clearInterval(timer);
      controller.abort();
    };
  }, []);

  async function refresh() {
    if (inFlight.current) return;
    inFlight.current = true;
    setRefreshing(true);
    try {
      const response = await fetch("/api/available-ips", { cache: "no-store" });
      const data = await response.json();
      if (!response.ok) throw new Error(data.error);
      setItems(data);
      setLocationVersion((version) => version + 1);
      setError("");
    } catch (error) {
      setError(
        error instanceof Error ? error.message : "获取可用 IP 失败，请重试",
      );
    } finally {
      setRefreshing(false);
      inFlight.current = false;
    }
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(
        filtered.map((item) => item.ip).join("\n"),
      );
      setCopied(true);
      toast.success(`已复制 ${filtered.length} 个 IP`);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      toast.error("复制失败，请手动选择 IP 复制");
    }
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
            按协议与传输方式共用 IP
            池。同池所有节点可复用检测结果，新增节点无需重测。每轮结束后更新，页面每
            10 秒自动刷新。
          </p>
        </div>
        <Button asChild variant="outline">
          <Link href="/probes">前往连通性测试</Link>
        </Button>
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
                {items.length} 个 IP
              </span>
            </h2>
            <p className="mt-1 text-xs leading-5 text-muted-foreground">
              复测失败只移除对应协议池中的 IP；延迟来自该池最近一次成功检测。
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
              disabled={!filtered.length}
              onClick={copy}
            >
              {copied ? <Check /> : <Copy />}复制筛选 IP
            </Button>
          </div>
        </div>
        <div className="flex flex-col gap-3 p-4 sm:flex-row sm:p-5">
          <div className="relative flex-1 sm:max-w-sm">
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
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="pl-5">IP 地址</TableHead>
              <TableHead>归属地</TableHead>
              <TableHead>ASN / 服务商</TableHead>
              <TableHead>支持的协议</TableHead>
              <TableHead className="hidden pr-5 md:table-cell">
                最近测通
              </TableHead>
              <TableHead>节点链接</TableHead>
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
                    {item.ip}
                  </TableCell>
                  <TableCell className="max-w-44 whitespace-normal align-top text-xs">
                    <IPLocationCell key={`${item.ip}:${locationVersion}`} ip={item.ip} />
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
                  <TableCell className="hidden pr-5 text-xs text-muted-foreground md:table-cell">
                    {formatTime(latest.checked_at)}
                  </TableCell>
                  <TableCell className="align-top">
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => setLinkIP(item.ip)}
                      aria-label={`生成 ${item.ip} 节点`}
                    >
                      生成节点
                    </Button>
                  </TableCell>
                </TableRow>
              );
            })}
            {!visible.length && (
              <TableRow>
                <TableCell colSpan={6} className="h-44 text-center">
                  <p className="font-medium">
                    {items.length ? "没有匹配的 IP" : "暂无可用 IP"}
                  </p>
                  <p className="mt-2 text-sm text-muted-foreground">
                    {items.length
                      ? "调整搜索词或协议筛选后重试。"
                      : "先完成一次连通性测试，测通的 IP 会自动出现在这里。"}
                  </p>
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
        <div className="flex flex-wrap items-center justify-between gap-3 border-t p-4 text-xs text-muted-foreground">
          <span aria-live="polite">
            共 {filtered.length} 个 IP · 第 {currentPage} / {pageCount} 页
          </span>
          <div className="flex gap-2">
            <Button
              size="sm"
              variant="outline"
              disabled={currentPage === 1}
              onClick={() => setPage(currentPage - 1)}
            >
              上一页
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={currentPage === pageCount}
              onClick={() => setPage(currentPage + 1)}
            >
              下一页
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}
