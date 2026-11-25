import { expect, test, type Page } from "@playwright/test";

const user = "admin";
const password = "correct-horse-battery";
const shots = process.env.SCREENSHOTS; // a directory, or unset to skip
const decoPort = process.env.DECO_PORT ?? "20602";

async function signIn(page: Page) {
  await page.goto("/");
  const setup = page.getByRole("heading", { name: "Set up Atlantis" });
  const login = page.getByRole("heading", { name: "Sign in to Atlantis" });
  await expect(setup.or(login)).toBeVisible();
  await page.getByLabel("Username").fill(user);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: /Create account|Sign in/ }).click();
  const connect = page.getByRole("heading", { name: /(Connect|Reconnect) your Deco/ });
  const home = page.getByText(/^Internet (is|status)/);
  await expect(connect.or(home)).toBeVisible();
  if (await connect.isVisible()) await connectDeco(page);
  await expect(home).toBeVisible();
}

/** Adds the pretend Deco from the Connect page. */
async function connectDeco(page: Page, opts: { wrongFirst?: boolean } = {}) {
  await page.getByLabel("Main Deco's IP address").fill("127.0.0.1");
  await page.getByLabel("TP-Link ID email").fill("demo@example.com");
  if (!(await page.getByLabel("Port").isVisible())) await page.getByRole("button", { name: "Show advanced options" }).click();
  await page.getByLabel("Port").fill(decoPort);
  if (opts.wrongFirst) {
    await page.getByLabel("TP-Link ID password").fill("not-the-password");
    await page.getByRole("button", { name: /^(Connect|Reconnect)$/ }).click();
    await expect(page.getByText(/rejected the TP-Link ID email or password/)).toBeVisible();
  }
  await page.getByLabel("TP-Link ID password").fill("demo-password");
  await shot(page, "connect");
  await page.getByRole("button", { name: /^(Connect|Reconnect)$/ }).click();
}

async function shot(page: Page, name: string, fullPage = true) {
  if (shots) await page.screenshot({ path: `${shots}/${name}.png`, fullPage });
}

test.describe.configure({ mode: "serial" });

test("connect the Deco from the UI", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Username").fill(user);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: /Create account|Sign in/ }).click();
  await expect(page.getByRole("heading", { name: /(Connect|Reconnect) your Deco/ })).toBeVisible();
  await connectDeco(page, { wrongFirst: true });
  await expect(page.getByText(/^Internet (is|status)/)).toBeVisible();
});

test("every page renders against the fake mesh", async ({ page }) => {
  await signIn(page);
  await expect(page.getByText("Connected to the Deco")).toBeVisible();
  await expect(page.getByRole("link", { name: /משרד/ }).first()).toBeVisible(); // Hebrew node name
  await shot(page, "overview");

  await page.getByRole("link", { name: "Clients" }).first().click();
  await expect(page.getByRole("heading", { name: "Clients" })).toBeVisible();
  await expect(page.locator("tbody tr")).not.toHaveCount(0);
  await shot(page, "clients");

  await page.getByLabel("Search").fill("zzzz-nothing");
  await expect(page.getByText("No devices match these filters.")).toBeVisible();
  await page.getByLabel("Search").fill("");

  await page.locator("tbody tr a").first().click();
  await expect(page.getByRole("heading", { name: "Presence" })).toBeVisible();
  await shot(page, "client-detail");

  await page.getByRole("link", { name: "Decos" }).first().click();
  await expect(page.getByText("main unit").first()).toBeVisible();
  await shot(page, "nodes");
  await page.locator("main a[href^='/nodes/']").first().click();
  await expect(page.getByRole("heading", { name: /Connected now/ })).toBeVisible();
  await shot(page, "node-detail");

  await page.getByRole("link", { name: "Wi-Fi" }).first().click();
  await expect(page.getByRole("heading", { name: "Networks" })).toBeVisible();
  await shot(page, "wifi");

  await page.getByRole("link", { name: "Events" }).first().click();
  await expect(page.getByRole("heading", { name: "Events" })).toBeVisible();
  await shot(page, "events");

  await page.getByRole("link", { name: "Settings" }).first().click();
  await expect(page.getByRole("heading", { name: "Configuration" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Deco", exact: true })).toBeVisible();
  await shot(page, "settings");
});

test("rename and watch a client", async ({ page }) => {
  await signIn(page);
  await page.goto("/clients");
  await page.locator("tbody tr a").first().click();
  await page.getByLabel("Name in Atlantis").fill("Noa's tablet");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByText("Saved")).toBeVisible();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Noa's tablet");
  await page.getByRole("switch", { name: "Watch this device" }).click();
  await expect(page.getByText("Now watching")).toBeVisible();
});

test("Wi-Fi change needs the typed confirmation", async ({ page }) => {
  await signIn(page);
  await page.goto("/wifi");
  await page.getByLabel("Network name").fill("Example-Net");
  await page.getByLabel("Password").fill("example-pass-123");
  await page.getByRole("button", { name: "Change Wi-Fi" }).click();
  const dialog = page.getByRole("alertdialog");
  const confirm = dialog.getByRole("button", { name: "Change Wi-Fi" });
  await expect(confirm).toBeDisabled();
  await dialog.getByLabel("Type CHANGE to confirm").fill("CHANGE");
  await expect(confirm).toBeEnabled();
  if (shots) await page.screenshot({ path: `${shots}/wifi-confirm.png` });
  await dialog.getByRole("button", { name: "Cancel" }).click();
});

test("add a notification channel", async ({ page }) => {
  await signIn(page);
  await page.goto("/settings");
  await page.getByRole("button", { name: "Add channel" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill("Logger");
  await dialog.getByLabel("Shoutrrr URL").fill("logger://");
  if (shots) await page.screenshot({ path: `${shots}/channel-dialog.png` });
  await dialog.getByRole("button", { name: "Add channel" }).click();
  await expect(page.getByText("Channel added")).toBeVisible();
  await expect(page.getByText("Logger", { exact: true })).toBeVisible();
});

test("dark mode and phone layout", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page);
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  await shot(page, "mobile-overview", false); // the bottom bar is fixed
  await page.getByRole("button", { name: "Dark mode" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await shot(page, "mobile-dark", false);
  await page.setViewportSize({ width: 1280, height: 860 });
  await page.goto("/");
  await expect(page.getByText(/^Internet (is|status)/)).toBeVisible();
  await shot(page, "overview-dark");
  await page.goto("/clients");
  await expect(page.locator("tbody tr")).not.toHaveCount(0);
  await shot(page, "clients-dark");
});

test("change the refresh times", async ({ page }) => {
  await signIn(page);
  await page.goto("/settings");
  await page.getByLabel("Check devices every").selectOption("15s");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByText("Refresh times saved")).toBeVisible();
  await expect(page.getByText("Set here; overrides the configuration file.")).toBeVisible();
});

test("remove the Deco and connect it again", async ({ page }) => {
  await signIn(page);
  await page.goto("/settings");
  await page.getByRole("button", { name: "Remove", exact: true }).click();
  const dialog = page.getByRole("alertdialog");
  await dialog.getByLabel("Type REMOVE to confirm").fill("REMOVE");
  await dialog.getByRole("button", { name: "Remove" }).click();
  await expect(page.getByRole("heading", { name: "Connect your Deco" })).toBeVisible();
  await connectDeco(page);
  await expect(page.getByText(/^Internet (is|status)/)).toBeVisible();
});
