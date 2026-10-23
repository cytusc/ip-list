"use client";
import { useState, useTransition } from "react";
import { ArrowRight, AlertCircle, Eye, EyeOff, Loader2 } from "lucide-react";
import { login } from "@/app/actions";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export function LoginForm() {
  const [pending, startTransition] = useTransition();
  const [error, setError] = useState("");
  const [visible, setVisible] = useState(false);
  return (
    <form
      className="space-y-5"
      onSubmit={(event) => {
        event.preventDefault();
        const data = new FormData(event.currentTarget);
        setError("");
        startTransition(async () => {
          try {
            const result = await login({
              username: data.get("username"),
              password: data.get("password"),
            });
            if (!result.ok) setError(result.error);
          } catch {
            setError("连接失败，请检查网络后重试");
          }
        });
      }}
    >
      <div className="space-y-2">
        <Label htmlFor="username">用户名</Label>
        <Input
          id="username"
          name="username"
          placeholder="请输入管理员用户名"
          autoComplete="username"
          maxLength={64}
          required
          disabled={pending}
          className="h-11 bg-white"
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor="password">密码</Label>
        <div className="relative">
          <Input
            id="password"
            name="password"
            type={visible ? "text" : "password"}
            placeholder="请输入密码"
            autoComplete="current-password"
            required
            maxLength={128}
            disabled={pending}
            className="h-11 bg-white pr-12"
          />
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="absolute top-0.5 right-1 size-10 text-muted-foreground"
            aria-label={visible ? "隐藏密码" : "显示密码"}
            onClick={() => setVisible(!visible)}
          >
            {visible ? <EyeOff /> : <Eye />}
          </Button>
        </div>
      </div>
      {error && (
        <div
          role="alert"
          className="flex items-start gap-2 rounded-lg bg-red-50 p-3 text-sm text-red-700"
        >
          <AlertCircle className="mt-0.5 size-4 shrink-0" />
          {error}
        </div>
      )}
      <Button type="submit" className="h-11 w-full" disabled={pending}>
        {pending ? (
          <>
            <Loader2 className="animate-spin" />
            正在登录…
          </>
        ) : (
          <>
            登录控制台
            <ArrowRight />
          </>
        )}
      </Button>
    </form>
  );
}
