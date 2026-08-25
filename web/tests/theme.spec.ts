import { expect, test } from '@playwright/test';

test('System is the default and explicit themes persist', async ({ page }) => {
  await page.route('**/api/admin/v1/readiness', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        api_version: '1',
        version: 'test',
        daemon_running: true,
        session_secure: true,
        cache_connected: true,
        cache_reference: 'local cache',
        schema_version: 17,
        checked_at: new Date().toISOString()
      })
    });
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
