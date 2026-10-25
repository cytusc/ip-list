import { test, expect } from "@playwright/test";

test("CF 统一检测、域名详情与移动端", async ({ page }) => {
  await page.goto("/login");
  await page.getByLabel("用户名").fill(process.env.ADMIN_USERNAME || "admin");
  await page
    .getByLabel("密码", { exact: true })
    .fill(process.env.ADMIN_PASSWORD!);
  await page.getByRole("button", { name: "登录控制台" }).click();
  await expect(page).toHaveURL(/\/nodes$/);

  await page.goto("/cf");
  const name = `CF 测试 ${Date.now()}`;
  await page.getByRole("button", { name: "添加域名" }).click();
  const addDialog = page.getByRole("dialog");
  await addDialog.getByLabel("名称", { exact: true }).fill(name);
  await addDialog
    .getByLabel("HTTPS 地址")
    .fill(`https://cf-${Date.now()}.invalid/`);
  await addDialog.getByRole("button", { name: "添加地址", exact: true }).click();
  await expect(addDialog).toBeHidden();

  // 列表补齐最后检测时间、添加时间与查看入口
  await expect(
    page.getByRole("columnheader", { name: "可用 IP" }),
  ).toBeVisible();
  await expect(
    page.getByRole("columnheader", { name: "最后检测" }),
  ).toBeVisible();
  await expect(
    page.getByRole("columnheader", { name: "添加时间" }),
  ).toBeVisible();
  const domain = page.getByRole("link", { name }).first();
  await expect(domain).toBeVisible();

  // 域名详情只展示域名数据，不出现检测进度
  await domain.click();
  await expect(page).toHaveURL(/\/cf\/[0-9a-f-]{36}$/);
  await expect(page.getByRole("heading", { name, exact: true })).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "该域名返回的可用 IP" }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "最近检测记录" }),
  ).toBeVisible();
  await expect(page.locator("progress")).toHaveCount(0);

  // 统一检测配置在列表页，启动后跳转到独立进度页
  await page.getByRole("link", { name: "← 返回 CF 优选" }).click();
  await page.getByRole("button", { name: "网段与检测配置" }).click();
  const config = page.getByRole("dialog");
  await expect(config.getByRole("combobox", { name: "检测域名" })).toBeVisible();
  await page.getByRole("button", { name: "选择检测网段" }).click();
  const clear = page.getByRole("button", { name: "清空", exact: true });
  if (await clear.isEnabled()) await clear.click();
  const option = page.getByRole("option").first();
  await option.click();
  await expect(option).toContainText("已勾选");
  await page.getByRole("button", { name: "完成", exact: true }).click();
  await page.getByLabel("超时（毫秒）").fill("1000");
  await page.getByRole("button", { name: "开始检测" }).click();
  await expect(page).toHaveURL(/\/cf\/progress$/, { timeout: 15000 });
  await expect(
    page.getByRole("heading", { name: "统一检测进度" }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: /共用 IP 池/ }),
  ).toBeVisible();
  await expect(page.locator("progress").first()).toBeVisible();

  // 进度页与详情页在移动端不产生横向溢出
  await page.setViewportSize({ width: 390, height: 844 });
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "统一检测进度" }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBeTruthy();
  await page.screenshot({
    path: "test-results/cf-progress-mobile.png",
    fullPage: true,
    animations: "disabled",
  });

  const stop = page.getByRole("button", { name: "停止检测", exact: true });
  if (await stop.isVisible()) await stop.click();

  await page.goto("/cf");
  // 移动端为卡片布局，可访问名称包含域名与各项指标
  await expect(page.getByRole("link", { name }).first()).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBeTruthy();
  await page.screenshot({
    path: "test-results/cf-mobile.png",
    fullPage: true,
    animations: "disabled",
  });

  // 清理：删除测试域名，共用 IP 池保留
  await page.getByRole("link", { name }).first().click();
  await page.getByRole("button", { name: "删除域名", exact: true }).click();
  await page.getByRole("button", { name: "确认删除", exact: true }).click();
  await expect(page).toHaveURL(/\/cf$/);
  await expect(page.getByRole("link", { name })).toHaveCount(0);
});
