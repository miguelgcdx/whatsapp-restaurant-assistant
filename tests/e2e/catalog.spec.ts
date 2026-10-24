import { expect, test } from "@playwright/test";

test.use({ trace: "off", screenshot: "off", video: "off" });
test("anonymous catalog and continuation requests do not grant authority", async ({
  page,
  request,
}) => {
  await page.goto("/dashboard/catalog?limit=1&cursor=opaque");
  await expect(page).toHaveURL(/\/sign-in$/);
  const response = await request.get(
    "/api/staff/catalog?limit=1&cursor=opaque",
    { headers: { Authorization: "Bearer forged", "X-Role": "admin" } },
  );
  expect(response.status()).toBe(401);
  expect(response.headers()["cache-control"]).toBe("no-store");
  expect(await response.json()).toEqual({
    error: { code: "unauthenticated", message: "Authentication is required." },
    request_id: expect.stringMatching(/^[a-f0-9]{32}$/),
  });
});
