import {
  type JourneyInput,
  runOrdersBrowser,
} from "../helpers/staff-orders-browser-run.mts";

/** One clean-context journey; no saved storage, captures, mutations or diagnostics. */
async function journey(input: JourneyInput) {
  const { chromium, expect } = await import("@playwright/test");
  const browser = await chromium.launch({
    channel: "chromium",
    headless: true,
    env: input.env,
  });
  const abort = () => {
    void browser.close().catch(() => {});
  };
  input.signal.addEventListener("abort", abort, { once: true });
  try {
    if (input.signal.aborted) throw new Error("journey stopped");
    const context = await browser.newContext({ serviceWorkers: "block" });
    context.setDefaultTimeout(10_000);
    context.setDefaultNavigationTimeout(15_000);
    let signingIn = true;
    let rejectedRequest = false;
    // Fail closed on unexpected providers or writes; never mock a successful read.
    await context.route("**/*", async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      if (
        url.origin === input.origin &&
        (request.method() === "GET" ||
          (signingIn &&
            request.method() === "POST" &&
            url.pathname === "/sign-in"))
      )
        await route.continue();
      else {
        rejectedRequest = true;
        await route.abort();
      }
    });
    const page = await context.newPage();
    await page.goto(`${input.origin}/sign-in`);
    // Existing password toggle is the supported hydration signal, not a sleep.
    await page
      .getByRole("button", { name: "Show password", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "Hide password", exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Hide password", exact: true })
      .click();
    await page
      .getByLabel("Work email", { exact: true })
      .fill(input.fixture.email);
    await page
      .getByLabel("Password", { exact: true })
      .fill(input.fixture.password);
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
    await expect(page).toHaveURL(`${input.origin}/dashboard`);
    await expect(
      page.getByText(`Signed in as ${input.fixture.name}.`, { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("Staff access confirmed.", { exact: true }),
    ).toBeVisible();
    signingIn = false;
    await page
      .getByRole("navigation", { name: "Staff navigation", exact: true })
      .getByRole("link", { name: "Orders", exact: true })
      .click();
    await expect(page).toHaveURL(`${input.origin}/dashboard/orders`);
    await expect(
      page.getByRole("heading", { name: "Staff orders", exact: true }),
    ).toBeVisible();
    const orderPath = `/dashboard/orders/${input.seed.order_id}`;
    const orderLink = page.getByRole("link", {
      name: `Order ${input.seed.order_id}`,
      exact: true,
    });
    await expect(orderLink).toHaveAttribute("href", orderPath);
    await orderLink.click();
    await expect(page).toHaveURL(`${input.origin}${orderPath}`);
    await expect(
      page.getByRole("heading", {
        name: `Order ${input.seed.order_id}`,
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      page.getByText(`Version: ${input.seed.initial_version}`, { exact: true }),
    ).toBeVisible();
    const status = page.getByRole("region", {
      name: "Order status",
      exact: true,
    });
    await expect(
      status.getByText("Fulfillment: submitted", { exact: true }),
    ).toBeVisible();
    await expect(
      status.getByText("Payment: unpaid", { exact: true }),
    ).toBeVisible();
    const summary = page.getByRole("region", {
      name: "Confirmed summary",
      exact: true,
    });
    await expect(
      summary.getByText(`Summary: ${input.seed.summary_id}`, { exact: true }),
    ).toBeVisible();
    await expect(
      summary.getByText(`${input.seed.unit_count} units in confirmed summary`, {
        exact: true,
      }),
    ).toBeVisible();
    const unitsPath = `${orderPath}/summaries/${input.seed.summary_id}/units`;
    const unitsLink = summary.getByRole("link", {
      name: "View exact confirmed units",
      exact: true,
    });
    await expect(unitsLink).toHaveAttribute("href", unitsPath);
    await unitsLink.click();
    await expect(page).toHaveURL(`${input.origin}${unitsPath}`);
    await expect(
      page.getByRole("heading", {
        name: "Confirmed pickup units",
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      page.getByText(`Order: ${input.seed.order_id}`, { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText(`Immutable summary: ${input.seed.summary_id}`, {
        exact: true,
      }),
    ).toBeVisible();
    // Traverse only the immutable-summary pagination, allowing any seed >=2 units.
    let units = 0;
    const seen = new Set<string>();
    while (units < input.seed.unit_count) {
      const items = page.getByRole("main").getByRole("listitem");
      await expect(items.first()).toBeVisible();
      const count = await items.count();
      await expect(
        page.getByText(
          `${input.seed.unit_count} units in confirmed summary; ${count} on this page.`,
          { exact: true },
        ),
      ).toBeVisible();
      for (const item of await items.all()) {
        const id = await item.getByText(/^Unit: /).textContent();
        if (!id || seen.has(id)) throw new Error("unit mismatch");
        seen.add(id);
      }
      units += count;
      if (units < input.seed.unit_count) {
        const next = page
          .getByRole("navigation", {
            name: "Confirmed units pagination",
            exact: true,
          })
          .getByRole("link", { name: "Next page", exact: true });
        const href = await next.getAttribute("href");
        if (!href || !href.startsWith(`${unitsPath}?`))
          throw new Error("pagination mismatch");
        await next.click();
        await expect(page).toHaveURL(`${input.origin}${href}`);
        await expect(
          page.getByText(`Immutable summary: ${input.seed.summary_id}`, {
            exact: true,
          }),
        ).toBeVisible();
      }
    }
    expect(units).toBe(input.seed.unit_count);
    await expect(
      page.getByText("End of confirmed units.", { exact: true }),
    ).toBeVisible();
    if (rejectedRequest) throw new Error("journey rejected");
    await context.close();
  } finally {
    input.signal.removeEventListener("abort", abort);
    await browser.close();
  }
}

// The helper owns argument validation, execution gate and sanitized failure reporting.
await runOrdersBrowser(journey);
