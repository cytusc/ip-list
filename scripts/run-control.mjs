import { existsSync } from "node:fs";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const binary = path.join(
  root,
  "backend",
  "rust",
  "target",
  "release",
  process.platform === "win32" ? "control.exe" : "control",
);

if (!existsSync(binary)) {
  console.error("control 尚未构建，请先运行 npm run control:build");
  process.exit(1);
}

const child = spawn(binary, [], {
  cwd: root,
  stdio: "inherit",
  env: {
    ...process.env,
    CONTROL_ADDR: process.env.CONTROL_ADDR ?? "0.0.0.0:3000",
    WEB_DIR: process.env.WEB_DIR ?? path.join(root, "out"),
    WORKER_URL: process.env.WORKER_URL ?? "http://127.0.0.1:8082",
    APP_ORIGIN: process.env.APP_ORIGIN ?? "http://127.0.0.1:3000",
  },
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => child.kill(signal));
}

child.on("exit", (code) => process.exit(code ?? 0));
