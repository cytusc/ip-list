"use client";

import Link from "next/link";
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
  Layers,
  Info,
  Loader2,
  ArrowUpRight,
} from "lucide-react";
import { toast } from "sonner";
import { deleteConfig } from "@/app/actions";
import type { Node, Network } from "@/lib/types";
import { cn } from "@/lib/utils";
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
  initialNodeFilter = "all",
}: {
  kind: "nodes" | "networks";
  nodes: Node[];
  networks: Network[];
  initialNodeFilter?: string;
}) {
  const isNode = kind === "nodes";
  const label = isNode ? "节点" : "网段";
  const items = isNode ? nodes : networks;
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("all");
  const [nodeFilter, setNodeFilter] = useState(initialNodeFilter);
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
        : `${item.name} ${item.cidr} ${item.node_name} ${item.description}`;
    return (
      text.toLowerCase().includes(term) &&
      (status === "all" || item.enabled === (status === "enabled")) &&
      (isNode ||
        nodeFilter === "all" ||
        ("node_id" in item && item.node_id === nodeFilter))
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
      title: `已启用${label}`,
      value: enabledCount,
      description: `当前标记为启用的${label}`,
      icon: CircleCheck,
      color: "bg-emerald-50 text-emerald-600",
    },
    isNode
      ? {
          title: "关联网段",
          value: nodes.reduce((sum, node) => sum + node.network_count, 0),
          description: "分配至各节点的网段配置",
          icon: Layers,
          color: "bg-violet-50 text-violet-600",
        }
      : {
          title: "阻止策略",
          value: networks.filter((item) => item.action === "deny").length,
          description: "使用阻止策略的网段",
          icon: CirclePause,
          color: "bg-amber-50 text-amber-600",
        },
  ];
  const addButton = (
    <Button
      onClick={() => setEditor({ item: null })}
      disabled={!isNode && !nodes.length}
      className="h-10"
    >
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
              : "为节点配置 IP 网段，清晰定义允许与阻止策略。"}
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
      {!isNode && !nodes.length && (
        <div className="mb-5 flex flex-wrap items-center gap-2 rounded-lg border border-blue-100 bg-blue-50 p-4 text-sm text-blue-800">
          <Info className="size-4" />
          配置网段前，请先创建一个节点。
          <Link
            href="/nodes"
            className="inline-flex items-center gap-1 font-medium underline underline-offset-4"
          >
            前往节点管理
            <ArrowUpRight className="size-3" />
          </Link>
        </div>
      )}
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
            {!isNode && (
              <Select
                value={nodeFilter}
                onValueChange={(value) => {
                  setNodeFilter(value);
                  setPage(1);
                }}
              >
                <SelectTrigger
                  className="w-36 bg-white"
                  aria-label="按节点筛选"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">全部节点</SelectItem>
                  {nodes.map((node) => (
                    <SelectItem key={node.id} value={node.id}>
                      {node.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
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
              </SelectContent>
            </Select>
          </div>
        </div>
        <Table>
          <TableHeader>
            <TableRow className="bg-slate-50/80 hover:bg-slate-50/80">
              <TableHead className="pl-6">{label}名称</TableHead>
              <TableHead>{isNode ? "节点地址" : "CIDR 网段"}</TableHead>
              <TableHead>{isNode ? "关联网段" : "所属节点"}</TableHead>
              {!isNode && <TableHead>策略</TableHead>}
              <TableHead>状态</TableHead>
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
                <TableCell>
                  {"network_count" in item ? (
                    <Link
                      href={`/networks?node=${item.id}`}
                      className="rounded-md border px-2 py-1 text-xs text-slate-600 hover:border-primary hover:text-primary"
                    >
                      {item.network_count} 个网段
                    </Link>
                  ) : (
                    <span className="text-xs text-slate-600">
                      {item.node_name}
                    </span>
                  )}
                </TableCell>
                {"action" in item && (
                  <TableCell>
                    <Badge
                      variant="outline"
                      className={
                        item.action === "allow"
                          ? "border-blue-100 bg-blue-50 text-blue-700"
                          : "border-amber-100 bg-amber-50 text-amber-800"
                      }
                    >
                      {item.action === "allow" ? "允许" : "阻止"}
                    </Badge>
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
                  ? "添加第一个节点，随后就可以为它配置 IP 网段。"
                  : "添加 IPv4 或 IPv6 网段，并选择对应的节点和策略。"}
            </p>
            {items.length ? (
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  setSearch("");
                  setStatus("all");
                  setNodeFilter("all");
                }}
              >
                清除筛选
              </Button>
            ) : (
              <Button
                variant="outline"
                size="sm"
                disabled={!isNode && !nodes.length}
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
          ? "节点用于关联网段配置。启用状态表示配置开关，不代表节点实际在线状态。"
          : "网段可用于批量筛选可用代理 IP；规则下发与流量过滤尚未实现。"}
      </p>
      {editor && (
        <ConfigForm
          kind={kind}
          nodes={nodes}
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
                ? "如节点关联了网段，请先删除或迁移对应的网段配置。"
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
