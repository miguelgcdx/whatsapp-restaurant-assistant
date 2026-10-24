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
  const origin = new URL(page.url()).origin;
  for (const [path, field] of [
    ["/api/staff/restaurant/availability", "open"],
    [
      "/api/staff/catalog/items/00000000-0000-4000-8000-000000000001/availability",
      "available",
    ],
  ]) {
    const denied = await request.patch(path, {
      headers: {
        origin,
        "idempotency-key": "anonymous-intent",
        Authorization: "Bearer forged",
        "X-Role": "admin",
      },
      data: { expected_version: "0", [field]: false },
    });
    expect(denied.status()).toBe(401);
    expect(denied.headers()["cache-control"]).toBe("no-store");
    expect(await denied.json()).toEqual({
      error: {
        code: "unauthenticated",
        message: "Authentication is required.",
      },
      request_id: expect.stringMatching(/^[a-f0-9]{32}$/),
    });
    const method = await request.get(path);
    expect(method.status()).toBe(405);
    expect(method.headers().allow).toBe("PATCH");
  }
});
