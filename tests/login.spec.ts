import { test, expect } from "@playwright/test";

for (const javaScriptEnabled of [true, false]) {
  test(`登录通过 POST 提交（JavaScript ${javaScriptEnabled ? "开启" : "关闭"}）`, async ({ browser }) => {
    const context = await browser.newContext({ javaScriptEnabled });
    try {
      const page = await context.newPage();
      const submissions: string[] = [];
      page.on("request", (request) => {
        const url = new URL(request.url());
        if (url.pathname === "/login") {
          expect(url.searchParams.has("password")).toBe(false);
          expect(url.searchParams.has("username")).toBe(false);
          if (request.method() === "POST") submissions.push(request.method());
        }
      });
      await page.goto("http://127.0.0.1:3000/login");
      await page.getByLabel("用户名").fill(process.env.ADMIN_USERNAME || "admin");
      await page.getByLabel("密码", { exact: true }).fill("wrong-password");
      await page.getByRole("button", { name: "登录控制台" }).click();
      await expect(page.getByRole("alert").filter({ hasText: "用户名或密码错误" })).toBeVisible();
      await page.getByLabel("用户名").fill(process.env.ADMIN_USERNAME || "admin");
      await page.getByLabel("密码", { exact: true }).fill(process.env.ADMIN_PASSWORD!);
      await page.getByRole("button", { name: "登录控制台" }).click();
      await expect(page).toHaveURL("http://127.0.0.1:3000/nodes");
      expect(submissions).toHaveLength(2);
    } finally {
      await context.close();
    }
  });
}
