"use client";

import { useState, useTransition } from "react";
import { AlertCircle, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { saveNode, saveNetwork } from "@/app/actions";
import type { Node, Network } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

export function ConfigForm({
  kind,
  nodes,
  item,
  onClose,
}: {
  kind: "nodes" | "networks";
  nodes: Node[];
  item: Node | Network | null;
  onClose: () => void;
}) {
  const isNode = kind === "nodes";
  const label = isNode ? "节点" : "网段";
  const initial = {
    name: item?.name || "",
    address: item && "address" in item ? item.address : "",
    port: item && "port" in item ? String(item.port) : "8080",
    cidr: item && "cidr" in item ? item.cidr : "",
    node_id: item && "node_id" in item ? item.node_id : "",
    action: item && "action" in item ? item.action : "deny",
    enabled: item?.enabled ?? true,
    description: item?.description || "",
  };
  const [values, setValues] = useState(initial);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [pending, startTransition] = useTransition();
  const set = (key: keyof typeof values, value: string | boolean) => {
    setValues((previous) => ({ ...previous, [key]: value }));
    setFields((previous) => ({ ...previous, [key]: "" }));
  };
  function close() {
    if (pending) return;
    if (
      JSON.stringify(values) !== JSON.stringify(initial) &&
      !window.confirm("配置尚未保存，确定放弃修改吗？")
    )
      return;
    onClose();
  }
  const field = (
    key: "name" | "address" | "port" | "cidr",
    title: string,
    placeholder: string,
    help?: string,
  ) => (
    <div className="space-y-2">
      <Label htmlFor={key}>
        {title}
        <span className="text-destructive">*</span>
      </Label>
      <Input
        id={key}
        value={values[key]}
        onChange={(event) => set(key, event.target.value)}
        onBlur={(event) => {
          if (!event.target.validity.valid)
            setFields((previous) => ({
              ...previous,
              [key]: event.target.validationMessage,
            }));
        }}
        placeholder={placeholder}
        required
        disabled={pending}
        type={key === "port" ? "number" : "text"}
        min={key === "port" ? 1 : undefined}
        max={key === "port" ? 65535 : undefined}
        maxLength={key === "name" ? 80 : 253}
        aria-invalid={Boolean(fields[key])}
        aria-describedby={
          fields[key] ? `${key}-error` : help ? `${key}-help` : undefined
        }
        className="h-10 bg-white"
      />
      {help && !fields[key] && (
        <p
          id={`${key}-help`}
          className="text-[11px] leading-5 text-muted-foreground"
        >
          {help}
        </p>
      )}
      {fields[key] && (
        <p
          id={`${key}-error`}
          className="flex items-start gap-1 text-xs text-destructive"
        >
          <AlertCircle className="mt-0.5 size-3 shrink-0" />
          {fields[key]}
        </p>
      )}
    </div>
  );
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) close();
      }}
    >
      <DialogContent
        className="max-h-[90dvh] overflow-y-auto sm:max-w-[520px]"
        onInteractOutside={(event) => event.preventDefault()}
      >
        <DialogHeader>
          <DialogTitle>
            {item ? "编辑" : "新增"}
            {label}
          </DialogTitle>
          <DialogDescription>
            {isNode
              ? "填写节点的基础信息，用于关联和管理网段配置。"
              : "指定 IP 网段及所属节点，选择对应的访问策略。"}
          </DialogDescription>
        </DialogHeader>
        <form
          className="space-y-5 pt-3"
          onSubmit={(event) => {
            event.preventDefault();
            setError("");
            setFields({});
            const data = isNode
              ? {
                  name: values.name,
                  address: values.address,
                  port: Number(values.port),
                  enabled: values.enabled,
                  description: values.description,
                }
              : {
                  name: values.name,
                  cidr: values.cidr,
                  node_id: values.node_id,
                  action: values.action,
                  enabled: values.enabled,
                  description: values.description,
                };
            if (!isNode && !values.node_id) {
              setFields({ node_id: "请选择所属节点" });
              return;
            }
            startTransition(async () => {
              try {
                const result = await (isNode ? saveNode : saveNetwork)(
                  item?.id || null,
                  data,
                );
                if (result.ok) {
                  toast.success(`${label}已${item ? "更新" : "创建"}`);
                  onClose();
                } else {
                  setError(result.error);
                  setFields(result.fields || {});
                }
              } catch {
                setError("保存失败，请检查网络后重试，输入内容已保留");
              }
            });
          }}
        >
          {field(
            "name",
            `${label}名称`,
            isNode ? "例如：上海主节点" : "例如：办公网络",
          )}
          {isNode ? (
            <div className="grid grid-cols-[1fr_110px] items-start gap-4">
              {field(
                "address",
                "节点地址",
                "192.168.1.10",
                "支持 IPv4、IPv6 或域名",
              )}
              {field("port", "端口", "8080")}
            </div>
          ) : (
            <>
              {field(
                "cidr",
                "CIDR 网段",
                "192.168.1.0/24",
                "支持 IPv4 / IPv6，请填写网络地址，主机位需为 0。",
              )}
              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-2">
                  <Label htmlFor="node_id">
                    所属节点<span className="text-destructive">*</span>
                  </Label>
                  <Select
                    value={values.node_id}
                    onValueChange={(value) => set("node_id", value)}
                    disabled={pending}
                  >
                    <SelectTrigger
                      id="node_id"
                      aria-invalid={Boolean(fields.node_id)}
                      aria-describedby={
                        fields.node_id ? "node_id-error" : undefined
                      }
                      className="h-10 w-full"
                    >
                      <SelectValue placeholder="请选择节点" />
                    </SelectTrigger>
                    <SelectContent>
                      {nodes.map((node) => (
                        <SelectItem key={node.id} value={node.id}>
                          {node.name}
                          {!node.enabled ? "（已停用）" : ""}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  {fields.node_id && (
                    <p id="node_id-error" className="text-xs text-destructive">
                      {fields.node_id}
                    </p>
                  )}
                </div>
                <div className="space-y-2">
                  <Label htmlFor="action">
                    访问策略<span className="text-destructive">*</span>
                  </Label>
                  <Select
                    value={values.action}
                    onValueChange={(value) => set("action", value)}
                    disabled={pending}
                  >
                    <SelectTrigger id="action" className="h-10 w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="deny">阻止</SelectItem>
                      <SelectItem value="allow">允许</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              </div>
            </>
          )}
          <div className="space-y-2">
            <Label htmlFor="description">
              备注
              <span className="text-xs font-normal text-muted-foreground">
                选填
              </span>
            </Label>
            <Textarea
              id="description"
              value={values.description}
              maxLength={500}
              disabled={pending}
              placeholder="添加用途或其他说明…"
              onChange={(event) => set("description", event.target.value)}
              className="min-h-20 resize-none"
            />
            <p className="text-right text-[10px] text-muted-foreground">
              {values.description.length} / 500
            </p>
            {fields.description && (
              <p className="text-xs text-destructive">{fields.description}</p>
            )}
          </div>
          <div className="flex items-center justify-between rounded-lg border bg-slate-50 p-4">
            <div>
              <Label htmlFor="enabled">启用{label}</Label>
              <p className="mt-1.5 text-[11px] text-muted-foreground">
                {values.enabled
                  ? "配置将标记为已启用"
                  : "保留配置，标记为已停用"}
              </p>
            </div>
            <Switch
              id="enabled"
              checked={values.enabled}
              disabled={pending}
              onCheckedChange={(checked) => set("enabled", checked)}
            />
          </div>
          {error && (
            <p
              role="alert"
              className="flex items-start gap-2 rounded-md bg-red-50 p-3 text-sm leading-6 text-red-700"
            >
              <AlertCircle className="mt-1 size-4 shrink-0" />
              {error}
            </p>
          )}
          <DialogFooter className="border-t pt-5">
            <Button
              type="button"
              variant="outline"
              onClick={close}
              disabled={pending}
            >
              取消
            </Button>
            <Button type="submit" disabled={pending}>
              {pending ? (
                <>
                  <Loader2 className="animate-spin" />
                  正在保存…
                </>
              ) : (
                "保存配置"
              )}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
