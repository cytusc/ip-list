import { test, expect } from "@playwright/test";
import { createServer, type Socket } from "node:net";

test("导入节点、批量测试、刷新恢复和停止", async ({
  page,
  request,
}, testInfo) => {
  const sockets = new Set<Socket>();
  const server = createServer((socket) => {
    sockets.add(socket);
    socket.on("close", () => sockets.delete(socket));
    socket.resume();
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  if (!address || typeof address === "string")
    throw new Error("test listener unavailable");
  const name = `连通性-${Date.now()}`;
  let nodeID = "",
    networkID = "";
  const apiURL = process.env.API_URL || "http://127.0.0.1:8081";
  const headers = { Origin: process.env.APP_ORIGIN || "http://localhost:3000" };
  try {
    await page.goto("/login");
    await page.getByLabel("用户名").fill(process.env.ADMIN_USERNAME || "admin");
    await page
      .getByLabel("密码", { exact: true })
      .fill(process.env.ADMIN_PASSWORD!);
    await page.getByRole("button", { name: "登录控制台" }).click();
    await expect(page).toHaveURL(/\/nodes$/);
    await page.getByRole("button", { name: "导入 VLESS 节点" }).click();
    const dialog = page.getByRole("dialog");
    await dialog
      .getByLabel("节点链接")
      .fill(
        `vless://11111111-1111-4111-8111-111111111111@original.invalid:${address.port}?security=none&type=tcp#${name}`,
      );
    await dialog.getByRole("button", { name: "导入节点", exact: true }).click();
    await expect(dialog).not.toBeVisible();
    await expect(page.getByRole("table")).toContainText(name);
    // Backend setup and teardown use a separate authenticated session.
    expect(
      (
        await request.post(`${apiURL}/auth/login`, {
          headers,
          data: {
            username: process.env.ADMIN_USERNAME || "admin",
            password: process.env.ADMIN_PASSWORD,
          },
        })
      ).ok(),
    ).toBeTruthy();
    const nodes = await (await request.get(`${apiURL}/nodes`)).json();
    nodeID = nodes.find((node: { name: string }) => node.name === name).id;
    const netResponse = await request.post(`${apiURL}/networks`, {
      headers,
      data: {
        name,
        cidr: "127.0.0.0/26",
        enabled: true,
        description: "browser test",
      },
    });
    expect(netResponse.ok()).toBeTruthy();
    networkID = (await netResponse.json()).id;
    await page.goto("/probes");
    await page.getByLabel("测试节点", { exact: true }).click();
    await page.getByRole("option", { name, exact: true }).click();
    await page.getByRole("checkbox", { name: new RegExp(name) }).check();
    await page.getByLabel("巡检方式", { exact: true }).click();
    await page.getByRole("option", { name: "单次检测", exact: true }).click();
    await page.getByLabel("检测网址").fill("http://probe.example/generate_204");
    await page.getByLabel("单 IP 超时（毫秒）").fill("15000");
    await page.getByLabel("并发数", { exact: true }).fill("1");
    await page.getByRole("button", { name: "开始测试", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "停止测试", exact: true }),
    ).toBeVisible();
    await page.reload();
    await expect(
      page.getByRole("button", { name: "停止测试", exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "停止测试", exact: true }).click();
    await expect(page.getByText("已停止", { exact: true })).toBeVisible({
      timeout: 15000,
    });
    await expect(page.getByLabel("检测网址")).toHaveValue(
      "http://probe.example/generate_204",
    );
    await expect(page.getByLabel("并发数", { exact: true })).toHaveValue("1");
    await page.getByLabel("巡检方式", { exact: true }).click();
    await page
      .getByRole("option", { name: "自动巡检 · 间隔 5 分钟", exact: true })
      .click();
    await page.getByRole("button", { name: "开始巡检", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "停止巡检", exact: true }),
    ).toBeVisible();
    await page.reload();
    await expect(page.getByLabel("巡检方式", { exact: true })).toContainText(
      "间隔 5 分钟",
    );
    await page.getByRole("button", { name: "停止巡检", exact: true }).click();
    await expect(page.getByText("已停止", { exact: true })).toBeVisible({
      timeout: 15000,
    });
    await page.screenshot({
      path: testInfo.outputPath("probes-desktop.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: testInfo.outputPath("probes-mobile.png"),
      fullPage: true,
    });
  } finally {
    const latest = await request.get(`${apiURL}/scans/latest`);
    if (latest.ok()) {
      const job = await latest.json();
      if (job?.status === "running" || job?.status === "waiting")
        await request.post(`${apiURL}/scans/${job.id}/cancel`, { headers });
    }
    if (networkID)
      await request.delete(`${apiURL}/networks/${networkID}`, { headers });
    if (nodeID) await request.delete(`${apiURL}/nodes/${nodeID}`, { headers });
    for (const socket of sockets) socket.destroy();
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});
