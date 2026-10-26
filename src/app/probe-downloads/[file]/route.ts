import { readFile } from "node:fs/promises";
import path from "node:path";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

// Public artifacts only. Credentials never appear in download URLs or files.
export async function GET(request: Request, { params }: { params: Promise<{ file: string }> }) {
  const { file } = await params;
  const script = file === "install.sh" || file === "install.ps1";
  if (!script && !/^latency-probe-(?:(?:linux|darwin)-(?:amd64|arm64)|windows-(?:amd64|arm64)\.exe)$/.test(file)) {
    return new Response("Not found", { status: 404 });
  }
  const compressed = !script && request.headers.get("accept-encoding")?.split(",").some(
    (part) => part.trim().split(";")[0].toLowerCase() === "gzip" && !/;\s*q=0(?:\.0*)?\s*$/.test(part),
  );
  try {
    const content = script
      ? await readFile(path.join(process.cwd(), "scripts/latency-probe", file))
      : await readFile(path.join(process.cwd(), "bin/latency-probe", compressed ? `${file}.gz` : file));
    return new Response(content, { headers: {
      "Content-Type": script ? "text/plain; charset=utf-8" : "application/octet-stream",
      "Content-Disposition": `attachment; filename="${file}"`,
      "Cache-Control": "no-store",
      "Content-Length": String(content.byteLength),
      "Vary": "Accept-Encoding",
      ...(compressed ? { "Content-Encoding": "gzip" } : {}),
      "X-Content-Type-Options": "nosniff",
    } });
  } catch {
    return new Response("安装文件暂未发布，请联系管理员构建探针程序", { status: 503 });
  }
}
