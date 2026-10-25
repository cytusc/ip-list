"use client";

import { useEffect, useState } from "react";
import { toast } from "sonner";
import { generateIPLinks } from "@/app/actions";
import type { GeneratedLink } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";

export function NodeLinksDialog({
  ip,
  onClose,
}: {
  ip: string;
  onClose: () => void;
}) {
  const [links, setLinks] = useState<GeneratedLink[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let disposed = false;
    generateIPLinks(ip)
      .then((result) => {
        if (disposed) return;
        if (result.ok) setLinks(result.links);
        else setError(result.error);
      })
      .catch(() => {
        if (!disposed) setError("生成失败，请重试");
      })
      .finally(() => {
        if (!disposed) setLoading(false);
      });
    return () => {
      disposed = true;
    };
  }, [ip, attempt]);
  async function copy(value: string) {
    try {
      await navigator.clipboard.writeText(value);
      toast.success("节点链接已复制");
    } catch {
      toast.error("复制失败，请从文本框手动复制");
    }
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>生成节点链接</DialogTitle>
          <DialogDescription className="break-all">
            {ip} · 使用对应协议池内所有已启用节点的配置，保留认证、协议和 TLS
            参数，可复制到代理客户端导入。
          </DialogDescription>
        </DialogHeader>
        {loading ? (
          <p
            role="status"
            className="py-8 text-center text-sm text-muted-foreground"
          >
            正在生成…
          </p>
        ) : error ? (
          <div className="space-y-3">
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
            <Button
              variant="outline"
              onClick={() => {
                setError("");
                setLoading(true);
                setAttempt((v) => v + 1);
              }}
            >
              重试
            </Button>
          </div>
        ) : (
          <>
            <div className="flex items-center justify-between text-sm">
              <span>共 {links.length} 个节点</span>
              <Button
                onClick={() => copy(links.map((link) => link.uri).join("\n"))}
                disabled={!links.length}
              >
                复制全部链接
              </Button>
            </div>
            {links.map((link) => (
              <div
                key={link.node_id}
                className="min-w-0 space-y-2 rounded-lg border p-3"
              >
                <div className="flex items-center justify-between gap-3">
                  <label
                    htmlFor={`link-${link.node_id}`}
                    className="min-w-0 break-words text-sm font-medium"
                  >
                    {link.node_name} · {link.protocol}
                  </label>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => copy(link.uri)}
                  >
                    复制
                  </Button>
                </div>
                <Textarea
                  id={`link-${link.node_id}`}
                  readOnly
                  value={link.uri}
                  className="min-h-24 break-all font-mono text-xs"
                  onFocus={(e) => e.currentTarget.select()}
                />
              </div>
            ))}
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
