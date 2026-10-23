import type { Metadata } from "next";
import { Toaster } from "@/components/ui/sonner";
import "./globals.css";

export const metadata: Metadata = {
  title: { default: "IP Guard · IP 过滤管理", template: "%s · IP Guard" },
  description: "节点与网段配置管理平台",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="zh-CN">
      <body className="font-sans">
        {children}
        <Toaster theme="light" richColors position="top-center" closeButton />
      </body>
    </html>
  );
}
