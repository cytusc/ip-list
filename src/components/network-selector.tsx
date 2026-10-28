"use client";

import Link from "next/link";
import { useState } from "react";
import { Check, ChevronsUpDown, Network as NetworkIcon, X } from "lucide-react";
import type { Network } from "@/lib/types";
import { networkAddressCount } from "@/lib/network";
import { cn } from "@/lib/utils";
import { ListPagination } from "@/components/list-pagination";
import type { useListOptions } from "@/components/use-list-options";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  Command,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";

export function NetworkSelector({
  networks,
  options,
  value,
  onChange,
  disabled = false,
}: {
  networks: Network[];
  options: ReturnType<typeof useListOptions<Network>>;
  value: string[];
  onChange: (ids: string[]) => void;
  disabled?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const query=options.search,setQuery=options.setSearch;
  const available = networks.filter((network) => network.enabled);
  const selected = available.filter((network) => value.includes(network.id));
  const filtered=options.data.items.filter(item=>available.some(network=>network.id===item.id));
  function toggle(id: string) {
    if (disabled) return;
    onChange(
      value.includes(id) ? value.filter((item) => item !== id) : [...value, id],
    );
  }

  return (
    <div className="min-w-0 space-y-3">
      <Popover
        open={open && !disabled}
        onOpenChange={(next) => {
          setOpen(next);
          if (!next) setQuery("");
        }}
      >
        <PopoverTrigger asChild>
          <Button
            type="button"
            variant="outline"
            disabled={disabled}
            aria-label="选择检测网段"
            className="w-full justify-between"
          >
            <span className="flex min-w-0 items-center gap-2">
              <NetworkIcon className="size-4 shrink-0 text-muted-foreground" />
              {selected.length
                ? `已选 ${selected.length} 个网段`
                : "搜索并选择网段…"}
            </span>
            <ChevronsUpDown className="size-4 shrink-0 text-muted-foreground" />
          </Button>
        </PopoverTrigger>
        <PopoverContent
          aria-label="检测网段选择"
          className="flex max-h-[var(--radix-popover-content-available-height)] w-[var(--radix-popover-trigger-width)] max-w-[calc(100vw-2rem)] flex-col overflow-hidden p-0"
        >
          <Command shouldFilter={false} className="min-h-0">
            <CommandInput
              aria-label="搜索网段"
              placeholder="搜索名称、CIDR、ASN、服务商…"
              value={query}
              onValueChange={setQuery}
              disabled={disabled}
            />
            <div className="flex flex-wrap items-center justify-between gap-2 border-b px-3 py-2">
              <span className="text-xs text-muted-foreground">
                {options.data.total} 个结果
              </span>
              <div className="flex gap-1">
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  disabled={
                    disabled ||
                    !filtered.some((network) => !value.includes(network.id))
                  }
                  onClick={() =>
                    onChange([
                      ...new Set([
                        ...value,
                        ...filtered.map((network) => network.id),
                      ]),
                    ])
                  }
                >
                  全选当前页
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  disabled={disabled || !value.length}
                  onClick={() => onChange([])}
                >
                  清空
                </Button>
              </div>
            </div>
            <CommandList aria-label="网段列表">
              {options.error && <p role="alert" className="p-3 text-sm text-destructive">{options.error}</p>}
              {!filtered.length && (
                <p className="px-3 py-8 text-center text-sm text-muted-foreground">
                  {options.loading ? "正在加载网段…" : "未找到匹配网段，请尝试其他关键词。"}
                </p>
              )}
              {filtered.map((network) => {
                const checked = value.includes(network.id);
                return (
                  <CommandItem
                    key={network.id}
                    value={network.id}
                    aria-checked={checked}
                    disabled={disabled}
                    onSelect={() => toggle(network.id)}
                  >
                    <span
                      aria-hidden="true"
                      className={cn(
                        "flex size-4 shrink-0 items-center justify-center rounded border",
                        checked &&
                          "border-primary bg-primary text-primary-foreground",
                      )}
                    >
                      {checked && <Check className="size-3" />}
                    </span>
                    <span className="min-w-0 flex-1 [overflow-wrap:anywhere]">
                      <span className="font-medium">{network.name}</span>
                      <span className="block font-mono text-xs text-muted-foreground">
                        {network.cidr} ·{" "}
                        {networkAddressCount(network.cidr).toString()} IP
                      </span>
                      <span className="block text-xs text-muted-foreground">
                        {[network.asn, network.provider]
                          .filter(Boolean)
                          .join(" · ") || "未填写归属信息"}
                      </span>
                    </span>
                    <span className="sr-only">
                      {checked ? "已勾选" : "未勾选"}
                    </span>
                  </CommandItem>
                );
              })}
            </CommandList>
            <ListPagination data={options.data} onPage={options.setPage} loading={options.loading}/>
          </Command>
          <div className="flex shrink-0 items-center justify-between border-t px-3 py-2">
            <span role="status" className="text-xs text-muted-foreground">
              已选 {selected.length} 个网段
            </span>
            <Button
              type="button"
              variant="secondary"
              size="sm"
              onClick={() => {
                setOpen(false);
                setQuery("");
              }}
            >
              完成
            </Button>
          </div>
        </PopoverContent>
      </Popover>
      {!!selected.length && (
        <div
          className="flex max-h-28 flex-wrap gap-2 overflow-y-auto"
          aria-label="已选网段"
        >
          {selected.map((network) => (
            <Badge
              key={network.id}
              variant="secondary"
              className="max-w-full gap-1 py-1 pl-2 pr-1"
            >
              <span
                className="truncate"
                title={`${network.name} ${network.cidr}`}
              >
                {network.name} · {network.cidr}
              </span>
              <button
                type="button"
                disabled={disabled}
                onClick={() => toggle(network.id)}
                aria-label={`移除网段 ${network.name}`}
                className="flex size-7 shrink-0 items-center justify-center rounded-full hover:bg-muted focus-visible:outline-ring disabled:opacity-50"
              >
                <X className="size-3" />
              </button>
            </Badge>
          ))}
        </div>
      )}
      {!available.length && (
        <p className="text-sm text-muted-foreground">
          暂无已启用的网段，
          <Link href="/networks" className="text-primary underline">
            前往添加网段
          </Link>
          。
        </p>
      )}
    </div>
  );
}
