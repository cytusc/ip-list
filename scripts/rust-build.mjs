import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";

const rustDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "backend", "rust");
const args = process.argv.slice(2);

if (process.platform === "win32") {
  const result = spawnSync("cmd.exe", ["/c", "build.bat", ...args], {
    cwd: rustDir,
    stdio: "inherit",
  });
  process.exit(result.status ?? 1);
}

const result = spawnSync("cargo", args, { cwd: rustDir, stdio: "inherit" });
process.exit(result.status ?? 1);
