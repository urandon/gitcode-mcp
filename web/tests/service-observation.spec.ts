import { expect, test, type Page } from '@playwright/test';
import { emptySnapshot } from '../src/lib/admin';
import { id, observationFixture, ref } from './fixtures/observation';

async function mockObservation(page: Page, fixture = observationFixture(), snapshot: any = structuredClone(emptySnapshot)) {
  const requests: string[] = []; let snapshotReads = 0;
  await page.addInitScript(() => {
    const listeners: Record<string, Array<() => void>> = {};
    (window as any).__emitObservation = (kind: string) => listeners[kind]?.forEach(f => f());
    (window as any).__copiedObservation = '';
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async (value: string) => { (window as any).__copiedObservation = value; } } });
    class FakeEventSource {
      constructor() { setTimeout(() => listeners.open?.forEach(f => f()), 0); }
      addEventListener(kind: string, listener: () => void) { (listeners[kind] ||= []).push(listener); }
      close() { for (const key of Object.keys(listeners)) delete listeners[key]; }
    }
    (window as any).EventSource = FakeEventSource;
  });
  await page.route('**/api/admin/v1/**', async route => {
    const request = route.request(); const url = new URL(request.url()); requests.push(`${request.method()} ${url.pathname}${url.search}`);
    if (url.pathname.endsWith('/session')) return route.fulfill({ json: { api_version: '1', csrf_token: 'csrf-public-fixture' } });
    if (url.pathname.endsWith('/snapshot')) { snapshotReads++; return route.fulfill({ json: snapshot }); }
    if (url.pathname.endsWith('/observability')) return route.fulfill({ json: fixture.summary });
    if (url.pathname.endsWith('/observability/metrics')) return route.fulfill({ json: fixture.metrics });
    if (url.pathname.endsWith('/observability/events')) return route.fulfill({ json: fixture.page });
    return route.fulfill({ status: 404, json: { error: { message: 'synthetic-private-provider-body' } } });
  });
  return { fixture, requests, snapshotReads: () => snapshotReads };
}
const emit = (page: Page, kind = 'observability_changed') => page.evaluate(k => (window as any).__emitObservation(k), kind);

test('typed event rows distinguish neutral-info failures and preserve maintenance predecessor copy', async ({ page }) => {
  const fixture = observationFixture();
  fixture.summary.event_templates!.job_terminal = { stream: 'stdout', severity: 'info', component: 'jobs', message: 'A job reached a confirmed terminal state.' };
  fixture.summary.event_templates!.maintenance_state_changed = { stream: 'stdout', severity: 'info', component: 'maintenance', message: 'Maintenance reached a confirmed state transition.' };
  fixture.summary.event_filters!.phase = ['completion', 'maintenance']; fixture.summary.event_filters!.failure_class = ['timeout']; fixture.summary.event_filters!.outcome = ['failed', 'succeeded', 'ready'];
  const base = { schema: 1, event_id: id('e'), boot_id: id('a'), occurred_at: fixture.summary.observed_at, sequence: 1, code: 'job_terminal', stream: 'stdout', severity: 'info', component: 'jobs', message: 'A job reached a confirmed terminal state.', phase: 'completion', job_ref: ref('job', '5'), correlation_ref: ref('correlation', '7') };
  fixture.page.events = [{ ...base, outcome: 'failed', failure_class: 'timeout' }, { ...base, event_id: id('f'), sequence: 2, outcome: 'succeeded' }, { schema: 1, event_id: id('1'), boot_id: id('a'), occurred_at: fixture.summary.observed_at, sequence: 3, code: 'maintenance_state_changed', stream: 'stdout', severity: 'info', component: 'maintenance', message: 'Maintenance reached a confirmed state transition.', phase: 'maintenance', outcome: 'ready', maintenance_scope: 'identity_resolution', previous_state: 'identity_conflict', previous_failure_class: 'identity', registration_ref: ref('registration', '2'), previous_registration_ref: ref('registration', '6'), cache_ref: ref('cache', '3'), repo_ref: ref('repo', '4'), correlation_ref: ref('correlation', '8'), raw_body: 'synthetic-private-secret' }];
  await mockObservation(page, fixture); await page.goto('/?view=Service');
  const rows = page.getByRole('list', { name: 'Retained catalog events' }).getByRole('listitem');
  await expect(rows.nth(0)).toContainText('Outcome'); await expect(rows.nth(0)).toContainText('Failed'); await expect(rows.nth(0)).toContainText('Failure Class'); await expect(rows.nth(0)).toContainText('Timeout');
  await expect(rows.nth(1)).toContainText('Succeeded'); await expect(rows.nth(1)).not.toContainText('Timeout');
  await expect(rows.nth(2)).toContainText('Identity Resolution'); await expect(rows.nth(2)).toContainText('Identity Conflict'); await expect(rows.nth(2)).toContainText('Previous Failure Class');
  await page.getByRole('button', { name: 'Maintenance State Changed', exact: true }).click(); await page.getByRole('button', { name: 'Copy safe context' }).click();
  const copy = JSON.parse(await page.evaluate(() => (window as any).__copiedObservation));
  expect(copy.event.maintenance_scope).toBe('identity_resolution'); expect(copy.event.previous_state).toBe('identity_conflict'); expect(copy.event.previous_failure_class).toBe('identity'); expect(copy.event.previous_registration_ref).toBe(ref('registration', '6'));
  expect(JSON.stringify(copy)).not.toContain('synthetic-private-secret'); expect(await page.locator('body').innerText()).not.toContain('synthetic-private-secret');
});

