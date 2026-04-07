import { execFile } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";

import { expect, test } from "@playwright/test";

const execFileAsync = promisify(execFile);
const repoRoot = path.resolve(process.cwd(), "..");

async function runCLI(args: string[]) {
  const { stdout } = await execFileAsync(
    "go",
    ["run", "./cmd/agentpad", "--server", "http://127.0.0.1:8080", "--actor", "debug-bot", "--json", ...args],
    { cwd: repoRoot },
  );
  return JSON.parse(stdout) as Record<string, unknown>;
}

test("keeps replacement delete chips near the changed paragraph after later remote edits", async ({ page }, testInfo) => {
  const tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "agentpad-remote-layout-"));
  const docPath = path.join(tempDir, "remote-layout.md");
  const screenshotPath = testInfo.outputPath("remote-artifact-layout.png");

  try {
    await fs.writeFile(docPath, "# Artifact Debug\n\nAlpha beta gamma.\n\nSecond paragraph for replacement testing.\n", "utf8");

    await page.goto("/");
    await page.getByLabel("Choose file").setInputFiles(docPath);
    await page.getByRole("button", { name: "Import file" }).click();

    await expect(page.getByRole("heading", { name: "remote-layout" })).toBeVisible();
    await expect(page.getByText("Live").first()).toBeVisible();
    await page.waitForTimeout(1000);

    const documentID = new URL(page.url()).pathname.replace(/^\/+/, "");

    await runCLI(["edit", documentID, "--match", "gamma.", "--replace", "gamma really."]);
    await expect(page.getByText("markdown • rev 1")).toBeVisible({ timeout: 15_000 });
    await runCLI([
      "edit",
      documentID,
      "--match",
      "Second paragraph for replacement testing.",
      "--replace",
      "Second paragraph for jsdiff token testing.",
    ]);
    await expect(page.getByText("markdown • rev 2")).toBeVisible({ timeout: 15_000 });
    await runCLI(["edit", documentID, "--match", "Artifact Debug", "--replace", "Live Artifact Debug"]);
    await expect(page.getByText("markdown • rev 3")).toBeVisible({ timeout: 15_000 });

    const replacementChip = page.locator(".cm-remote-change-delete").filter({ hasText: "replacement" }).first();
    const secondParagraphLine = page.locator(".cm-line").filter({ hasText: "Second paragraph for jsdiff" }).first();
    const headingLine = page.locator(".cm-line").filter({ hasText: "# Live Artifact Debug" }).first();

    await expect(replacementChip).toBeVisible({ timeout: 15_000 });
    await expect(headingLine).toBeVisible();
    await expect(secondParagraphLine).toBeVisible({ timeout: 15_000 });
    await expect(secondParagraphLine).toContainText("token testing.", { timeout: 15_000 });

    const replacementBox = await replacementChip.boundingBox();
    const paragraphBox = await secondParagraphLine.boundingBox();
    const headingBox = await headingLine.boundingBox();

    expect(replacementBox).not.toBeNull();
    expect(paragraphBox).not.toBeNull();
    expect(headingBox).not.toBeNull();

    const replacementTop = replacementBox!.y;
    const paragraphTop = paragraphBox!.y;
    const headingTop = headingBox!.y;

    await page.screenshot({ path: screenshotPath, fullPage: true });
    await testInfo.attach("remote-artifact-layout", {
      path: screenshotPath,
      contentType: "image/png",
    });
    testInfo.annotations.push({
      type: "layout-metrics",
      description: JSON.stringify({
        replacementTop,
        paragraphTop,
        headingTop,
      }),
    });

    expect(Math.abs(replacementTop - paragraphTop)).toBeLessThan(24);
    expect(Math.abs(replacementTop - headingTop)).toBeGreaterThan(24);
  } finally {
    await fs.rm(tempDir, { recursive: true, force: true });
  }
});
