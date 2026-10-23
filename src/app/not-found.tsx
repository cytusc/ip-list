import Link from "next/link";
export default function NotFound() {
  return (
    <main className="grid min-h-dvh place-content-center gap-4 text-center">
      <h1 className="text-2xl font-semibold">页面不存在</h1>
      <Link href="/nodes" className="text-primary underline">
        返回节点管理
      </Link>
    </main>
  );
}
