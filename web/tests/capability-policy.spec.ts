import { expect, test } from '@playwright/test';
import { observationFixture } from './fixtures/observation';

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

test('allowed Service cohort survives visiting a restricted broad snapshot', async ({ page }) => {
  const policy = { schema: 1, managed: true, fingerprint: 'policy-observer', sources: ['machine'], decisions: ['admin_service_observation', 'service_observability', 'service_metrics', 'service_events'].map((id) => ({ id, allowed: true, reason: 'allowed' })) };
  const fixture = observationFixture();
  const paths: string[] = [];
  await page.addInitScript(() => { (window as any).EventSource = class { addEventListener() {} close() {} }; });
  await page.route('**/api/admin/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname; paths.push(path);
    if (path.endsWith('/session')) return route.fulfill({ json: { csrf_token: 'fixture' } });
    if (path.endsWith('/policy')) return route.fulfill({ json: policy });
    if (path.endsWith('/snapshot')) return route.fulfill({ json: { api_version: '1', revision: 'policy-only', generated_at: new Date().toISOString(), service: { version: 'test', protocol: 'admin.v1', running: true, installed: false, admin_secure: true }, policy, policy_restricted: true } });
    if (path.endsWith('/observability')) return route.fulfill({ json: fixture.summary });
    if (path.endsWith('/observability/metrics')) return route.fulfill({ json: fixture.metrics });
    if (path.endsWith('/observability/events')) return route.fulfill({ json: fixture.page });
    return route.fulfill({ status: 403, json: { code: 'capability_policy_denied' } });
  });
  await page.goto('/?view=Service');
  await expect(page.getByRole('heading', { name: 'Execution and observation', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Overview', exact: true }).click();
  await expect(page.getByRole('status', { name: 'Policy restricted observation' })).toBeVisible();
  await page.getByRole('button', { name: 'Service', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Execution and observation', exact: true })).toBeVisible();
  await expect(page.getByRole('status', { name: 'Policy restricted observation' })).toHaveCount(0);
  expect(paths.every((path) => ['/api/admin/v1/session', '/api/admin/v1/policy', '/api/admin/v1/snapshot', '/api/admin/v1/observability', '/api/admin/v1/observability/metrics', '/api/admin/v1/observability/events'].includes(path))).toBe(true);
});
