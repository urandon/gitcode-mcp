import { expect, test } from '@playwright/test';
import { mkdir, readFile } from 'node:fs/promises';
import path from 'node:path';
import { resolveBrowserTestPolicy } from '../src/lib/browser-test-policy';

const browserPolicy = resolveBrowserTestPolicy(process.env);
const launchURL = browserPolicy.adminLaunchURL;
const outputDir = browserPolicy.adminQAOutput;
const referencePath = browserPolicy.referencePath;

test('embedded admin launch, controls, and theme states', async ({ page, context }) => {
  test.skip(!launchURL || !outputDir, 'requires an explicit one-time local QA launch URL and output directory');
  await mkdir(outputDir!, { recursive: true });
  await page.setViewportSize({ width: 1487, height: 1058 });
  const firstSnapshot = page.waitForResponse((response) => response.url().endsWith('/api/admin/v1/snapshot') && response.status() === 200);
  const firstEvents = page.waitForResponse((response) => response.url().includes('/api/admin/v1/events') && response.status() === 200);
  await page.goto(launchURL!);
  await expect(page.getByRole('heading', { name: 'Local operator console' })).toBeVisible();
  await expect(page).not.toHaveURL(/launch=/);
  const snapshotPayload = await (await firstSnapshot).json();
  expect(snapshotPayload.api_version).toBe('1');
  expect(snapshotPayload.revision).toMatch(/^snapshot-/);
  expect(JSON.stringify(snapshotPayload)).not.toContain('/Users/');
  expect((await firstEvents).headers()['content-type']).toContain('text/event-stream');

  const cookies = await context.cookies();
  const session = cookies.find((cookie) => cookie.name === 'gitcode_mcp_admin_session');
  expect(session?.httpOnly).toBe(true);
  expect(session?.sameSite).toBe('Strict');

  const system = page.getByRole('radio', { name: 'System' });
  await expect(system).toHaveAttribute('aria-checked', 'true');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'system');
  await page.screenshot({ path: path.join(outputDir!, 'admin-system.png'), fullPage: true });

  await page.getByRole('radio', { name: 'Light' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await page.screenshot({ path: path.join(outputDir!, 'admin-light.png'), fullPage: true });

  await page.getByRole('radio', { name: 'Dark' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await page.screenshot({ path: path.join(outputDir!, 'admin-dark.png'), fullPage: true });

  await page.getByRole('button', { name: 'Open diagnostics' }).click();
  await expect(page.getByRole('heading', { name: 'Diagnostics', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Overview' }).click();
  await expect(page.getByRole('button', { name: 'Refresh snapshot' })).toBeEnabled();

  const reloadedSnapshot = page.waitForResponse((response) => response.url().endsWith('/api/admin/v1/snapshot') && response.status() === 200);
  await page.reload();
  await reloadedSnapshot;
  await expect(page.getByRole('heading', { name: 'Local operator console' })).toBeVisible();

  await system.click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'system');

  if (referencePath) {
    const reference = (await readFile(referencePath)).toString('base64');
    const implementation = (await readFile(path.join(outputDir!, 'admin-light.png'))).toString('base64');
    const comparePage = await context.newPage();
    await comparePage.setViewportSize({ width: 2974, height: 1058 });
    await comparePage.setContent(`<!doctype html><style>html,body{margin:0;background:#111}main{display:flex;width:2974px;height:1058px}figure{position:relative;margin:0;width:1487px;height:1058px;overflow:hidden}img{display:block;width:1487px;height:1058px}figcaption{position:absolute;top:12px;left:12px;padding:7px 10px;border-radius:5px;background:rgba(20,25,24,.86);color:#fff;font:600 13px system-ui}</style><main><figure><img src="data:image/png;base64,${reference}"><figcaption>Selected reference</figcaption></figure><figure><img src="data:image/png;base64,${implementation}"><figcaption>Embedded implementation</figcaption></figure></main>`);
    await comparePage.screenshot({ path: path.join(outputDir!, 'admin-light-comparison.png'), fullPage: true });
    await comparePage.close();
  }
});
