"use client";

import { useTransition } from "react";
import { useRouter } from "next/navigation";
import { Loader2, RotateCw } from "lucide-react";
import { toast } from "sonner";
import { recheckNetwork } from "@/app/actions";
import { protocolLabels } from "@/lib/protocol";
import type { Network } from "@/lib/types";
import { Button } from "@/components/ui/button";

export function NetworkRecheck({ network }: { network: Network }) {
  const [pending, startTransition] = useTransition();
  const router = useRouter();
  return (
    <Button
      variant="ghost"
      size="sm"
      className="h-8 text-slate-500"
      disabled={pending || !network.enabled}
      aria-label={`重新检测${network.name}`}
      title={network.enabled ? "检测该网段所有已配置协议，包括协议池已禁用的网段" : "请先启用该网段"}
      onClick={() => {
        startTransition(async () => {
          try {
            const result = await recheckNetwork(network.id);
            if (!result.ok) {
              toast.error(result.error);
              return;
            }
            const started = result.items.filter((item) => item.job_id).length;
            const errors = result.items
              .filter((item) => item.error)
              .map((item) => `${protocolLabels[item.protocol] || item.protocol}：${item.error}`)
              .join("；");
            const options = {
              description: errors || "检测结果可在连通性测试中查看，成功后会恢复对应协议池的网段。",
              action: { label: "查看检测", onClick: () => router.push("/probes") },
              duration: errors ? 10000 : 5000,
            };
            if (started) toast.success(`已启动 ${started} 个协议的重新检测`, options);
            else toast.error("未启动重新检测", options);
          } catch {
            toast.error("启动请求失败，请稍后重试");
          }
        });
      }}
    >
      {pending ? <Loader2 className="size-3.5 animate-spin" /> : <RotateCw className="size-3.5" />}
      {pending ? "正在启动…" : "重新检测"}
    </Button>
  );
}
