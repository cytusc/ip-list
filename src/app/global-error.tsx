"use client";
export default function GlobalError() {
  return (
    <html lang="zh-CN">
      <body
        style={{ fontFamily: "sans-serif", padding: 60, textAlign: "center" }}
      >
        <h1>服务暂时不可用</h1>
        <p>请检查数据库和 Redis 连接后重新加载页面。</p>
        <button onClick={() => window.location.reload()}>重新加载</button>
      </body>
    </html>
  );
}
