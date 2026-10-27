"use client";
import { useState, useTransition } from "react";
import { toast } from "sonner";
import { importNodes } from "@/app/actions";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog";

export function NodeImport() {
  const [open, setOpen] = useState(false);
  const [links, setLinks] = useState("");
  const [error, setError] = useState("");
  const [pending, startTransition] = useTransition();
  return (
    <>
      <Button variant="outline" onClick={() => setOpen(true)}>
        导入 VLESS / Trojan 节点
      </Button>
      <Dialog
        open={open}
        onOpenChange={(value) => {
          if (
            !pending &&
            (value || !links || window.confirm("链接尚未导入，确定关闭吗？"))
          )
            setOpen(value);
        }}
      >
        <DialogContent onInteractOutside={(event) => event.preventDefault()}>
          <DialogHeader>
            <DialogTitle>导入 VLESS / Trojan 节点</DialogTitle>
            <DialogDescription>
              每行一条，最多 20 条。使用链接中的名称，支持
              HTTPUpgrade、XHTTP、gRPC、WS 和 TCP。
            </DialogDescription>
          </DialogHeader>
          <form
            className="space-y-4"
            onSubmit={(event) => {
              event.preventDefault();
              setError("");
              startTransition(async () => {
                try {
                  const result = await importNodes(links);
                  if (result.ok) {
                    toast.success("节点已导入");
                    setLinks("");
                    setOpen(false);
                  } else setError(result.error);
                } catch {
                  setError("导入失败，请重试；链接已保留");
                }
              });
            }}
          >
            <Label htmlFor="import-links">节点链接</Label>
            <Textarea
              id="import-links"
              value={links}
              onChange={(event) => setLinks(event.target.value)}
              required
              disabled={pending}
              maxLength={15000}
              className="min-h-48 break-all font-mono text-xs"
              placeholder="vless://UUID@server:443?security=tls&type=grpc&serviceName=example#节点名称"
            />
            <p className="text-xs text-muted-foreground">
              同一服务器的不同传输方式可分别导入；节点名称需唯一。任一行出错时全部保留，不会部分导入。
            </p>
            {error && (
              <p role="alert" className="text-sm text-destructive">
                {error}
              </p>
            )}
            <DialogFooter>
              <Button type="submit" disabled={pending || !links.trim()}>
                {pending ? "正在导入…" : "导入节点"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
