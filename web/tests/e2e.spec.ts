import { execFile } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";

import { expect, test } from "@playwright/test";

const execFileAsync = promisify(execFile);
const repoRoot = path.resolve(process.cwd(), "..");
const liveUpdateTimeout = 15_000;

async function runCLI(args: string[]) {
  const { stdout } = await execFileAsync(
    "go",
    ["run", "./cmd/agentpad", "--server", "http://127.0.0.1:8080", "--actor", "cli-user", "--json", ...args],
    { cwd: repoRoot },
  );
  return JSON.parse(stdout) as Record<string, unknown>;
}

test("opens a local file in the collaborative UI", async ({ page }) => {
  const samplePath = path.resolve(repoRoot, "testdata", "sample.md");

  await page.goto("/");
  await page.getByPlaceholder("/Users/you/Documents/note.md").fill(samplePath);
  await page.getByRole("button", { name: "Open file" }).click();

  await expect(page.getByRole("heading", { name: "sample" })).toBeVisible();
  await expect(page.getByText("Live").first()).toBeVisible();
  await expect(page.getByRole("heading", { name: "Comments", exact: true })).toBeVisible();
});

test("updates thread state live when the CLI changes it", async ({ page }) => {
  const tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "agentpad-live-"));
  const docPath = path.join(tempDir, "websocket-thread.md");

  try {
    await fs.writeFile(docPath, "# Title\n\nAlpha beta gamma delta\n", "utf8");

    await page.goto("/");
    await page.getByPlaceholder("/Users/you/Documents/note.md").fill(docPath);
    await page.getByRole("button", { name: "Open file" }).click();

    await expect(page.getByRole("heading", { name: "websocket-thread" })).toBeVisible();
    await expect(page.getByText("Live").first()).toBeVisible();
    await expect(page.getByText("No open comments")).toBeVisible();

    const created = await runCLI(["threads", "create", docPath, "--start", "9", "--end", "19", "--body", "CLI comment"]);
    const threadId = String(created.id ?? "");
    const threadCard = page.locator("[data-thread-card]").first();

    await expect(threadCard).toBeVisible({ timeout: liveUpdateTimeout });
    await expect(page.locator(".cm-thread-highlight")).toHaveCount(1, { timeout: liveUpdateTimeout });
    await expect(threadCard.getByText("1 comment")).toBeVisible({ timeout: liveUpdateTimeout });
    await expect(threadCard.locator(".thread-unread-badge")).toHaveText("1 new", { timeout: liveUpdateTimeout });
    await expect(threadCard.getByText("Alpha beta")).toBeVisible({ timeout: liveUpdateTimeout });

    await threadCard.getByRole("button", { name: /Alpha beta/ }).click();
    await expect(threadCard.locator(".thread-unread-badge")).toHaveCount(0);
    await expect(threadCard.getByText("CLI comment")).toBeVisible();

    await runCLI(["threads", "reply", docPath, threadId, "--body", "CLI reply"]);
    await expect(threadCard.getByText("2 comments")).toBeVisible({ timeout: liveUpdateTimeout });
    await expect(threadCard.getByText("CLI reply")).toBeVisible({ timeout: liveUpdateTimeout });
    await expect(threadCard.locator(".thread-unread-badge")).toHaveCount(0);

    await runCLI(["threads", "resolve", docPath, threadId]);
    await expect(page.locator(".cm-thread-highlight")).toHaveCount(0, { timeout: liveUpdateTimeout });
    await expect(threadCard.locator(".thread-state")).toHaveText("resolved", { timeout: liveUpdateTimeout });
    await expect(threadCard.getByRole("button", { name: "Reopen" })).toBeVisible({ timeout: liveUpdateTimeout });
    await expect(page.getByRole("tab", { name: /resolved/i })).toHaveAttribute("aria-selected", "true", { timeout: liveUpdateTimeout });

    await runCLI(["threads", "reopen", docPath, threadId]);
    await expect(page.locator(".cm-thread-highlight")).toHaveCount(1, { timeout: liveUpdateTimeout });
    await expect(threadCard.locator(".thread-state")).toHaveText("open", { timeout: liveUpdateTimeout });
    await expect(threadCard.getByRole("button", { name: "Resolve" })).toBeVisible({ timeout: liveUpdateTimeout });
    await expect(page.getByRole("tab", { name: /open/i })).toHaveAttribute("aria-selected", "true", { timeout: liveUpdateTimeout });
  } finally {
    await fs.rm(tempDir, { recursive: true, force: true });
  }
});

