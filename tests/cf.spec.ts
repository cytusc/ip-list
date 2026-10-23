import { test, expect } from "@playwright/test";

test("CF 地址详情、检测与移动端", async ({ page }) => {
  await page.goto("/login");
  await page.getByLabel("用户名").fill(process.env.ADMIN_USERNAME || "admin");
  await page
    .getByLabel("密码", { exact: true })
    .fill(process.env.ADMIN_PASSWORD!);
  await page.getByRole("button", { name: "登录控制台" }).click();
  await expect(page).toHaveURL(/\/nodes$/);
  await page.goto("/cf");
  const name = `CF 测试 ${Date.now()}`;
  await page.getByLabel("名称", { exact: true }).fill(name);
  await page.getByLabel("HTTPS 地址").fill(`https://cf-${Date.now()}.invalid/`);
  await page.getByRole("button", { name: "添加地址", exact: true }).click();
  await expect(page.getByRole("heading", { name, exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "开始检测" })).toBeDisabled();
  const box = page.getByRole("checkbox").first();
  await box.check();
  await page.getByLabel("超时（毫秒）").fill("1000");
  await page.getByRole("button", { name: "开始检测" }).click();
  await expect(
    page.getByRole("status").filter({ hasText: /检测中|检测完成/ }),
  ).toBeVisible();
  await page.reload();
  await expect(page.getByRole("checkbox").first()).toBeChecked();
  const stop = page.getByRole("button", { name: "停止检测", exact: true });
  if (await stop.isVisible()) await stop.click();
  await expect(page.getByRole("button", { name: "开始检测" })).toBeEnabled({
    timeout: 15000,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBeTruthy();
  await page.screenshot({ path: "test-results/cf-mobile.png", fullPage: true });
  await page.getByRole("button", { name: "删除地址", exact: true }).click();
  await page.getByRole("button", { name: "确认删除", exact: true }).click();
  await expect(page).toHaveURL(/\/cf$/);
  await expect(page.getByText(name, { exact: true })).toHaveCount(0);
});
