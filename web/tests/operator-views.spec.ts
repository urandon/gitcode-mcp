import { expect, test, type Page } from '@playwright/test';
import { mkdir, readFile } from 'node:fs/promises';
import path from 'node:path';

const qaOutput = process.env.ADMIN_VIEW_QA_OUTPUT;
const qaReference = process.env.ADMIN_QA_REFERENCE;

const snapshot = {
  api_version: '1', revision: 'snapshot-operator', generated_at: new Date().toISOString(),
  service: { version: 'test', protocol: 'admin.v1', running: true, installed: true, install_kind: 'user', admin_secure: true },
  attention: [{ id: 'attention-1', severity: 'warning', entity_type: 'maintenance', entity_id: 'reg-1', code: 'cache_busy', message: 'RAG retry is waiting for the cache writer.', remediation: 'Wait for the scheduled retry.' }],
  caches: [{ cache_ref: 'cache-111111112222', path_fingerprint: 'sha256:public', storage_mode: 'managed', readiness: 'ready', schema_version: 17, wal_capable: true, journal_mode: 'wal', record_count: 42, chunk_count: 80, repository_count: 1,
    repositories: [{ repo_id: 'example/repo', display_name: 'Example repository', aliases: [], scopes: ['issues', 'wiki'], binding_state: 'bound',
      counts: { records: 42, comments: 8, chunks: 80, by_kind: [{ kind: 'issue', count: 30 }, { kind: 'wiki', count: 12 }], secondary: { pending: 2, deferred: 1, complete: 5, total: 8 } },
      coverage: {
        head: { state: 'current', status: 'fresh', records_listed: 42, updated_at: new Date().toISOString() },
        tail: { state: 'partial', status: 'partial', stop_reason: 'max_pages', pages_listed: 3 },
        secondary: { state: 'partial', status: 'deferred', missing: 3 },
        projection: { state: 'current', status: 'current', current_generation: 8, covered_generation: 8 },
        rag: { state: 'current', status: 'ready', current_generation: 8, covered_generation: 8, eligible: 80, embedded: 80 }
      },
      execution: { active_job_ids: ['job-000001'], contention: { state: 'waiting', operation: 'rag' }, scheduled_retry: { stage: 'rag', at: new Date(Date.now() + 60000).toISOString() }, last_stage_errors: [{ stage: 'rag', failure_class: 'cache_busy', message: 'RAG maintenance recorded cache_busy.' }] },
      collections: [{ kind: 'issue', count: 30, head: { state: 'current', status: 'fresh' }, tail: { state: 'partial', status: 'backfilling', stop_reason: 'max_pages' } }, { kind: 'wiki', count: 12, head: { state: 'current', status: 'fresh' }, tail: { state: 'current', status: 'complete' } }],
      recent_sync_events: [{ id: 'sync-1', kind: 'issue', status: 'succeeded', completed_at: new Date().toISOString(), zero_delta: false }]
    }]
  }],
  jobs: [
    { id: 'job-000001', type: 'rag-index', cache_ref: 'cache-111111112222', repo_id: 'example/repo', registration_id: 'reg-1', status: 'running', created_at: new Date(Date.now() - 120000).toISOString(), started_at: new Date(Date.now() - 110000).toISOString(), updated_at: new Date().toISOString(), steps: 80, completed: 40, work_ref: 'work-active', cancellable: true, retryable: false, progress_retained: 2, progress_limit: 256, throughput_per_second: 0.36, eta_seconds: 111, progress: [{ type: 'started', phase: 'running', collection: 'rag-index' }, { type: 'records', phase: 'running', collection: 'rag-index', records_fetched: 40, rate_limit_state: 'ready' }] },
    { id: 'job-000002', type: 'sync', cache_ref: 'cache-111111112222', repo_id: 'example/repo', registration_id: 'reg-1', status: 'failed', created_at: new Date(Date.now() - 240000).toISOString(), updated_at: new Date(Date.now() - 180000).toISOString(), finished_at: new Date(Date.now() - 180000).toISOString(), failure_class: 'provider_unavailable', failure_message: 'The provider was unavailable.', work_ref: 'work-terminal', cancellable: false, retryable: true, progress_retained: 1, progress_limit: 256, progress: [{ type: 'failed', phase: 'failed', collection: 'issues', records_failed: 1, retry_after: '30s', attempt: 2, rate_limit_state: 'waiting' }] },
    { id: 'job-000003', type: 'sync', cache_ref: 'cache-111111112222', repo_id: 'example/repo', registration_id: 'reg-1', status: 'interrupted', created_at: new Date(Date.now() - 360000).toISOString(), updated_at: new Date(Date.now() - 300000).toISOString(), finished_at: new Date(Date.now() - 300000).toISOString(), work_ref: 'work-interrupted', cancellable: false, retryable: true, progress_retained: 1, progress_limit: 256, progress: [{ type: 'interrupted', phase: 'interrupted', collection: 'sync' }] }
  ],
  maintenance: [{ registration_id: 'reg-1', cache_ref: 'cache-111111112222', repo_id: 'example/repo', enabled: true, state: 'retry_scheduled', generation: 4, policy: { sync_enabled: true, sync_mode: 'head-and-backfill', rag_enabled: true, collections: ['issues', 'wiki'], head_max_pages: 3, tail_slice_pages: 10, profile: 'easy-rag' } }],
  diagnostics: [
    { id: 'diag-current', severity: 'warning', entity_type: 'maintenance', entity_id: 'reg-1', failure_class: 'cache_busy', message: 'A cache writer is active.', retryable: true, current: true, remediation: 'Wait for the scheduled retry.' },
    { id: 'diag-recovered', severity: 'warning', entity_type: 'cache', entity_id: 'cache-111111112222', failure_class: 'provider_unavailable', message: 'The provider recovered.', retryable: true, current: false }
  ],
  capabilities: [{ id: 'rag_search', category: 'rag', safety_class: 'read_only', description: 'Search cached RAG chunks.', ui_enabled: false, ui_reason: 'Search lab is delivered separately.', cli_name: 'rag-search', cli_enabled: true, mcp_name: 'rag_search', mcp_enabled: true }]
};

