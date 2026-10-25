import { test, expect } from "@playwright/test";

test("可用 IP 合并协议、搜索筛选、刷新失败恢复与移动端展示", async ({
  page,
}, testInfo) => {
  await page.goto("/login");
  await page.getByLabel("用户名").fill(process.env.ADMIN_USERNAME || "admin");
  await page
    .getByLabel("密码", { exact: true })
    .fill(process.env.ADMIN_PASSWORD!);
  await page.getByRole("button", { name: "登录控制台" }).click();
  await expect(page).toHaveURL(/\/nodes$/);
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("link", { name: "可用 IP", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "可用 IP", exact: true }),
  ).toBeVisible();
  const checked_at = new Date().toISOString();
  const fixtures = [
    {
      ip: "192.0.2.25",
      asn: "AS13335",
      provider: "Cloudflare",
      checks: [
        {
          protocol: "vless/httpupgrade",
          delay_ms: 42,
          node_id: "node-1",
          node_name: "香港 HTTPUpgrade",
          checked_at,
        },
        {
          protocol: "vless/xhttp",
          delay_ms: 68,
          node_id: "node-2",
          node_name: "香港 XHTTP",
          checked_at,
        },
        {
          protocol: "vless/xhttp",
          delay_ms: 80,
          node_id: "node-3",
          node_name: "备用 XHTTP",
          checked_at,
        },
      ],
    },
    {
      ip: "2001:db8:1234:5678:abcd:ef01:1234:5678",
      checks: [
        {
          protocol: "vless/grpc",
          delay_ms: 95,
          node_id: "node-4",
          node_name: "香港 gRPC",
          checked_at,
        },
      ],
    },
  ];
  let fail = false;
  await page.route("**/api/available-ips", async (route) => {
    await route.fulfill({
      status: fail ? 503 : 200,
      json: fail ? { error: "测试服务暂时不可用" } : fixtures,
    });
  });
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  const row = page.getByRole("row").filter({ hasText: "192.0.2.25" });
  await expect(row).toHaveCount(1);
  await expect(row).toContainText("HTTPUpgrade");
  await expect(row).toContainText("AS13335");
  await expect(row).toContainText("Cloudflare");
  await expect(row).toContainText("42 ms");
  await expect(row).toContainText("68 ms");
  await expect(
    row.getByText("同协议与传输方式的所有节点共用").first(),
  ).toBeVisible();
  await expect(row.locator('[data-slot="badge"]')).toHaveCount(2);
  await page.getByLabel("协议筛选", { exact: true }).click();
  await page.getByRole("option", { name: "VLESS · gRPC", exact: true }).click();
  await expect(row).toHaveCount(0);
  await expect(page.getByRole("table")).toContainText(fixtures[1].ip);
  await page.getByLabel("搜索 IP、节点、ASN 或服务商").fill("不匹配");
  await expect(page.getByText("没有匹配的 IP", { exact: true })).toBeVisible();
  await page.getByLabel("搜索 IP、节点、ASN 或服务商").fill("");
  await page.getByLabel("协议筛选", { exact: true }).click();
  await page.getByRole("option", { name: "全部协议", exact: true }).click();
  fail = true;
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  await expect(
    page.getByRole("alert").filter({ hasText: "测试服务暂时不可用" }),
  ).toContainText("测试服务暂时不可用");
  await expect(row).toBeVisible();
  fail = false;
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  await expect(
    page.getByRole("alert").filter({ hasText: "测试服务暂时不可用" }),
  ).toHaveCount(0);
  await page.screenshot({
    path: testInfo.outputPath("available-ips-desktop.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: testInfo.outputPath("available-ips-mobile.png"),
    fullPage: true,
  });
});
