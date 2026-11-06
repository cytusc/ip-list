import { test, expect } from "@playwright/test";

test("可用 IP 列展示、排序、搜索筛选、刷新失败恢复与移动端展示", async ({
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
  await page.route("**/api/available-ips/locations?*", async (route) => {
    const ips = new URL(route.request().url()).searchParams.getAll("ip");
    await route.fulfill({ json: ips.map(ip => ({ ip, location: ip === "192.0.2.25"
      ? { country: "HK", region: "Hong Kong", city: "Hong Kong" }
      : { country: "", region: "", city: "" } })) });
  });
  const fixtures = [
    {
      ip: "192.0.2.25",
      asn: "AS13335",
      provider: "Cloudflare",
      probe_delays: [{ probe_id: "probe-1", name: "测试探针", online: true, fresh: true, window_end: checked_at,
        samples: Array.from({ length: 20 }, (_, id) => ({ id, ip: "192.0.2.25", checked_at, success: true, delay_ms: id === 19 ? 1000 : 42 })),
      }],
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
  await page.route("**/api/lists/available-ips?*", async (route) => {
    const query = new URL(route.request().url()).searchParams;
    let items = fixtures.filter(item => (!query.get("search") || item.ip.includes(query.get("search")!))
      && (!query.get("protocol") || query.get("protocol") === "all" || item.checks.some(check => check.protocol === query.get("protocol"))));
    if (query.get("direction") === "desc") items = items.toReversed();
    await route.fulfill({
      status: fail ? 503 : 200,
      json: fail ? { error: "测试服务暂时不可用" } : { items, total: items.length, page: 1, pageSize: 20, allTotal: fixtures.length },
    });
  });
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  const row = page.getByRole("row").filter({ hasText: "192.0.2.25" });
  await expect(row).toHaveCount(1);
  await expect(row).toContainText("42.00 ms");
  await expect(page.getByRole("columnheader", { name: "P95 平均延迟" })).toBeVisible();
  await expect(row).toContainText("AS13335");
  await expect(row).toContainText("Cloudflare");
  await expect(row.getByRole("img", { name: /香港.*国旗/ })).toBeVisible();
  await expect(row).toContainText("Hong Kong");
  await expect(page.getByRole("table")).toContainText("未知归属地");
  await expect(page.getByRole("columnheader", { name: "支持的协议" })).toHaveCount(0);
  for (const [name, sort] of [["探针延迟", "probe_delay"], ["最近测通", "checked_at"]]) {
    for (const direction of ["asc", "desc"]) {
      const request = page.waitForRequest(request => {
        const url = new URL(request.url());
        return url.pathname === "/api/lists/available-ips" && url.searchParams.get("sort") === sort
          && url.searchParams.get("direction") === direction && url.searchParams.get("page") === "1";
      });
      await page.getByRole("button", { name, exact: true }).click();
      await request;
      await expect(page.getByRole("columnheader", { name })).toHaveAttribute("aria-sort", direction === "asc" ? "ascending" : "descending");
      await expect(page.locator("tbody tr").first()).toContainText(direction === "asc" ? fixtures[0].ip : fixtures[1].ip);
    }
  }
  await page.getByLabel("协议筛选", { exact: true }).click();
  await page.getByRole("option", { name: "VLESS · gRPC", exact: true }).click();
  await expect(row).toHaveCount(0);
  await expect(page.getByRole("table")).toContainText(fixtures[1].ip);
  await page.getByLabel("搜索 IP、节点、ASN 或服务商").fill("不匹配");
  await expect(page.getByText("没有匹配的 IP", { exact: true })).toBeVisible();
  await page.getByLabel("搜索 IP、节点、ASN 或服务商").fill("");
  await page.getByLabel("协议筛选", { exact: true }).click();
  await page.getByRole("option", { name: "全部协议", exact: true }).click();
  await expect(row).toBeVisible();
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
