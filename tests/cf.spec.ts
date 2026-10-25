import { test, expect } from "@playwright/test";

test("CF 统一配置、共用检测与移动端", async ({ page }) => {
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
  const domain = page
    .getByRole("link")
    .filter({ has: page.getByRole("heading", { name, exact: true }) });
  await expect(domain).toBeVisible();
  await domain.click();
  await expect(page.getByRole("heading", { name, exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "选择检测网段" })).toHaveCount(
    0,
  );
  await page
    .getByRole("link", { name: "← 返回 CF 优选 · 统一检测配置" })
    .click();
  await page.getByRole("button", { name: "网段与检测配置" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("combobox", { name: "检测域名" }).click();
  await page.getByRole("option").filter({ hasText: name }).click();
  // A new domain may restore selections from the shared pool's latest job.
  await page.getByRole("button", { name: "选择检测网段" }).click();
  const clear = page.getByRole("button", { name: "清空", exact: true });
  if (await clear.isEnabled()) await clear.click();
  await page.getByRole("button", { name: "完成", exact: true }).click();
  await expect(page.getByRole("button", { name: "开始检测" })).toBeDisabled();
  await page.getByRole("button", { name: "选择检测网段" }).click();
  const box = page.getByRole("option").first();
  await box.click();
  await expect(box).toContainText("已勾选");
  await page.getByRole("button", { name: "完成", exact: true }).click();
  await page.getByRole("button", { name: "Close", exact: true }).click();
  await page.getByRole("button", { name: "网段与检测配置" }).click();
  await expect(
    page.getByRole("button", { name: "选择检测网段" }),
  ).toContainText("已选 1 个网段");
  await page.getByLabel("超时（毫秒）").fill("1000");
  await page.getByRole("button", { name: "开始检测" }).click();
  await expect(
    dialog.getByRole("status").filter({ hasText: /检测中|检测完成/ }),
  ).toBeVisible();
  await page.reload();
  await page.getByRole("button", { name: "网段与检测配置" }).click();
  await expect(
    page.getByRole("button", { name: "选择检测网段" }),
  ).toContainText("已选 1 个网段");
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
  await page.screenshot({
    path: "test-results/cf-config-mobile.png",
    fullPage: true,
    animations: "disabled",
  });
  await page.getByRole("button", { name: "Close", exact: true }).click();
  await expect(dialog).toBeHidden();
  await page.screenshot({
    path: "test-results/cf-mobile.png",
    fullPage: true,
    animations: "disabled",
  });
  await domain.click();
  await page.getByRole("button", { name: "删除地址", exact: true }).click();
  await page.getByRole("button", { name: "确认删除", exact: true }).click();
  await expect(page).toHaveURL(/\/cf$/);
  await expect(page.getByText(name, { exact: true })).toHaveCount(0);
});
