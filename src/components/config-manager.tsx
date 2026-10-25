"use client";

import { useState, useTransition } from "react";
import {
  Server,
  Network as NetworkIcon,
  Plus,
  Search,
  Pencil,
  Trash2,
  ChevronLeft,
  ChevronRight,
  CircleCheck,
  CirclePause,
  Info,
  Loader2,
} from "lucide-react";
import { toast } from "sonner";
import { deleteConfig } from "@/app/actions";
import type { Node, Network } from "@/lib/types";
import { cn } from "@/lib/utils";
import { protocolLabels } from "@/lib/protocol";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
} from "@/components/ui/alert-dialog";
import { NodeImport } from "@/components/node-import";
import { ConfigForm } from "@/components/config-form";

export function ConfigManager({
  kind,
  nodes,
  networks,
}: {
  kind: "nodes" | "networks";
  nodes: Node[];
  networks: Network[];
}) {
  const isNode = kind === "nodes";
  const label = isNode ? "节点" : "网段";
  const items = isNode ? nodes : networks;
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("all");
  const [page, setPage] = useState(1);
  const [editor, setEditor] = useState<{ item: Node | Network | null } | null>(
    null,
  );
  const [deleting, setDeleting] = useState<Node | Network | null>(null);
  const [deleteError, setDeleteError] = useState("");
  const [pending, startTransition] = useTransition();
  const filtered = items.filter((item) => {
    const term = search.trim().toLowerCase();
    const text =
      "address" in item
        ? `${item.name} ${item.address} ${item.description}`
        : `${item.name} ${item.cidr} ${item.description} ${item.asn} ${item.provider} ${(item.disabled_protocols || []).map((protocol) => protocolLabels[protocol] || protocol).join(" ")}`;
    return (
      text.toLowerCase().includes(term) &&
      (status === "all" ||
        (status === "pool-disabled"
          ? "cidr" in item && !!item.disabled_protocols?.length
          : item.enabled === (status === "enabled")))
    );
  });
  const pageCount = Math.max(1, Math.ceil(filtered.length / 10));
  const currentPage = Math.min(page, pageCount);
  const visible = filtered.slice((currentPage - 1) * 10, currentPage * 10);
  const enabledCount = items.filter((item) => item.enabled).length;
  const Icon = isNode ? Server : NetworkIcon;
  const stats = [
    {
      title: `${label}总数`,
      value: items.length,
      description: `已添加的全部${label}`,
      icon: Icon,
      color: "bg-blue-50 text-blue-600",
    },
    {
      title: isNode ? "已启用节点" : "全局启用网段",
      value: enabledCount,
      description: `当前标记为启用的${label}`,
      icon: CircleCheck,
      color: "bg-emerald-50 text-emerald-600",
    },
    {
      title: isNode ? "已停用节点" : "全局停用网段",
      value: items.length - enabledCount,
      description: "保留配置，暂不参与检测",
      icon: CirclePause,
      color: "bg-amber-50 text-amber-600",
    },
  ];
  const addButton = (
    <Button onClick={() => setEditor({ item: null })} className="h-10">
      <Plus className="size-4" />
      新增{label}
    </Button>
  );
  return (
    <>
      <div className="mb-8 flex flex-wrap items-center justify-between gap-4">
        <div>
          <div className="mb-2 flex items-center gap-2 text-xs text-muted-foreground">
            <span className="size-1.5 rounded-full bg-primary" />
            配置管理
          </div>
          <h1 className="text-[26px] font-semibold tracking-tight">
            {isNode ? "节点管理" : "网段配置"}
          </h1>
          <p className="mt-2 text-sm text-muted-foreground">
            {isNode
              ? "集中管理接入节点，维护节点地址与基础配置。"
              : "统一维护网段与协议池状态。整段检测均失败时，仅在对应协议池禁用，可在连通性测试中恢复。"}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {isNode && <NodeImport />}
          {addButton}
        </div>
      </div>
      <div className="mb-8 grid gap-4 sm:grid-cols-3 xl:gap-6">
        {stats.map((stat) => (
          <div
            key={stat.title}
            className="flex justify-between rounded-xl border bg-white p-5 xl:p-6"
          >
            <div>
              <p className="text-xs font-medium text-muted-foreground">
                {stat.title}
              </p>
              <p className="mt-3 text-3xl font-semibold tabular-nums tracking-tight">
                {stat.value}
                <span className="ml-2 text-xs font-normal text-muted-foreground">
                  个
                </span>
              </p>
              <p className="mt-3 text-[11px] text-muted-foreground">
                {stat.description}
              </p>
            </div>
            <span
              className={cn(
                "flex size-10 items-center justify-center rounded-xl",
                stat.color,
              )}
            >
              <stat.icon className="size-5" />
            </span>
          </div>
        ))}
      </div>
      <section className="overflow-hidden rounded-xl border bg-white">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b px-6 py-5">
          <div className="flex items-center gap-2.5">
            <h2 className="text-sm font-semibold">{label}列表</h2>
            <Badge
              variant="secondary"
              className="rounded-md font-normal tabular-nums"
            >
              {items.length}
            </Badge>
          </div>
          <p className="text-xs text-muted-foreground">
            管理{label}的基础信息与启用状态
          </p>
        </div>
        <div className="flex flex-wrap items-center justify-between gap-3 px-5 py-4">
          <div className="relative w-full sm:max-w-xs">
            <Search className="absolute top-3 left-3 size-4 text-slate-400" />
            <Input
              value={search}
              aria-label={`搜索${label}`}
              onChange={(event) => {
                setSearch(event.target.value);
                setPage(1);
              }}
              placeholder={
                isNode
                  ? "搜索节点名称、地址或备注…"
                  : "搜索网段名称、CIDR 或备注…"
              }
              className="h-10 bg-white pl-9"
            />
          </div>
          <div className="flex flex-wrap gap-2">
            <Select
              value={status}
              onValueChange={(value) => {
                setStatus(value);
                setPage(1);
              }}
            >
              <SelectTrigger className="w-32 bg-white" aria-label="按状态筛选">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部状态</SelectItem>
                <SelectItem value="enabled">已启用</SelectItem>
                <SelectItem value="disabled">已停用</SelectItem>
                {!isNode && (
                  <SelectItem value="pool-disabled">协议池已禁用</SelectItem>
                )}
              </SelectContent>
            </Select>
          </div>
        </div>
        <Table>
          <TableHeader>
            <TableRow className="bg-slate-50/80 hover:bg-slate-50/80">
              <TableHead className="pl-6">{label}名称</TableHead>
              <TableHead>{isNode ? "节点地址" : "CIDR 网段"}</TableHead>
              {!isNode && <TableHead>ASN / 服务商</TableHead>}
              <TableHead>{isNode ? "状态" : "全局 / 协议池状态"}</TableHead>
              <TableHead className="hidden xl:table-cell">更新时间</TableHead>
              <TableHead className="pr-6 text-right">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {visible.map((item) => (
              <TableRow key={item.id} className="h-[78px]">
                <TableCell className="pl-6">
                  <div className="flex items-center gap-3">
                    <span className="hidden size-9 shrink-0 items-center justify-center rounded-lg border bg-slate-50 text-slate-500 lg:flex">
                      <Icon className="size-4" />
                    </span>
                    <div className="min-w-0">
                      <p
                        className="max-w-[200px] truncate font-medium"
                        title={item.name}
                      >
                        {item.name}
                      </p>
                      <p
                        className="mt-1 max-w-[200px] truncate text-xs text-muted-foreground"
                        title={item.description}
                      >
                        {item.description || "暂无备注"}
                      </p>
                    </div>
                  </div>
                </TableCell>
                <TableCell className="font-mono text-xs text-slate-600">
                  {"address" in item
                    ? `${item.address.includes(":") ? `[${item.address}]` : item.address}:${item.port}`
                    : item.cidr}
                </TableCell>
                {"asn" in item && (
                  <TableCell className="max-w-48 whitespace-normal text-xs">
                    <p className="font-mono">{item.asn || "—"}</p>
                    <p className="mt-1 break-words text-muted-foreground">
                      {item.provider || "未填写"}
                    </p>
                  </TableCell>
                )}
                <TableCell>
                  <Badge
                    variant="outline"
                    className={cn(
                      "gap-1.5 rounded-md border-0 px-2 py-1 text-[11px] font-normal",
                      item.enabled
                        ? "bg-emerald-50 text-emerald-700"
                        : "bg-slate-100 text-slate-600",
                    )}
                  >
                    <span
                      className={cn(
                        "size-1.5 rounded-full",
                        item.enabled ? "bg-emerald-500" : "bg-slate-400",
                      )}
                    />
                    {item.enabled ? "已启用" : "已停用"}
                  </Badge>
                  {"cidr" in item && !!item.disabled_protocols?.length && (
                    <div className="mt-1.5 flex max-w-72 flex-wrap gap-1">
                      {item.disabled_protocols.map((protocol) => (
                        <Badge
                          key={protocol}
                          variant="outline"
                          title="当前协议池已禁用"
                          className="text-[11px] font-normal"
                          style={{
                            backgroundColor: "#991b1b",
                            borderColor: "#991b1b",
                            color: "#ffffff",
                          }}
                        >
                          {protocolLabels[protocol] || protocol}
                        </Badge>
                      ))}
                    </div>
                  )}
                </TableCell>
                <TableCell className="hidden text-xs text-muted-foreground xl:table-cell">
                  {new Date(item.updated_at).toLocaleString("zh-CN", {
                    timeZone: "Asia/Shanghai",
                    month: "2-digit",
                    day: "2-digit",
                    hour: "2-digit",
                    minute: "2-digit",
                    hour12: false,
                  })}
                </TableCell>
                <TableCell className="pr-5 text-right">
                  <div className="flex justify-end gap-1">
                    <Button
                      variant="ghost"
                      size="icon"
                      aria-label={`编辑${item.name}`}
                      title="编辑"
                      className="size-8 text-slate-500"
                      onClick={() => setEditor({ item })}
                    >
                      <Pencil className="size-3.5" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      aria-label={`删除${item.name}`}
                      title="删除"
                      className="size-8 text-slate-500 hover:bg-red-50 hover:text-red-600"
                      onClick={() => {
                        setDeleting(item);
                        setDeleteError("");
                      }}
                    >
                      <Trash2 className="size-3.5" />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {!visible.length && (
          <div className="flex min-h-64 flex-col items-center justify-center gap-3 p-8 text-center">
            <span className="mb-1 rounded-2xl border bg-slate-50 p-4">
              <Icon className="size-7 text-slate-400" />
            </span>
            <h3 className="text-sm font-medium">
              {items.length ? "没有匹配的配置" : `还没有${label}配置`}
            </h3>
            <p className="max-w-sm text-xs leading-6 text-muted-foreground">
              {items.length
                ? "尝试更换关键词，或清除筛选条件。"
                : isNode
                  ? "添加第一个节点，用于测试网段内 IP 的代理可用性。"
                  : "添加 IPv4 或 IPv6 网段，供代理节点和 CF 检测复用。"}
            </p>
            {items.length ? (
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  setSearch("");
                  setStatus("all");
                }}
              >
                清除筛选
              </Button>
            ) : (
              <Button
                variant="outline"
                size="sm"
                onClick={() => setEditor({ item: null })}
              >
                <Plus />
                新增{label}
              </Button>
            )}
          </div>
        )}
        <div className="flex items-center justify-between border-t px-6 py-4">
          <p className="text-xs text-muted-foreground">
            共 {filtered.length} 条配置 · 每页 10 条
          </p>
          <div className="flex items-center gap-3">
            <span className="text-xs text-muted-foreground">
              {currentPage} / {pageCount}
            </span>
            <Button
              variant="outline"
              size="icon"
              className="size-8 bg-white"
              aria-label="上一页"
              disabled={currentPage === 1}
              onClick={() => setPage(currentPage - 1)}
            >
              <ChevronLeft />
            </Button>
            <Button
              variant="outline"
              size="icon"
              className="size-8 bg-white"
              aria-label="下一页"
              disabled={currentPage === pageCount}
              onClick={() => setPage(currentPage + 1)}
            >
              <ChevronRight />
            </Button>
          </div>
        </div>
      </section>
      <p className="mt-5 flex items-start gap-2 text-xs leading-5 text-muted-foreground">
        <Info className="mt-0.5 size-3.5 shrink-0" />
        {isNode
          ? "节点与网段独立维护，在连通性测试中按需组合。启用状态不代表在线状态。"
          : "网段可用于代理节点与 CF 检测；ASN 和服务商信息会展示在可用 IP 列表中。"}
      </p>
      {editor && (
        <ConfigForm
          kind={kind}
          item={editor.item}
          onClose={() => setEditor(null)}
        />
      )}
      <AlertDialog
        open={Boolean(deleting)}
        onOpenChange={(open) => {
          if (!open && !pending) setDeleting(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              删除{label}「{deleting?.name}」？
            </AlertDialogTitle>
            <AlertDialogDescription>
              删除后无法恢复。
              {isNode
                ? "对应节点的检测结果也将移除。"
                : "该网段的配置将被永久移除。"}
            </AlertDialogDescription>
          </AlertDialogHeader>
          {deleteError && (
            <p
              role="alert"
              className="rounded-md bg-red-50 p-3 text-sm text-red-700"
            >
              {deleteError}
            </p>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={pending}>取消</AlertDialogCancel>
            <Button
              variant="destructive"
              disabled={pending}
              onClick={() => {
                if (!deleting) return;
                setDeleteError("");
                startTransition(async () => {
                  try {
                    const result = await deleteConfig(kind, deleting.id);
                    if (result.ok) {
                      toast.success(`${label}已删除`);
                      setDeleting(null);
                    } else setDeleteError(result.error);
                  } catch {
                    setDeleteError("删除失败，请检查网络后重试");
                  }
                });
              }}
            >
              {pending ? (
                <>
                  <Loader2 className="animate-spin" />
                  正在删除…
                </>
              ) : (
                "确认删除"
              )}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
