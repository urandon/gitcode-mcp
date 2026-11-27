import { expect, test } from '@playwright/test';

test('restricted instance explains withheld data without issuing action or provider reads', async ({ page }) => {
  const policy = { schema: 1, managed: true, profile: 'research', fingerprint: 'sha256:policy-fixture', sources: ['machine', 'deployment'], decisions: [{ id: 'get_source', allowed: true, reason: 'allowed' }, { id: 'merge_pr', allowed: false, reason: 'not_allowlisted' }] };
  const requested: string[] = [];
  await page.route('**/api/admin/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    requested.push(path);
    if (path.endsWith('/session')) return route.fulfill({ json: { csrf_token: 'fixture' } });
    if (path.endsWith('/policy')) return route.fulfill({ json: policy });
    if (path.endsWith('/snapshot')) return route.fulfill({ json: { api_version: '1', revision: 'policy-only', generated_at: new Date().toISOString(), service: { version: 'test', protocol: 'admin.v1', running: true, installed: false, admin_secure: true }, policy, policy_restricted: true } });
    return route.fulfill({ status: 403, json: { code: 'capability_policy_denied' } });
  });
  await page.goto('/?view=Maintenance');
  const card = page.getByRole('region', { name: 'Instance capability policy' });
  await expect(card.getByText('research', { exact: true })).toBeVisible();
  await expect(card.getByText('sha256:policy-fixture')).toBeVisible();
  await card.getByText('Effective grants (1 allowed)').click();
  await expect(card.getByRole('row').filter({ hasText: 'merge_pr' })).toContainText('Denied');
  await expect(page.getByRole('status', { name: 'Policy restricted observation' })).toContainText('not an empty cache or job list');
  await expect(page.getByRole('button', { name: 'Render plan', exact: true })).toHaveCount(0);
  await expect(card.getByRole('combobox')).toHaveCount(0);
  await page.getByRole('button', { name: 'Service', exact: true }).click();
  await expect(page.getByRole('status', { name: 'Policy restricted observation' })).toBeVisible();
  await page.getByRole('button', { name: 'Local projects', exact: true }).click();
  await expect(page.getByRole('status', { name: 'Policy restricted observation' })).toBeVisible();
  expect(requested.every((path) => ['/api/admin/v1/session', '/api/admin/v1/policy', '/api/admin/v1/snapshot'].includes(path))).toBe(true);
});