test('resource freshness belongs to summary sample even when metrics are fresh or unavailable', async ({ page }) => {
  const mock = await mockObservation(page); mock.fixture.summary.metrics_sampled_at = new Date(Date.now() - 120000).toISOString();
  await page.goto('/?view=Service'); const resources = page.locator('section[aria-labelledby="resources-title"]');
  await expect(resources.getByText('Samples stale', { exact: true })).toBeVisible();
  await expect(page.locator('section[aria-labelledby="metrics-title"]').getByText('Samples current', { exact: true })).toBeVisible();
  mock.fixture.summary.metrics_sampled_at = new Date().toISOString();
  await page.route('**/api/admin/v1/observability/metrics?**', route => route.fulfill({ status: 503, json: { error: { code: 'observation_busy' } } }));
  await emit(page); await expect(page.getByText('Metrics · Observation is busy. Retry the bounded read.')).toBeVisible();
  await expect(resources.getByText('Samples current', { exact: true })).toBeVisible();
  mock.fixture.summary.metrics_sampled_at = '0001-01-01T00:00:00Z'; await emit(page);
  await expect(resources.getByText('Samples missing', { exact: true })).toBeVisible();
});

test('first-page discontinuity requires explicit restart before continuation', async ({ page }) => {
  const mock = await mockObservation(page); mock.fixture.page.has_more = true; mock.fixture.page.cursor = 'e30';
  await page.goto('/?view=Service'); const next = page.getByRole('button', { name: 'Continue retained scan' }); await expect(next).toBeEnabled();
  await emit(page); await expect.poll(() => mock.requests.filter(r => r.includes('/observability/events')).length).toBeGreaterThanOrEqual(2); await expect(next).toBeEnabled();
  mock.fixture.summary.observation_health.loss_epoch = 1; await emit(page);
  await expect(page.getByText('Continuation stopped at a discontinuity. Restart explicitly; no automatic skip was made.')).toBeVisible(); await expect(next).toBeDisabled();
  expect(mock.requests.filter(r => r.includes('/observability/events')).every(r => !r.includes('cursor='))).toBe(true);
  await page.getByRole('button', { name: 'Restart retained timeline' }).click(); await expect(next).toBeEnabled();
});

test('unknown regex-valid URL enums are removed only after delivered catalog and never sent', async ({ page }) => {
  const mock = await mockObservation(page);
  await page.goto(`/?view=Service&obs_code=not_a_catalog_code&obs_phase=not_a_catalog_phase&obs_job_ref=${ref('job', '5')}&obs_cursor=e30`);
  await expect(page.getByText('Invalid URL selectors were discarded. No untrusted selector was sent.')).toBeVisible();
  await expect(page).not.toHaveURL(/not_a_catalog|obs_cursor/); await expect(page).toHaveURL(new RegExp(`obs_job_ref=${ref('job', '5')}`));
  await expect(page.getByRole('button', { name: 'Job Admitted', exact: true })).toBeVisible();
  expect(mock.requests.join('\n')).not.toContain('not_a_catalog'); expect(mock.requests.join('\n')).not.toContain('cursor=e30');
});