async function mockAdmin(page: Page, value = snapshot) {
  await page.route('**/api/admin/v1/session', (route) => route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', csrf_token: 'csrf-test' }) }));
  await page.route('**/api/admin/v1/snapshot', (route) => route.fulfill({ contentType: 'application/json', body: JSON.stringify(value) }));
  await page.route('**/api/admin/v1/events', (route) => route.fulfill({ status: 200, contentType: 'text/event-stream', body: ': ready\n\n' }));
}

test('operator views keep coverage truth, deep links, and recovery states', async ({ page }) => {
  await mockAdmin(page);
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Attention queue' })).toBeVisible();
  await expect(page.getByText('RAG retry is waiting for the cache writer.')).toBeVisible();
  if (qaOutput) {
    await mkdir(qaOutput, { recursive: true });
    await page.setViewportSize({ width: 1487, height: 1058 });
    await page.screenshot({ path: path.join(qaOutput, 'operator-overview.png'), fullPage: true });
  }

  await page.getByRole('button', { name: 'Caches' }).click();
  await expect(page).toHaveURL(/view=Caches/);
  await page.getByRole('button', { name: /Example repository/ }).click();
  await expect(page.getByRole('heading', { name: 'Corpus coverage' })).toBeVisible();
  await expect(page.getByText('Partial · stopped by Max Pages')).toBeVisible();
  await expect(page.getByText('Last stage error is historical context')).toBeVisible();
  if (qaOutput) await page.screenshot({ path: path.join(qaOutput, 'repository-coverage.png'), fullPage: true });

  await page.getByRole('tab', { name: 'Collections' }).click();
  await expect(page).toHaveURL(/tab=collections/);
  await expect(page.getByRole('table', { name: 'Cached collection counts and coverage' })).toBeVisible();
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Collections and frontiers' })).toBeVisible();

  await page.getByRole('tab', { name: 'Search status' }).click();
  await expect(page.getByText('Hybrid search ready')).toBeVisible();
  await page.getByRole('tab', { name: 'Activity' }).click();
  await expect(page.getByText('Recent sync events')).toBeVisible();

  await page.getByRole('button', { name: 'Diagnostics' }).click();
  await expect(page.getByText('A cache writer is active.')).toBeVisible();
  await page.getByRole('button', { name: 'Recovered' }).click();
  await expect(page.getByText('The provider recovered.')).toBeVisible();
  await expect(page).toHaveURL(/diagnostics=recovered/);

  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await expect(page.getByRole('button', { name: 'Overview' })).toBeVisible();
  if (qaOutput) await page.screenshot({ path: path.join(qaOutput, 'diagnostics-narrow.png'), fullPage: true });

  if (qaOutput && qaReference) {
    const reference = (await readFile(qaReference)).toString('base64');
    const implementation = (await readFile(path.join(qaOutput, 'operator-overview.png'))).toString('base64');
    const comparePage = await page.context().newPage();
    await comparePage.setViewportSize({ width: 2974, height: 1058 });
    await comparePage.setContent(`<!doctype html><style>html,body{margin:0;background:#111}main{display:flex;width:2974px;height:1058px}figure{position:relative;margin:0;width:1487px;height:1058px;overflow:hidden}img{display:block;width:1487px;height:1058px;object-fit:cover;object-position:top}figcaption{position:absolute;top:12px;left:12px;padding:7px 10px;border-radius:5px;background:rgba(20,25,24,.86);color:#fff;font:600 13px system-ui}</style><main><figure><img src="data:image/png;base64,${reference}"><figcaption>Selected direction</figcaption></figure><figure><img src="data:image/png;base64,${implementation}"><figcaption>Operator overview</figcaption></figure></main>`);
    await comparePage.screenshot({ path: path.join(qaOutput, 'operator-overview-comparison.png'), fullPage: true });
    await comparePage.close();
  }
});

test('API version mismatch is explicit and blocks ordinary views', async ({ page }) => {
  await mockAdmin(page, { ...snapshot, api_version: '2' });
  await page.goto('/');
  await expect(page.getByRole('alert')).toContainText('UI/API version mismatch');
  await expect(page.getByRole('heading', { name: 'Attention queue' })).toHaveCount(0);
});

test('loading and empty cache estate have explicit states', async ({ page }) => {
  let releaseSnapshot!: () => void;
  const snapshotGate = new Promise<void>((resolve) => { releaseSnapshot = resolve; });
  await page.route('**/api/admin/v1/snapshot', async (route) => {
    await snapshotGate;
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ ...snapshot, revision: 'snapshot-empty', attention: [], caches: [], jobs: [], maintenance: [], diagnostics: [], capabilities: [] }) });
  });
  await page.route('**/api/admin/v1/events', (route) => route.fulfill({ status: 200, contentType: 'text/event-stream', body: ': ready\n\n' }));
  await page.route('**/api/admin/v1/session', (route) => route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', csrf_token: 'csrf-test' }) }));
  const navigation = page.goto('/');
  await expect(page.locator('.loading-grid')).toBeVisible();
  releaseSnapshot();
  await navigation;
  await expect(page.getByRole('heading', { name: 'No managed caches' })).toBeVisible();
});

