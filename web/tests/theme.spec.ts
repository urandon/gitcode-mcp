import { expect, test } from '@playwright/test';

test('System is the default and explicit themes persist', async ({ page }) => {
  await page.route('**/api/admin/v1/snapshot', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        api_version: '1',
        revision: 'snapshot-test',
        generated_at: new Date().toISOString(),
        service: { version: 'test', running: true, admin_secure: true },
        caches: [{ cache_ref: 'cache-test', readiness: 'ready', schema_version: 17 }]
      })
    });
  });
  await page.route('**/api/admin/v1/events', async (route) => {
    await route.fulfill({ status: 200, contentType: 'text/event-stream', body: ': ready\n\n' });
  });
  await page.goto('/');
  const system = page.getByRole('radio', { name: 'System' });
  await expect(system).toHaveAttribute('aria-checked', 'true');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'system');

  await page.getByRole('radio', { name: 'Dark' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await page.reload();
  await expect(page.getByRole('radio', { name: 'Dark' })).toHaveAttribute('aria-checked', 'true');

  await system.click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'system');
});
