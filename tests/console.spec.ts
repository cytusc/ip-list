import { test, expect } from "@playwright/test";

test("登录、节点与网段配置的完整流程", async ({ page }, testInfo) => {
  const name = `测试节点-${Date.now()}`;
  await page.goto("/nodes");
  await expect(page).toHaveURL(/\/login$/);
  await page.getByLabel("用户名").fill(process.env.ADMIN_USERNAME || "admin");
  await page.getByLabel("密码", { exact: true }).fill("wrong-password");
  await page.getByRole("button", { name: "登录控制台" }).click();
  await expect(page.locator("form").getByRole("alert")).toContainText(
    "用户名或密码错误",
  );
  await page
    .getByLabel("密码", { exact: true })
    .fill(process.env.ADMIN_PASSWORD!);
  await page.getByRole("button", { name: "登录控制台" }).click();
  await expect(page).toHaveURL(/\/nodes$/);
  await page.getByRole("button", { name: "新增节点" }).first().click();
  let dialog = page.getByRole("dialog");
  await dialog.getByLabel("节点名称").fill(name);
  await dialog.getByLabel("节点地址").fill(`${Date.now()}.example.com`);
  await dialog.getByLabel("端口").fill("8080");
  await dialog.getByLabel("备注").fill("办公网络接入节点");
  await dialog.getByRole("button", { name: "保存配置" }).click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByRole("table")).toContainText(name);
  await page.getByRole("button", { name: `编辑${name}`, exact: true }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("端口").fill("9090");
  await dialog.getByRole("button", { name: "保存配置" }).click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByRole("table")).toContainText(":9090");
  await page.screenshot({
    path: testInfo.outputPath("nodes.png"),
    fullPage: true,
  });
  await page
    .getByRole("navigation", { name: "主导航", exact: true })
    .getByRole("link", { name: "网段配置" })
    .click();
  await page.getByRole("button", { name: "新增网段" }).first().click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("网段名称").fill("办公网段");
  await dialog.getByLabel("CIDR 网段").fill("192.168.12.1/24");
  await dialog.getByLabel("ASN").fill("13335");
  await dialog.getByLabel("服务商").fill("Cloudflare");
  await dialog.getByRole("button", { name: "保存配置" }).click();
  await expect(
    dialog.getByText("主机位需为 0", { exact: false }),
  ).toBeVisible();
  await dialog.getByLabel("CIDR 网段").fill("192.168.12.0/24");
  await dialog.getByRole("button", { name: "保存配置" }).click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByRole("table")).toContainText("192.168.12.0/24");
  await page.screenshot({
    path: testInfo.outputPath("networks.png"),
    fullPage: true,
  });
  await page.reload();
  await expect(page.getByRole("table")).toContainText("办公网段");
  await page
    .getByRole("navigation", { name: "主导航", exact: true })
    .getByRole("link", { name: "节点管理" })
    .click();
  await page.getByRole("button", { name: `删除${name}`, exact: true }).click();
  await page.getByRole("button", { name: "确认删除", exact: true }).click();
  await expect(page.getByRole("alertdialog")).not.toBeVisible();
  await page
    .getByRole("navigation", { name: "主导航", exact: true })
    .getByRole("link", { name: "网段配置" })
    .click();
  await page.getByRole("button", { name: "删除办公网段", exact: true }).click();
  await page.getByRole("button", { name: "确认删除", exact: true }).click();
  await expect(page.getByRole("alertdialog")).not.toBeVisible();
  await page
    .getByRole("navigation", { name: "主导航", exact: true })
    .getByRole("link", { name: "节点管理" })
    .click();
  await expect(page.getByRole("table")).not.toContainText(name);
  await page.getByRole("button", { name: "退出登录" }).click();
  await expect(page).toHaveURL(/\/login$/);
  await page.goto("/networks");
  await expect(page).toHaveURL(/\/login$/);
});

test("移动端登录和导航", async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/login");
  await page.getByLabel("用户名").fill(process.env.ADMIN_USERNAME || "admin");
  await page
    .getByLabel("密码", { exact: true })
    .fill(process.env.ADMIN_PASSWORD!);
  await page.getByRole("button", { name: "登录控制台" }).click();
  await expect(page).toHaveURL(/\/nodes$/);
  await page
    .getByRole("navigation", { name: "移动端导航" })
    .getByRole("link", { name: "网段配置" })
    .click();
  await expect(
    page.getByRole("heading", { name: "网段配置", exact: true }),
  ).toBeVisible();
  const noOverflow = await page.evaluate(
    () => document.documentElement.scrollWidth <= window.innerWidth,
  );
  expect(noOverflow).toBe(true);
  await page.screenshot({
    path: testInfo.outputPath("mobile.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "退出登录" }).click();
  await expect(page).toHaveURL(/\/login$/);
});
