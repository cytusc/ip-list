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

export function ConfigForm({
  kind,
  item,
  onClose,
}: {
  kind: "nodes" | "networks";
  item: Node | Network | null;
  onClose: () => void;
}) {
  const isNode = kind === "nodes";
  const label = isNode ? "节点" : "网段";
  const initial = {
    share_uri: item && "share_uri" in item ? item.share_uri : "",
    name: item?.name || "",
    address: item && "address" in item ? item.address : "",
    port: item && "port" in item ? String(item.port) : "8080",
    asn: item && "asn" in item ? item.asn : "",
    provider: item && "provider" in item ? item.provider : "",
    cidr: item && "cidr" in item ? item.cidr : "",
    enabled: item?.enabled ?? true,
    description: item?.description || "",
  };
  const [values, setValues] = useState(initial);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [pending, startTransition] = useTransition();
  const set = (key: keyof typeof values, value: string | boolean) => {
    setValues((previous) => ({
      ...previous,
      ...(key === "cidr" ? { asn: "", provider: "" } : {}),
      [key]: value,
    }));
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
        required={!isNode || !values.share_uri || key === "name"}
        disabled={
          pending ||
          (isNode &&
            Boolean(values.share_uri) &&
            (key === "address" || key === "port"))
        }
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
              ? "填写节点基础信息和用于连通性测试的分享链接。"
              : "维护独立 IP 地址池，可用于代理节点和 CF 网站检测。"}
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
                  share_uri: values.share_uri,
                  name: values.name,
                  address: values.address,
                  port: Number(values.port),
                  enabled: values.enabled,
                  description: values.description,
                }
              : {
                  name: values.name,
                  cidr: values.cidr,
                  asn: values.asn,
                  provider: values.provider,
                  enabled: values.enabled,
                  description: values.description,
                };
            startTransition(async () => {
              try {
                const result = await (isNode ? saveNode : saveNetwork)(
                  item?.id || null,
                  data,
                );
                if (result.ok) {
                  if (result.warning) toast.warning(result.warning);
                  else {
                    toast.success(`${label}已${item ? "更新" : "创建"}`, {
                      description: !isNode && data.enabled
                        ? "新增、修改 CIDR 或重新启用网段后会自动增量检测；已有任务时排队处理。"
                        : undefined,
                    });
                  }
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
          {isNode && (
            <div className="space-y-2">
              <Label htmlFor="share_uri">VLESS 链接（选填）</Label>
              <Textarea
                id="share_uri"
                value={values.share_uri}
                disabled={pending}
                maxLength={8192}
                onChange={(event) => set("share_uri", event.target.value)}
                className="break-all font-mono text-xs"
              />
              <p className="text-xs text-muted-foreground">
                用于批量连通性测试；保存时自动从链接提取地址和端口。
              </p>
              {fields.share_uri && (
                <p role="alert" className="text-xs text-destructive">
                  {fields.share_uri}
                </p>
              )}
            </div>
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
              <p className="text-xs text-muted-foreground">
                新增、修改 CIDR 或重新启用网段后，会使用已配置的节点和 CF 域名自动检测未测 IP。已有任务时排队处理，无需手动开始。
              </p>
              <p className="text-xs text-muted-foreground">
                根据网段自动查询 ASN 和所属机构；修改 CIDR 后会重新查询，
                缺失数据由后台自动补全。以下信息可按需手动修正。
              </p>
              <div className="grid gap-4 sm:grid-cols-2">
                {(
                  [
                    ["asn", "ASN", "留空自动获取"],
                    ["provider", "服务商", "留空自动获取"],
                  ] as const
                ).map(([key, title, placeholder]) => (
                  <div key={key} className="space-y-2">
                    <Label htmlFor={key}>{title}</Label>
                    <Input
                      id={key}
                      value={values[key]}
                      placeholder={placeholder}
                      maxLength={key === "asn" ? 255 : 100}
                      disabled={pending}
                      onChange={(e) => set(key, e.target.value)}
                      aria-invalid={Boolean(fields[key])}
                      aria-describedby={
                        fields[key] ? `${key}-error` : undefined
                      }
                    />
                    {fields[key] && (
                      <p
                        id={`${key}-error`}
                        className="text-xs text-destructive"
                      >
                        {fields[key]}
                      </p>
                    )}
                  </div>
                ))}
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
