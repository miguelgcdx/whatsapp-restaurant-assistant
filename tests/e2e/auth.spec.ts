import { expect, test } from "@playwright/test";

// Parent T3 supplies an isolated real Next+Go baseURL and generated fixtures.
// No credentials or storageState are read from inherited environment/files here.
test.use({ trace: "off", screenshot: "off", video: "off" });

test("host layout is restaurant-facing in both themes and viewport sizes", async ({
  page,
}) => {
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme });
    for (const width of [320, 1440]) {
      await page.setViewportSize({ width, height: 1000 });
      await page.goto("/sign-in");
      await expect(
        page.getByRole("heading", { name: "Good to see you.", exact: true }),
      ).toBeVisible();
      await expect(
        page.getByRole("banner").getByText("Service", { exact: true }),
      ).toBeVisible();
      await expect(
        page.getByText("Sign in to your restaurant workspace.", {
          exact: true,
        }),
      ).toBeVisible();
      await expect(
        page
          .getByRole("contentinfo")
          .getByText("Menu. Availability. One place."),
      ).toBeVisible();
      await expect(
        page.getByText(/generated local staff credentials|DESIGN PREVIEW/),
      ).toHaveCount(0);
      await expect(page.locator("[data-preview-only]")).toHaveCount(0);
      // The static shell can paint before the form's Suspense boundary resolves.
      await expect(
        page.getByLabel("Work email", { exact: true }),
      ).toBeVisible();
      await expect(page.locator("main form")).toBeVisible();
      const geometry = await page.locator("main").evaluate((element) => ({
        overflow: document.documentElement.scrollWidth > window.innerWidth,
        formWidth:
          element.querySelector("form")?.getBoundingClientRect().width ?? 0,
        small: [
          ...element.querySelectorAll('input:not([type="hidden"]),button'),
        ].filter((control) => {
          const box = control.getBoundingClientRect();
          return box.width < 44 || box.height < 44;
        }).length,
      }));
      expect(geometry.overflow).toBe(false);
      expect(geometry.formWidth).toBeGreaterThan(0);
      expect(geometry.formWidth).toBeLessThanOrEqual(384);
      expect(geometry.small).toBe(0);
    }
  }
});

