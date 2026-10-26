"use client";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useTransition } from "react";
import {
  Cloud,
  ShieldCheck,
  Server,
  Activity,
  ListChecks,
  Network,
  LogOut,
  ChevronRight,
  Loader2,
  Ban,
} from "lucide-react";
import { toast } from "sonner";
import { logout } from "@/app/actions";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

const navigation = [
  { href: "/cf", label: "CF 优选", icon: Cloud },
  { href: "/probes", label: "连通性测试", icon: Activity },
  { href: "/available-ips", label: "可用 IP", icon: ListChecks },
  { href: "/nodes", label: "节点管理", icon: Server },
  { href: "/networks", label: "网段配置", icon: Network },
  { href: "/blacklist", label: "IP 黑名单", icon: Ban },
];
export function ConsoleShell({
  username,
  children,
}: {
  username: string;
  children: React.ReactNode;
}) {
  const pathname = usePathname();
  const [pending, startTransition] = useTransition();
  const current = navigation.find(
    (item) => item.href === pathname || pathname.startsWith(item.href + "/"),
  );
  return (
    <div className="min-h-dvh">
      <aside className="fixed inset-y-0 left-0 z-20 hidden w-60 flex-col border-r bg-white md:flex">
        <Link href="/nodes" className="flex h-20 items-center gap-3 px-7">
          <span className="flex size-9 items-center justify-center rounded-xl bg-primary text-white">
            <ShieldCheck className="size-5" />
          </span>
          <span className="text-xl font-semibold tracking-tight">
            IP Guard
            <span className="mt-0.5 block text-[9px] font-medium tracking-[.22em] text-muted-foreground">
              ACCESS CONTROL
            </span>
          </span>
        </Link>
        <div className="px-5 pt-9">
          <p className="mb-4 px-3 text-[11px] font-medium tracking-widest text-slate-400">
            工作空间
          </p>
          <nav aria-label="主导航" className="space-y-2">
            {navigation.map(({ href, label, icon: Icon }) => (
              <Link
                key={href}
                href={href}
                aria-current={
                  pathname === href || pathname.startsWith(href + "/")
                    ? "page"
                    : undefined
                }
                className={cn(
                  "flex h-11 items-center gap-3 rounded-lg px-3 text-sm font-medium transition-colors hover:bg-muted",
                  pathname === href || pathname.startsWith(href + "/")
                    ? "bg-accent text-primary hover:bg-accent"
                    : "text-slate-600",
                )}
              >
                <Icon className="size-[18px]" />
                {label}
                {(pathname === href || pathname.startsWith(href + "/")) && (
                  <span className="ml-auto size-1.5 rounded-full bg-primary" />
                )}
              </Link>
            ))}
          </nav>
        </div>
        <div className="mx-5 mt-auto mb-6 rounded-xl border bg-slate-50 p-4">
          <div className="mb-2 flex items-center gap-2 text-xs font-medium">
            <ShieldCheck className="size-4 text-slate-500" />
            访问边界，从配置开始
          </div>
          <p className="text-[11px] leading-5 text-muted-foreground">
            集中维护节点和网段，
            <br />
            让每一条规则清晰可查。
          </p>
        </div>
      </aside>
      <div className="md:ml-60">
        <header className="flex h-20 items-center justify-between gap-3 border-b bg-white px-5 lg:px-10">
          <div className="flex items-center gap-3 text-sm">
            <ShieldCheck className="size-6 text-primary md:hidden" />
            <span className="hidden text-muted-foreground sm:inline">
              控制台
            </span>
            <ChevronRight className="hidden size-3.5 text-slate-400 sm:inline" />
            <span className="font-medium">{current?.label || "控制台"}</span>
          </div>
          <div className="flex items-center gap-3">
            <span className="flex size-8 items-center justify-center rounded-full bg-blue-50 text-xs font-semibold text-primary">
              {username.slice(0, 2).toUpperCase()}
            </span>
            <div className="hidden text-xs sm:block">
              <p className="font-medium">{username}</p>
              <p className="mt-1 text-[10px] text-muted-foreground">管理员</p>
            </div>
            <span className="mx-1 h-5 border-l" />
            <Button
              aria-label="退出登录"
              variant="ghost"
              size="icon"
              className="text-muted-foreground"
              disabled={pending}
              onClick={() =>
                startTransition(async () => {
                  try {
                    const result = await logout();
                    if (!result.ok) toast.error(result.error);
                  } catch {
                    toast.error("退出失败，请重试");
                  }
                })
              }
            >
              {pending ? <Loader2 className="animate-spin" /> : <LogOut />}
            </Button>
          </div>
        </header>
        <nav
          aria-label="移动端导航"
          className="flex flex-wrap gap-2 border-b bg-white px-5 py-3 md:hidden"
        >
          {navigation.map(({ href, label, icon: Icon }) => (
            <Link
              key={href}
              href={href}
              aria-current={
                pathname === href || pathname.startsWith(href + "/")
                  ? "page"
                  : undefined
              }
              className={cn(
                "flex items-center gap-2 rounded-md px-4 py-2 text-sm",
                pathname === href || pathname.startsWith(href + "/")
                  ? "bg-accent text-primary"
                  : "text-muted-foreground",
              )}
            >
              <Icon className="size-4" />
              {label}
            </Link>
          ))}
        </nav>
        <main className="mx-auto max-w-[1600px] px-5 py-8 lg:px-10 lg:py-10">
          {children}
        </main>
        <footer className="mx-5 mt-10 flex flex-wrap justify-between gap-2 border-t py-6 text-[11px] text-muted-foreground lg:mx-10">
          <span>IP Guard · IP 过滤管理系统</span>
          <span>节点与网段配置控制台</span>
        </footer>
      </div>
    </div>
  );
}