test('job detail deep links and cancel uses explicit CSRF-bound confirmation', async ({ page }) => {
  await mockAdmin(page);
  let actionBody: Record<string, string> = {};
  await page.route('**/api/admin/v1/jobs/job-000001/cancel', async (route) => {
    expect(route.request().headers()['x-csrf-token']).toBe('csrf-test');
    actionBody = route.request().postDataJSON();
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', action: 'cancel', receipt: { receipt_id: 'receipt-cancel', action: 'cancel', target_job_id: 'job-000001', result_job_id: 'job-000001', outcome: 'cancellation_requested', job_status: 'running', replayed: false, created_at: new Date().toISOString() } }) });
  });
  await page.goto('/?view=Jobs&job=job-000001');
  await expect(page.getByRole('heading', { name: 'Rag Index' })).toBeVisible();
  await expect(page.getByText('work-active')).toBeVisible();
  if (qaOutput) {
    await mkdir(qaOutput, { recursive: true });
    await page.setViewportSize({ width: 1487, height: 1058 });
    await page.screenshot({ path: path.join(qaOutput, 'job-detail.png'), fullPage: true });
    if (qaReference) {
      const reference = (await readFile(qaReference)).toString('base64');
      const implementation = (await readFile(path.join(qaOutput, 'job-detail.png'))).toString('base64');
      const comparePage = await page.context().newPage();
      await comparePage.setViewportSize({ width: 2974, height: 1058 });
      await comparePage.setContent(`<!doctype html><style>html,body{margin:0;background:#111}main{display:flex;width:2974px;height:1058px}figure{position:relative;margin:0;width:1487px;height:1058px;overflow:hidden}img{display:block;width:1487px;height:1058px;object-fit:cover;object-position:top}figcaption{position:absolute;top:12px;left:12px;padding:7px 10px;border-radius:5px;background:rgba(20,25,24,.86);color:#fff;font:600 13px system-ui}</style><main><figure><img src="data:image/png;base64,${reference}"><figcaption>Selected direction</figcaption></figure><figure><img src="data:image/png;base64,${implementation}"><figcaption>Job supervision detail</figcaption></figure></main>`);
      await comparePage.screenshot({ path: path.join(qaOutput, 'job-detail-comparison.png'), fullPage: true });
      await comparePage.close();
    }
  }
  await page.reload();
  await expect(page).toHaveURL(/job=job-000001/);
  await page.getByRole('button', { name: 'Cancel job' }).click();
  await expect(page.getByRole('dialog')).toContainText('Cancel active work?');
  if (qaOutput) await page.screenshot({ path: path.join(qaOutput, 'job-cancel-confirmation.png'), fullPage: true });
  await page.getByRole('button', { name: 'Confirm cancel' }).click();
  await expect(page.getByRole('status')).toContainText('Cancellation Requested');
  expect(actionBody.idempotency_key).toMatch(/^admin-cancel-/);
});

