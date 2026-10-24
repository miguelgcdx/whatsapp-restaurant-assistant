import { expect, test } from "@playwright/test";

// Parent T3 supplies an isolated real Next+Go baseURL and generated fixtures.
// No credentials or storageState are read from inherited environment/files here.
test.use({ trace: "off", screenshot: "off", video: "off" });

test("anonymous direct protected requests fail closed", async ({
  page,
  request,
}) => {
  await page.goto("/dashboard");
  await expect(page).toHaveURL(/\/sign-in$/);
  await expect(
    page.getByRole("heading", { name: "Staff sign in" }),
  ).toBeVisible();
  const response = await request.get("/api/staff/access", {
    headers: { Authorization: "Bearer forged", "X-Role": "admin" },
  });
  expect(response.status()).toBe(401);
  expect(await response.json()).toEqual({
    error: { code: "unauthenticated", message: "Authentication is required." },
  });
});

test("browser auth token and signup endpoints stay closed", async ({
  request,
}) => {
  for (const path of ["token", "get-session", "sign-up/email"]) {
    const response = await request.get(`/api/auth/${path}`);
    expect(response.status()).toBe(404);
    expect(response.headers()["set-auth-jwt"]).toBeUndefined();
  }
});
