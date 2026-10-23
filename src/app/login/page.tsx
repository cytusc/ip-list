import { redirect } from "next/navigation";
import { ShieldCheck, Network, Server, ArrowUpRight } from "lucide-react";
import { getUser } from "@/lib/auth";
import { LoginForm } from "@/components/login-form";

export const dynamic = "force-dynamic";
export default async function LoginPage() {
  if (await getUser()) redirect("/nodes");
  return (
    <main className="grid min-h-dvh lg:grid-cols-2">
      <section className="relative hidden overflow-hidden bg-[#101e38] p-14 text-white lg:flex lg:flex-col lg:justify-between">
        <div className="flex items-center gap-3 text-xl font-semibold">
          <span className="rounded-xl bg-blue-500 p-2.5">
            <ShieldCheck className="size-6" />
          </span>
          IP Guard
        </div>
        <div className="relative z-10 max-w-lg py-16">
          <div className="mb-6 inline-flex items-center gap-2 rounded-full border border-white/15 px-3 py-1.5 text-xs text-blue-200">
            <span className="size-1.5 rounded-full bg-blue-400" />
            IP 访问配置管理
          </div>
          <h1 className="text-4xl leading-snug font-semibold tracking-tight xl:text-5xl">
            清晰管理节点，
            <br />
            从容配置访问边界。
          </h1>
          <p className="mt-6 max-w-sm text-base leading-8 text-slate-400">
            让分散的节点与网段配置井然有序。
            <br />
            一个控制台，专注每一条访问规则。
          </p>
          <div className="mt-12 flex gap-8 text-sm text-slate-300">
            <span className="flex items-center gap-2">
              <Server className="size-4 text-blue-400" />
              节点管理
            </span>
            <span className="flex items-center gap-2">
              <Network className="size-4 text-blue-400" />
              网段配置
            </span>
          </div>
        </div>
        <div className="text-xs tracking-widest text-slate-500">
          IP GUARD / CONFIGURATION CONSOLE
        </div>
        <div
          aria-hidden
          className="absolute -right-64 -bottom-64 size-[650px] rounded-full border border-blue-400/10 shadow-[0_0_0_80px_#60a5fa05,0_0_0_160px_#60a5fa04]"
        />
      </section>
      <section className="flex flex-col items-center justify-center bg-white px-6 py-14">
        <div className="w-full max-w-[370px]">
          <div className="mb-10 flex items-center gap-2 font-semibold lg:hidden">
            <ShieldCheck className="size-6 text-primary" />
            IP Guard
          </div>
          <span className="mb-6 flex size-12 items-center justify-center rounded-2xl border bg-background">
            <ArrowUpRight className="size-6 text-slate-600" />
          </span>
          <h2 className="text-2xl font-semibold tracking-tight">欢迎回来</h2>
          <p className="mt-3 mb-8 text-sm text-muted-foreground">
            登录控制台，管理你的节点与网段配置。
          </p>
          <LoginForm />
          <p className="mt-8 text-center text-xs leading-6 text-muted-foreground">
            仅限管理员访问 · 登录有效期为 24 小时
          </p>
        </div>
      </section>
    </main>
  );
}