test("host password visibility is an explicit reversible control", async ({
  page,
}) => {
  await page.goto("/sign-in");
  const password = page.getByLabel("Password", { exact: true });
  await expect(password).toHaveAttribute("type", "password");
  await page
    .getByRole("button", { name: "Show password", exact: true })
    .click();
  await expect(password).toHaveAttribute("type", "text");
  await expect(
    page.getByRole("button", { name: "Hide password", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await page
    .getByRole("button", { name: "Hide password", exact: true })
    .click();
  await expect(password).toHaveAttribute("type", "password");
  await expect(
    page.getByRole("button", { name: "Show password", exact: true }),
  ).toHaveAttribute("aria-pressed", "false");
});

test("host submit reflects actual action pending without contacting authentication", async ({
  page,
}) => {
  await page.goto("/sign-in");
  // Public synthetic input only. The action request is held and aborted locally.
  await page.getByLabel(/email/i).fill("preview@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("public-not-a-real-account-password");
  let release: (() => void) | undefined;
  let observed: (() => void) | undefined;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const started = new Promise<void>((resolve) => {
    observed = resolve;
  });
  await page.route("**/sign-in", async (route) => {
    if (route.request().method() !== "POST") return route.continue();
    observed?.();
    await held;
    await route.abort().catch(() => undefined);
  });
  try {
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
    await started;
    await expect(
      page.getByRole("button", { name: "Signing in…", exact: true }),
    ).toBeDisabled();
  } finally {
    release?.();
    await page.unrouteAll({ behavior: "wait" });
  }
});

test("sign-in URL data stays behind Suspense", async ({ page }) => {
  let signInUrlDiagnostic = false;
  page.on("console", (message) => {
    const text = message.text();
    if (
      text.includes("Next.js encountered URL data") &&
      text.includes("/sign-in")
    ) {
      signInUrlDiagnostic = true;
    }
  });
  for (const path of ["/sign-in", "/sign-in?error=1"]) {
    await page.goto(path);
    await expect(
      page.getByRole("heading", { name: "Good to see you.", exact: true }),
    ).toBeVisible();
    await expect(page.getByLabel("Work email", { exact: true })).toBeVisible();
    await expect(page.getByLabel("Password", { exact: true })).toBeVisible();
    if (path.includes("?")) {
      await expect(page.getByRole("main").getByRole("alert")).toHaveText(
        "We couldn’t sign you in. Check your details and try again.",
      );
      await expect(page.locator("form")).toHaveAttribute(
        "aria-describedby",
        "sign-in-error sign-in-help",
      );
    } else {
      await expect(page.getByRole("main").getByRole("alert")).toHaveCount(0);
      await expect(page.locator("form")).toHaveAttribute(
        "aria-describedby",
        "sign-in-help",
      );
    }
    // Exercise the visible development insight, not just HTTP success.
    const devtools = page.locator("nextjs-portal");
    await devtools
      .getByRole("button", { name: "Open Next.js Dev Tools", exact: true })
      .last()
      .click();
    const menuInsights = devtools.getByRole("menuitem", { name: /Insights/ });
    if (await menuInsights.count()) await menuInsights.click();
    const insightsTab = devtools.getByRole("button", { name: /^Insights/ });
    if (await insightsTab.count()) await insightsTab.click();
    await expect(
      devtools.getByText("Next.js encountered URL data outside of Suspense.", {
        exact: true,
      }),
    ).toHaveCount(0);
    expect(signInUrlDiagnostic).toBe(false);
    await page.keyboard.press("Escape");
  }
});

test("anonymous direct protected requests fail closed", async ({
  page,
  request,
}) => {
  await page.goto("/dashboard");
  await expect(page).toHaveURL(/\/sign-in$/);
  await expect(
    page.getByRole("heading", { name: "Good to see you." }),
  ).toBeVisible();
  await page.setViewportSize({ width: 320, height: 720 });
  await expect(page.getByLabel("Work email", { exact: true })).toBeVisible();
  await expect(page.getByLabel("Password", { exact: true })).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Sign in", exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("Work email", { exact: true })).toHaveAttribute(
    "autocomplete",
    "username",
  );
  await expect(page.getByLabel("Password", { exact: true })).toHaveAttribute(
    "autocomplete",
    "current-password",
  );
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
  await page.goto("/sign-in?error=1");
  await expect(page.getByRole("main").getByRole("alert")).toHaveText(
    "We couldn’t sign you in. Check your details and try again.",
  );
  await expect(page.locator("form")).toHaveAttribute(
    "aria-describedby",
    "sign-in-error sign-in-help",
  );
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
  const response = await request.get("/api/staff/access", {
    headers: { Authorization: "Bearer forged", "X-Role": "admin" },
  });
  expect(response.status()).toBe(401);
  expect(await response.json()).toEqual({
    error: { code: "unauthenticated", message: "Authentication is required." },
  });
});

test("public access panels retain messages and responsive landmarks", async ({
  page,
}) => {
  const panels = [
    {
      path: "/sign-in",
      heading: "Good to see you.",
      message: "Sign in to your restaurant workspace.",
    },
    {
      path: "/access-denied",
      heading: "Staff access is not available.",
      message: "Your account does not currently have staff access.",
    },
    {
      path: "/unavailable",
      heading: "Staff access is temporarily unavailable.",
      message: "Please try again when the local services are available.",
    },
  ];
  for (const width of [320, 1440]) {
    await page.setViewportSize({ width, height: 900 });
    for (const panel of panels) {
      await page.goto(panel.path);
      await expect(page).toHaveTitle("Sign Restaurant");
      await expect(page.getByRole("main")).toHaveCount(1);
      await expect(
        page.getByRole("heading", { name: panel.heading, exact: true }),
      ).toBeVisible();
      await expect(
        page.getByText(panel.message, { exact: true }),
      ).toBeVisible();
      await expect(
        page.getByText(panel.path === "/sign-in" ? "Service" : "Service desk", {
          exact: true,
        }),
      ).toBeVisible();
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
      ).toBe(true);
      if (panel.path === "/sign-in") {
        await expect(
          page.getByLabel("Work email", { exact: true }),
        ).toBeVisible();
        await expect(
          page.getByLabel("Password", { exact: true }),
        ).toBeVisible();
        await expect(
          page.getByRole("button", { name: "Sign in", exact: true }),
        ).toBeVisible();
      } else if (panel.path === "/access-denied") {
        await expect(
          page.getByRole("link", { name: "Return to sign in", exact: true }),
        ).toHaveAttribute("href", "/sign-in");
        await expect(
          page.getByRole("button", { name: "Sign out", exact: true }),
        ).toBeVisible();
      } else {
        await expect(
          page.getByRole("link", { name: "Try again", exact: true }),
        ).toHaveAttribute("href", "/dashboard");
      }
    }
  }
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