test('pre-observation daemon HTML-200 SPA fallback is unsupported and never echoed', async ({ page }) => {
  await mockObservation(page);
  await page.route('**/api/admin/v1/observability**', route => route.fulfill({ status: 200, contentType: 'text/html; charset=utf-8', body: '<!doctype html><html>synthetic-private-old-spa</html>' }));
  await page.goto('/?view=Service');
  await expect(page.getByText('Summary · Unsupported by this daemon. Update browser assets and daemon together.')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Copy safe context' })).toBeDisabled();
  expect(await page.locator('body').innerText()).not.toContain('synthetic-private-old-spa');
});

test('Service opens independently of broad snapshot and distinguishes nullable resources, health, phases and safe copy', async ({ page }) => {
  const mock = await mockObservation(page);
  await page.goto('/?view=Service');
  await expect(page.getByRole('heading', { name: 'Execution and observation' })).toBeVisible();
  expect(mock.snapshotReads()).toBe(0);
  await expect(page.getByText('Missing probe, not zero.')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Repository Docs · Other · Inspection' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Repository Docs · Other · Indexing' })).toBeVisible();
  await page.getByRole('button', { name: 'Job Admitted', exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`obs_event_id=${id('e')}`));
  await page.getByRole('button', { name: 'Copy safe context' }).click();
  await expect(page.getByText('Safe context copied (schema 1, at most 16 KiB).')).toBeVisible();
  const copy = await page.evaluate(() => (window as any).__copiedObservation);
  const parsed = JSON.parse(copy); expect(parsed.event.job_ref).toBe(ref('job', '5')); expect(parsed.observation_state).toBe('partial'); expect(parsed.execution_state).toBe('degraded'); expect(copy).not.toContain('csrf'); expect(copy).not.toContain('synthetic-private');
  expect(mock.requests.filter(r => r.startsWith('POST'))).toEqual([]);
  expect(mock.requests.some(r => /provider|setup|repair|reconcile/.test(r))).toBe(false);
});

test('finite filters and selection preserve browser history without untrusted URL data', async ({ page }) => {
  const mock = await mockObservation(page); await page.goto('/?view=Service&obs_job_ref=private-coordinate&obs_code=token%3Dsecret');
  await expect(page.getByText('Invalid URL selectors were discarded. No untrusted selector was sent.')).toBeVisible();
  await page.getByRole('combobox', { name: 'Code', exact: true }).selectOption('job_admitted');
  await expect(page).toHaveURL(/obs_code=job_admitted/);
  expect(page.url()).not.toContain('private-coordinate');
  await page.getByRole('combobox', { name: 'Severity', exact: true }).selectOption('info');
  await expect(page).toHaveURL(/obs_severity=info/);
  await page.goBack(); await expect(page.getByRole('combobox', { name: 'Severity', exact: true })).toHaveValue('');
  await expect(page.getByRole('combobox', { name: 'Code', exact: true })).toHaveValue('job_admitted');
  await page.getByRole('button', { name: 'Clear filters and selection' }).click(); await expect(page).toHaveURL(/\?view=Service$/);
  expect(mock.requests.join('\n')).not.toContain('private-coordinate');
});

