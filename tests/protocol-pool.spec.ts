import { test, expect } from "@playwright/test";

test("协议池空网段隔离与恢复", async ({ page, request }) => {
  const apiURL = process.env.API_URL || "http://127.0.0.1:8081";
  const headers = { Origin: process.env.APP_ORIGIN || "http://localhost:3000" };
  const name = `协议池-${Date.now()}`;
  const ids: string[] = [];
  let networkID = "";
  const login = await request.post(`${apiURL}/auth/login`, {
    headers,
    data: {
      username: process.env.ADMIN_USERNAME || "admin",
      password: process.env.ADMIN_PASSWORD,
    },
  });
  expect(login.ok()).toBeTruthy();
  try {
    const imported = await request.post(`${apiURL}/nodes/import`, {
      headers,
      data: {
        links: [
          `vless://11111111-1111-4111-8111-111111111111@sample.invalid:1?security=none&type=httpupgrade#${name}-HTTP`,
          `vless://11111111-1111-4111-8111-111111111111@sample.invalid:1?security=none&type=grpc#${name}-gRPC`,
        ].join("\n"),
      },
    });
    expect(imported.ok()).toBeTruthy();
    const nodes = await (await request.get(`${apiURL}/nodes`)).json();
    ids.push(
      ...nodes
        .filter((node: { name: string }) => node.name.startsWith(name))
        .map((node: { id: string }) => node.id),
    );
    const response = await request.post(`${apiURL}/networks`, {
      headers,
      data: {
        name,
        cidr: "198.18.93.0/30",
        enabled: true,
        description: "protocol pool browser test",
      },
    });
    expect(response.ok()).toBeTruthy();
    networkID = (await response.json()).id;
    await page.goto("/login");
    await page.getByLabel("用户名").fill(process.env.ADMIN_USERNAME || "admin");
    await page
      .getByLabel("密码", { exact: true })
      .fill(process.env.ADMIN_PASSWORD!);
    await page.getByRole("button", { name: "登录控制台" }).click();
    await expect(page).toHaveURL(/\/nodes$/);
    await page.goto("/probes");
    const dialog = page.getByRole("dialog");
    const openForm = async () => {
      await page.getByRole("button", { name: "新建检测" }).click();
      await expect(dialog).toBeVisible();
    };
    const selectNode = async (suffix: string) => {
      await dialog.getByLabel("测试节点", { exact: true }).click();
      await page
        .getByRole("option", { name: new RegExp(`${name}-${suffix}`) })
        .click();
    };
    await openForm();
    await selectNode("HTTP");
    await dialog.getByRole("button", { name: "选择检测网段" }).click();
    const clearNetworks = page.getByRole("button", { name: "清空", exact: true });
    if (await clearNetworks.isEnabled()) await clearNetworks.click();
    await page.getByRole("combobox", { name: "搜索网段" }).fill(name);
    await page.getByRole("option", { name: new RegExp(name) }).click();
    await page.getByRole("button", { name: "完成", exact: true }).click();
    await dialog.getByLabel("单 IP 超时（毫秒）").fill("1000");
    await dialog.getByLabel("巡检方式", { exact: true }).click();
    await page.getByRole("option", { name: "单次检测", exact: true }).click();
    await dialog.getByRole("button", { name: "开始测试", exact: true }).click();
    await expect(dialog).not.toBeVisible();
    // 全段失败后协议池会禁用该网段，记录先结束再重新打开配置查看。
    await expect(
      page.getByRole("row").filter({ hasText: name }).first(),
    ).toContainText("已完成", { timeout: 20000 });
    await page.reload();
    await openForm();
    const disabledRow = () =>
      page.getByText(`${name} · 198.18.93.0/30`, { exact: true }).locator("..");
    await expect(
      disabledRow().getByRole("button", { name: "恢复扫描", exact: true }),
    ).toBeVisible({ timeout: 15000 });
    const networks = await (await request.get(`${apiURL}/networks`)).json();
    const network = networks.find(
      (row: { id: string }) => row.id === networkID,
    );
    expect(network.enabled).toBe(true);
    expect(network.disabled_protocols).toContain("vless/httpupgrade");
    expect(network.disabled_protocols).not.toContain("vless/grpc");
    await page.goto("/networks");
    await page.getByLabel("搜索网段").fill(name);
    await page.getByLabel("按状态筛选").click();
    await page.getByRole("option", { name: "协议池已禁用", exact: true }).click();
    const row = page.getByRole("row").filter({ hasText: name });
    await expect(row.getByText("VLESS · HTTPUpgrade", { exact: true })).toBeVisible();
    await expect(row.getByText("已启用", { exact: true })).toBeVisible();
    await page.reload();
    await page.getByLabel("搜索网段").fill(name);
    const badge = row.getByText("VLESS · HTTPUpgrade", { exact: true });
    await expect(badge).toHaveCSS("background-color", "rgb(153, 27, 27)");
    await expect(badge).toHaveCSS("color", "rgb(255, 255, 255)");
    await page.goto("/probes");
    await openForm();
    await selectNode("gRPC");
    await expect(
      disabledRow().getByRole("button", { name: "恢复扫描", exact: true }),
    ).toHaveCount(0);
    await dialog.getByRole("button", { name: "选择检测网段" }).click();
    await page.getByRole("combobox", { name: "搜索网段" }).fill(name);
    await expect(
      page.getByRole("option", { name: new RegExp(name) }),
    ).toBeVisible();
    await page.getByRole("button", { name: "完成", exact: true }).click();
    await selectNode("HTTP");
    await disabledRow()
      .getByRole("button", { name: "恢复扫描", exact: true })
      .click();
    await expect(
      disabledRow().getByRole("button", { name: "恢复扫描", exact: true }),
    ).toHaveCount(0);
  } finally {
    const latest = await request.get(
      `${apiURL}/scans/latest?protocol=vless%2Fhttpupgrade`,
    );
    if (latest.ok()) {
      const job = await latest.json();
      if (ids.includes(job?.input?.node_id))
        await request.post(`${apiURL}/scans/${job.id}/cancel`, { headers });
    }
    if (networkID)
      await request.delete(`${apiURL}/networks/${networkID}`, { headers });
    for (const id of ids)
      await request.delete(`${apiURL}/nodes/${id}`, { headers });
  }
});
