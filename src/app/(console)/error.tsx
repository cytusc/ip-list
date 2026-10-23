"use client";
import { AlertCircle } from "lucide-react";
import { Button } from "@/components/ui/button";
export default function ErrorPage({ reset }: { reset: () => void }) {
  return (
    <div className="rounded-xl border bg-white p-12 text-center">
      <AlertCircle className="mx-auto mb-4 size-9 text-destructive" />
      <h1 className="text-lg font-semibold">配置加载失败</h1>
      <p className="mt-3 mb-6 text-sm text-muted-foreground">
        请重试；若持续失败，请检查 PostgreSQL 和 Redis 是否正常运行。
      </p>
      <Button onClick={reset}>重新加载</Button>
    </div>
  );
}