test('sparse scan requires explicit continuation, keeps request cursor on invalidation and stops at loss boundary', async ({ page }) => {
  const mock = await mockObservation(page); mock.fixture.page.events = []; mock.fixture.page.has_more = true; mock.fixture.page.cursor = 'e30';
  await page.goto('/?view=Service'); await expect(page.getByText('No match in this scanned page. More retained records remain to scan.')).toBeVisible();
  expect(mock.requests.filter(r => r.includes('/observability/events')).every(r => !r.includes('cursor='))).toBe(true);
  await page.getByRole('button', { name: 'Continue retained scan' }).click(); await expect(page).toHaveURL(/obs_cursor=e30/);
  await emit(page); await expect.poll(() => mock.requests.filter(r => r.includes('cursor=e30')).length).toBeGreaterThanOrEqual(2);
  mock.fixture.summary.observation_health.loss_epoch = 1;
  await emit(page); await expect(page.getByText('Continuation stopped at a discontinuity. Restart explicitly; no automatic skip was made.')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Continue retained scan' })).toBeDisabled();
  await page.getByRole('button', { name: 'Restart retained timeline' }).click(); await expect(page).not.toHaveURL(/obs_cursor=/);
});

test('expired cursor and not-retained selection are explicit, not an empty success', async ({ page }) => {
  await mockObservation(page);
  await page.route('**/api/admin/v1/observability/events?**', route => new URL(route.request().url()).searchParams.has('cursor') ? route.fulfill({ status: 409, json: { error: { code: 'snapshot_required', reason: 'synthetic-private-provider-body' } } }) : route.fulfill({ json: observationFixture().page }));
  await page.goto('/?view=Service&obs_cursor=e30');
  await expect(page.getByText(/Timeline · Cursor expired/)).toBeVisible(); await expect(page.getByRole('button', { name: 'Copy safe context' })).toBeDisabled();
  await page.getByRole('button', { name: 'Restart retained timeline' }).click(); await expect(page.getByRole('button', { name: 'Job Admitted', exact: true })).toBeVisible();
  await page.goto(`/?view=Service&obs_event_id=${id('f')}`); await expect(page.getByText('Selection not retained. No fallback entity was selected.')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Copy safe context' })).toBeDisabled();
  expect(await page.locator('body').innerText()).not.toContain('synthetic-private-provider-body');
});

test('older daemon is unsupported, while unrelated endpoint failure does not hide execution authority', async ({ page }) => {
  const mock = await mockObservation(page);
  await page.route('**/api/admin/v1/observability/metrics?**', route => route.fulfill({ status: 503, json: { error: { code: 'observation_busy', reason: 'synthetic-private-provider-body' } } }));
  await page.goto('/?view=Service'); await expect(page.getByRole('heading', { name: 'Execution and observation' })).toBeVisible(); await expect(page.getByText('Metrics · Observation is busy. Retry the bounded read.')).toBeVisible();
  delete mock.fixture.summary.admin_observation_version;
  await emit(page); await expect(page.getByText(/Summary · Unsupported by this daemon/)).toBeVisible();
  await expect(page.getByRole('button', { name: 'Copy safe context' })).toBeDisabled();
  expect(mock.snapshotReads()).toBe(0);
});

test('same-selection invalidations coalesce without starving the read and navigation fences late responses', async ({ page }) => {
  const mock = await mockObservation(page); let release: () => void = () => {}; let started = 0; let hold = false;
  await page.route('**/api/admin/v1/observability/events?**', async route => {
    started++; if (hold) { const wait = new Promise<void>(resolve => { release = resolve; }); await wait; }
    await route.fulfill({ json: mock.fixture.page }).catch(() => {});
  });
  await page.goto('/?view=Service'); await expect(page.getByRole('button', { name: 'Job Admitted', exact: true })).toBeVisible();
  hold = true; const baseline = started; await emit(page); await expect.poll(() => started).toBe(baseline + 1);
  await emit(page); await emit(page); await emit(page); expect(started).toBe(baseline + 1);
  hold = false; release(); await expect.poll(() => started).toBe(baseline + 2);
  await expect(page.getByRole('button', { name: 'Refresh observation', exact: true })).toBeEnabled();
  hold = true; await emit(page); await expect.poll(() => started).toBe(baseline + 3);
  await page.getByRole('button', { name: 'Diagnostics', exact: true }).click(); hold = false; release();
  await expect(page.getByRole('heading', { name: 'Diagnostics', exact: true })).toBeVisible(); await expect(page.getByRole('heading', { name: 'Event timeline' })).toHaveCount(0);
});

test('listed job context joins in both directions; missing authority never guesses a locator', async ({ page }) => {
  const snapshot: any = structuredClone(emptySnapshot); snapshot.jobs = [{ id: 'job-public', type: 'sync', status: 'succeeded', created_at: new Date().toISOString(), updated_at: new Date().toISOString(), cancellable: false, retryable: false, progress_retained: 0, progress_limit: 256, observation_refs: { job_ref: ref('job', '5') } }];
  await mockObservation(page, observationFixture(), snapshot);
  await page.goto('/?view=Jobs&job=job-public'); await page.getByRole('button', { name: 'Observe job in Service' }).click(); await expect(page).toHaveURL(new RegExp(`obs_job_ref=${ref('job', '5')}`));
  await page.getByRole('button', { name: 'Job Admitted', exact: true }).click(); await page.getByRole('button', { name: 'Open exact job' }).click(); await expect(page).toHaveURL(/view=Jobs&job=job-public/);
  await page.goto(`/?view=Service&obs_incident_ref=${ref('correlation', 'c')}`); await expect(page.getByText('Entity authority unavailable or ambiguous. No arbitrary locator or sibling was resolved.')).toBeVisible();
});

test('disconnected and stale sample states remain separate from health across narrow themes and keyboard navigation', async ({ page }) => {
  const at = new Date(Date.now() - 120000).toISOString(); const fixture = observationFixture(at); fixture.summary.execution_health.state = 'healthy';
  await mockObservation(page, fixture); await page.setViewportSize({ width: 390, height: 844 }); await page.goto('/?view=Service');
  await expect(page.locator('section[aria-labelledby="resources-title"]').getByText('Samples stale', { exact: true })).toBeVisible();
  await expect(page.locator('section[aria-labelledby="metrics-title"]').getByText('Samples stale', { exact: true })).toBeVisible();
  await emit(page, 'error'); await expect(page.getByText(/Live observation disconnected/)).toBeVisible();
  for (const theme of ['Light', 'Dark', 'System']) { await page.setViewportSize({ width: 1280, height: 844 }); await page.getByRole('radio', { name: theme, exact: true }).click(); await page.setViewportSize({ width: 390, height: 844 }); await expect(page.getByRole('heading', { name: 'Service', exact: true })).toBeVisible(); expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true); }
  await page.getByRole('button', { name: 'Refresh observation', exact: true }).focus(); await expect(page.getByRole('button', { name: 'Refresh observation', exact: true })).toBeFocused(); await page.keyboard.press('Tab'); await expect(page.getByRole('button', { name: 'Copy safe context' })).toBeFocused();
});

test('source-specific round trip keeps the selected source and refuses a disappearing sibling fallback', async ({ page }) => {
  const snapshot: any = structuredClone(emptySnapshot);
  const parentRefs = { cache_ref: ref('cache', '3'), repo_ref: ref('repo', '4'), registration_ref: ref('registration', '2') };
  snapshot.caches = [{ cache_ref: 'cache-public', readiness: 'ready', repository_count: 1, record_count: 0, chunk_count: 0, observation_refs: { cache_ref: parentRefs.cache_ref }, repositories: [{
    repo_id: 'example/repo', binding_state: 'bound', scopes: ['issues'], observation_refs: parentRefs, counts: { records: 0, comments: 0, chunks: 0, secondary: { pending: 0, deferred: 0, complete: 0, total: 0 } }, coverage: {}, collections: [], recent_sync_events: [], execution: {},
    documentation: { state: 'registered', registered: true, overlay: false, eligible_files: 0, eligible_chunks: 0, embedded_chunks: 0, reused_chunks: 0, failed_chunks: 0, missing_objects: 0, excluded_files: 0, revision_set_count: 0, search_available: false, sources: [
      { source_registration_id: 'source-public-a', source_registration_generation: 1, state: 'registered', observation_refs: { ...parentRefs, source_ref: ref('source', '1') } },
      { source_registration_id: 'source-public-b', source_registration_generation: 1, state: 'registered', observation_refs: { ...parentRefs, source_ref: ref('source', '6') } }
    ] }
  }] }];
  snapshot.maintenance = [{ registration_id: 'registration-public', cache_ref: 'cache-public', repo_id: 'example/repo', enabled: true, state: 'ready', generation: 1, policy: { sync_enabled: false, rag_enabled: false, collections: [] }, observation_refs: parentRefs }];
  const mock = await mockObservation(page, observationFixture(), snapshot);
  await page.goto('/?view=Caches&cache=cache-public&repo=example%2Frepo&tab=documentation');
  await page.getByRole('button', { name: 'Observe exact documentation source in Service' }).click();
  await expect(page).toHaveURL(new RegExp(`obs_source_ref=${ref('source', '1')}`));
  await page.getByRole('button', { name: 'Repository Docs · Other · Inspection' }).click();
  await page.getByRole('button', { name: 'Open exact documentation source' }).click();
  await expect(page).toHaveURL(new RegExp(`doc_source_ref=${ref('source', '1')}`));
  await expect(page.getByRole('combobox', { name: 'Git authority' })).toHaveValue('source-public-a');
  snapshot.caches[0].repositories[0].documentation.sources.shift(); await emit(page, 'snapshot_changed');
  await expect(page.getByText('Selected documentation source is unavailable. No sibling fallback was selected.')).toBeVisible();
  await expect(page.getByRole('combobox', { name: 'Git authority' })).toHaveValue('');
  await page.getByRole('button', { name: 'Preview exact plan' }).click();
  await expect(page.getByText('Selected documentation source is unavailable. Select an authority explicitly; no sibling fallback is allowed.')).toBeVisible();
  expect(mock.requests.filter(r => r.startsWith('POST'))).toEqual([]);
});