test("shows remote editor highlights for CLI inserts and clears them on click", async ({ page }) => {
  const tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "agentpad-remote-edit-"));
  const docPath = path.join(tempDir, "remote-edit.md");

  try {
    await fs.writeFile(docPath, "# Title\n\nAlpha beta gamma delta\n", "utf8");

    await page.goto("/");
    await page.getByPlaceholder("/Users/you/Documents/note.md").fill(docPath);
    await page.getByRole("button", { name: "Open file" }).click();
    await expect(page.getByRole("heading", { name: "remote-edit" })).toBeVisible();
    await expect(page.getByText("Live").first()).toBeVisible({ timeout: liveUpdateTimeout });
    await page.waitForTimeout(1000);

    await runCLI(["edit", docPath, "--start", "31", "--end", "31", "--text", " together"]);

    const remoteInsert = page.locator(".cm-remote-change-insert");
    await expect(remoteInsert).toHaveCount(1, { timeout: liveUpdateTimeout });
    await expect(remoteInsert.first()).toHaveAttribute("title", "Edited by cli-user", { timeout: liveUpdateTimeout });

    await remoteInsert.first().click();
    await expect(page.locator(".cm-remote-change-insert")).toHaveCount(0, { timeout: liveUpdateTimeout });
  } finally {
    await fs.rm(tempDir, { recursive: true, force: true });
  }
});

test("shows remote editor highlights for CLI edits", async ({ page }) => {
  const tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "agentpad-cli-edit-"));
  const docPath = path.join(tempDir, "cli-edit.md");

  try {
    await fs.writeFile(docPath, "# Title\n\nAlpha beta gamma delta\n", "utf8");

    await page.goto("/");
    await page.getByPlaceholder("/Users/you/Documents/note.md").fill(docPath);
    await page.getByRole("button", { name: "Open file" }).click();
    await expect(page.getByRole("heading", { name: "cli-edit" })).toBeVisible();
    await expect(page.getByText("Live").first()).toBeVisible({ timeout: liveUpdateTimeout });
    await page.waitForTimeout(1000);

    await runCLI(["edit", docPath, "--start", "15", "--end", "19", "--text", "crew"]);

    await expect(page.locator(".cm-remote-change-replace")).toHaveCount(1, { timeout: liveUpdateTimeout });
    await expect(page.locator(".cm-remote-change-delete")).toHaveCount(1, { timeout: liveUpdateTimeout });
    await expect(page.locator(".cm-remote-change-replace").first()).toHaveAttribute("title", "Edited by cli-user", {
      timeout: liveUpdateTimeout,
    });
    await expect(page.locator(".cm-remote-change-delete").first()).toContainText("beta", { timeout: liveUpdateTimeout });
    await expect(page.locator(".cm-remote-change-author").first()).toHaveText("cli-user", { timeout: liveUpdateTimeout });

    await page.locator(".cm-remote-change-replace").first().click();
    await expect(page.locator(".cm-remote-change-replace")).toHaveCount(0, { timeout: liveUpdateTimeout });
    await expect(page.locator(".cm-remote-change-delete")).toHaveCount(0, { timeout: liveUpdateTimeout });

    await runCLI(["edit", docPath, "--start", "20", "--end", "25", "--text", "squad"]);

    await expect(page.locator(".cm-remote-change-replace")).toHaveCount(1, { timeout: liveUpdateTimeout });
    await expect(page.locator(".cm-remote-change-delete")).toHaveCount(1, { timeout: liveUpdateTimeout });
    await expect(page.locator(".cm-remote-change-delete").first()).toContainText("gamma", { timeout: liveUpdateTimeout });

    await page.locator(".cm-remote-change-delete").first().click();
    await expect(page.locator(".cm-remote-change-replace")).toHaveCount(0, { timeout: liveUpdateTimeout });
    await expect(page.locator(".cm-remote-change-delete")).toHaveCount(0, { timeout: liveUpdateTimeout });
  } finally {
    await fs.rm(tempDir, { recursive: true, force: true });
  }
});