test('retry coalescing, filters, interruption, and structured wait state are observable', async ({ page }) => {
  await mockAdmin(page);
  const retryKeys: string[] = [];
  await page.route('**/api/admin/v1/jobs/job-000002/retry', async (route) => {
    expect(route.request().headers()['x-csrf-token']).toBe('csrf-test');
    retryKeys.push(route.request().postDataJSON().idempotency_key);
    if (retryKeys.length === 1) {
      await route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'temporarily_unavailable', message: 'Receipt delivery was interrupted.', remediation: 'Retry this confirmation.' } }) });
      return;
    }
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', action: 'retry', receipt: { receipt_id: 'receipt-retry', action: 'retry', target_job_id: 'job-000002', result_job_id: 'job-000001', outcome: 'coalesced', job_status: 'running', replayed: false, created_at: new Date().toISOString() } }) });
  });
  await page.goto('/?view=Jobs');
  await page.getByLabel('State').selectOption('interrupted');
  await expect(page.getByText('job-000003')).toBeVisible();
  await expect(page.getByText('job-000001')).toHaveCount(0);
  await expect(page).toHaveURL(/job_state=interrupted/);
  await page.goto('/?view=Jobs&job=job-000002');
  await expect(page.getByText('Rate limit: Waiting · retry after 30s')).toBeVisible();
  await page.getByRole('button', { name: 'Retry job' }).click();
  await expect(page.getByRole('dialog')).toContainText('Equivalent active work will be coalesced');
  await page.getByRole('button', { name: 'Confirm retry' }).click();
  await expect(page.getByRole('alert')).toContainText('Receipt delivery was interrupted. Retry this confirmation.');
  await page.getByRole('button', { name: 'Confirm retry' }).click();
  await expect(page.getByRole('status')).toContainText('Coalesced');
  await expect(page.getByRole('status')).toContainText('job-000001');
  expect(retryKeys).toHaveLength(2);
  expect(retryKeys[1]).toBe(retryKeys[0]);
});
