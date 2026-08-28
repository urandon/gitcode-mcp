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
      documentation: { state: 'partial', revision_set_id: 'repo-doc-set-public', commit_oid: '0123456789abcdef0123456789abcdef01234567', requested_revision: 'HEAD', policy_source: 'committed', policy_hash: 'policy-public-safe', git_store_ref: 'git-store-public', overlay: false, namespace_id: 'embns-public', eligible_files: 4, eligible_chunks: 10, embedded_chunks: 6, reused_chunks: 2, failed_chunks: 1, missing_objects: 1, updated_at: new Date().toISOString(), revision_set_count: 2, search_available: false, index_handoff: 'gitcode-mcp repo-docs index --repo example/repo --detach', search_handoff: 'gitcode-mcp repo-docs search --repo example/repo "QUERY"' },
      recent_sync_events: [{ id: 'sync-1', kind: 'issue', status: 'succeeded', completed_at: new Date().toISOString(), zero_delta: false }]
    }]
  }],
  jobs: [
    { id: 'job-000001', type: 'rag-index', cache_ref: 'cache-111111112222', repo_id: 'example/repo', registration_id: 'reg-1', status: 'running', created_at: new Date(Date.now() - 120000).toISOString(), started_at: new Date(Date.now() - 110000).toISOString(), updated_at: new Date().toISOString(), steps: 80, completed: 40, work_ref: 'work-active', cancellable: true, retryable: false, progress_retained: 2, progress_limit: 256, throughput_per_second: 0.36, eta_seconds: 111, progress: [{ type: 'started', phase: 'running', collection: 'rag-index' }, { type: 'records', phase: 'running', collection: 'rag-index', records_fetched: 40, rate_limit_state: 'ready' }] },
    { id: 'job-000002', type: 'sync', cache_ref: 'cache-111111112222', repo_id: 'example/repo', registration_id: 'reg-1', status: 'failed', created_at: new Date(Date.now() - 240000).toISOString(), updated_at: new Date(Date.now() - 180000).toISOString(), finished_at: new Date(Date.now() - 180000).toISOString(), failure_class: 'provider_unavailable', failure_collection: 'issues', failure_message: 'The provider was unavailable while syncing issues.', retry_after: '30s', inspect_command: 'gitcode-mcp service job job-000002 --format json', remediation_command: 'gitcode-mcp service maintenance --format json', work_ref: 'work-terminal', cancellable: false, retryable: true, progress_retained: 1, progress_limit: 256, progress: [{ type: 'failed', phase: 'failed', collection: 'issues', records_failed: 1, retry_after: '30s', attempt: 2, rate_limit_state: 'waiting' }] },
    { id: 'job-000003', type: 'sync', cache_ref: 'cache-111111112222', repo_id: 'example/repo', registration_id: 'reg-1', status: 'interrupted', created_at: new Date(Date.now() - 360000).toISOString(), updated_at: new Date(Date.now() - 300000).toISOString(), finished_at: new Date(Date.now() - 300000).toISOString(), work_ref: 'work-interrupted', cancellable: false, retryable: true, progress_retained: 1, progress_limit: 256, progress: [{ type: 'interrupted', phase: 'interrupted', collection: 'sync' }] }
  ],
  maintenance: [{ registration_id: 'reg-1', cache_ref: 'cache-111111112222', repo_id: 'example/repo', enabled: true, state: 'retry_scheduled', generation: 4, policy: { sync_enabled: true, sync_mode: 'head-and-backfill', rag_enabled: true, collections: ['issues', 'wiki'], head_max_pages: 3, tail_slice_pages: 10, profile: 'easy-rag' } }],
  diagnostics: [
    { id: 'diag-current', severity: 'warning', entity_type: 'maintenance', entity_id: 'reg-1', failure_class: 'cache_busy', message: 'A cache writer is active.', retryable: true, current: true, remediation: 'Wait for the scheduled retry.' },
    { id: 'diag-recovered', severity: 'warning', entity_type: 'cache', entity_id: 'cache-111111112222', failure_class: 'provider_unavailable', message: 'The provider recovered.', retryable: true, current: false }
  ],
  capabilities: [
    { id: 'rag_search', category: 'rag', safety_class: 'read_only', description: 'Search cached RAG chunks.', ui_enabled: false, ui_reason: 'Search lab is delivered separately.', cli_name: 'rag-search', cli_enabled: true, mcp_name: 'rag_search', mcp_enabled: true },
    { id: 'admin_maintenance_plan_apply', category: 'admin', safety_class: 'background_job', description: 'Plan and apply maintenance.', ui_enabled: true, cli_name: 'maintenance', cli_enabled: true, mcp_enabled: false },
    { id: 'admin_binding_plan_apply', category: 'admin', safety_class: 'audited_write', description: 'Plan and apply bindings.', ui_enabled: true, cli_name: 'repo', cli_enabled: true, mcp_enabled: false },
    { id: 'admin_registration_controls', category: 'admin', safety_class: 'background_job', description: 'Reconcile and disable registrations.', ui_enabled: true, cli_name: 'maintenance', cli_enabled: true, mcp_enabled: false },
    { id: 'admin_search_compare', category: 'admin', safety_class: 'read_only', description: 'Compare search modes.', ui_enabled: true, cli_name: 'search_sources', cli_enabled: true, mcp_enabled: false },
    { id: 'admin_provider_smoke', category: 'admin', safety_class: 'read_only', description: 'Smoke provider.', ui_enabled: true, cli_name: 'rag', cli_enabled: true, mcp_enabled: false },
    { id: 'admin_rag_bounded_repair', category: 'admin', safety_class: 'background_job', description: 'Bounded RAG repair.', ui_enabled: true, cli_name: 'rag', cli_enabled: true, mcp_enabled: false }
  ]
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
  await expect(page.getByRole('heading', { name: 'Search Lab' })).toBeVisible();
  await expect(page.getByText('A query never syncs GitCode, starts provider setup, or repairs an index.')).toBeVisible();
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

test('repository documentation cohort exposes versioned authority, coverage, and safe handoffs', async ({ page }) => {
  await mockAdmin(page);
  await page.goto('/?view=Caches&cache=cache-111111112222&repo=example%2Frepo&tab=documentation');
  await expect(page.getByRole('heading', { name: 'Repository documentation RAG' })).toBeVisible();
  await expect(page.getByText('Committed Git')).toBeVisible();
  await expect(page.getByText('8/10')).toBeVisible();
  await expect(page.getByText('repo-doc-set-public')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Copy index command' })).toBeEnabled();
  await expect(page.getByRole('button', { name: 'Copy search command' })).toBeEnabled();
  await expect(page.getByText('No repository document or absolute filesystem path is exposed by this screen.')).toBeVisible();
  await page.reload();
  await expect(page).toHaveURL(/tab=documentation/);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
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
  await expect(page.getByRole('alert')).toContainText('Receipt delivery was interrupted.');
  await expect(page.getByRole('alert')).toContainText('Retry this confirmation.');
  await page.getByRole('button', { name: 'Confirm retry' }).click();
  await expect(page.getByRole('status')).toContainText('Coalesced');
  await expect(page.getByRole('status')).toContainText('job-000001');
  expect(retryKeys).toHaveLength(2);
  expect(retryKeys[1]).toBe(retryKeys[0]);
});

test('failed jobs are discoverable and expose exact inspect and remediation commands', async ({ page }) => {
  await mockAdmin(page);
  await page.goto('/?view=Jobs');
  await expect(page.getByText('Failed').first()).toBeVisible();
  await expect(page.getByText('1').first()).toBeVisible();
  await page.getByRole('button', { name: 'Show failed' }).click();
  await expect(page).toHaveURL(/job_state=failed/);
  await expect(page.getByText('job-000002')).toBeVisible();
  await page.getByText('job-000002').click();
  await expect(page.getByText('Provider Unavailable · Issues')).toBeVisible();
  await expect(page.getByText('gitcode-mcp service job job-000002 --format json')).toBeVisible();
  await expect(page.getByText('gitcode-mcp service maintenance --format json')).toBeVisible();
});

test('maintenance validation identifies the field and omits an unavailable CLI handoff', async ({ page }) => {
  await mockAdmin(page);
  await page.route('**/api/admin/v1/maintenance/plan', async (route) => {
    await route.fulfill({ status: 422, contentType: 'application/json', body: JSON.stringify({ error: {
      code: 'invalid_policy',
      field: 'head_interval_seconds',
      message: 'head interval must be at least 60 seconds',
      remediation: 'Increase the head interval and render the plan again.',
      blockers: ['head_interval_seconds must be greater than or equal to 60']
    } }) });
  });
  await page.goto('/?view=Maintenance');
  await page.getByRole('button', { name: 'Render plan' }).click();
  const alert = page.getByRole('alert');
  await expect(alert).toContainText('Maintenance control failed · Head Interval Seconds');
  await expect(alert).toContainText('head_interval_seconds must be greater than or equal to 60');
  await expect(alert.locator('code')).toHaveCount(0);
});

test('maintenance plan/apply renders every effect and safely retries one confirmed intent', async ({ page }) => {
  await mockAdmin(page);
  let plannedBody: Record<string, unknown> = {};
  const applyKeys: string[] = [];
  await page.route('**/api/admin/v1/maintenance/plan', async (route) => {
    expect(route.request().headers()['x-csrf-token']).toBe('csrf-test');
    plannedBody = route.request().postDataJSON();
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', result: {
      schema_version: 'gitcode-mcp.maintenance-plan.v1', plan_id: 'maintenance-plan-safe', configuration_hash: 'sha256:cfg', status: 'confirmation_required', repo_id: 'example/repo',
      cache: { cache_ref: 'cache-111111112222', path_fingerprint: 'sha256:public', location_kind: 'managed', schema_version: 17, scopes: ['issues', 'wiki'] },
      provider: { profile: 'easy-rag', provider: 'ollama', provider_type: 'local', model: 'nomic-embed-text', data_boundary: 'local_network', installed: true, running: true, model_available: true, embedding_smoke_status: 'ready' },
      policy: { sync_enabled: true, sync_mode: 'head-and-backfill', rag_enabled: true }, blockers: [], next_action: 'apply the plan with an idempotency key',
      actions: [
        { id: 'validate-cache', class: 'inspect', status: 'complete', summary: 'validate cache identity, schema, and repository binding' },
        { id: 'enroll-cache', class: 'local_config_write', status: 'required', summary: 'enroll the validated cache and maintenance policy', confirmation_required: true },
        { id: 'enqueue-initial-maintenance', class: 'job_enqueue', status: 'required', summary: 'coalesce initial maintenance work', confirmation_required: true }
      ]
    } }) });
  });
  await page.route('**/api/admin/v1/maintenance/apply', async (route) => {
    const body = route.request().postDataJSON();
    applyKeys.push(body.idempotency_key);
    expect(body.plan_id).toBe('maintenance-plan-safe');
    if (applyKeys.length === 1) {
      await route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'temporarily_unavailable', message: 'Receipt delivery was interrupted.', remediation: 'Retry this confirmation.' } }) });
      return;
    }
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', result: { status: 'enabled', plan_id: 'maintenance-plan-safe', repo_id: 'example/repo', jobs_started: ['job-000004'], audit_receipt: 'audit-1' } }) });
  });
  await page.goto('/?view=Maintenance');
  await expect(page.getByRole('heading', { name: 'Maintenance policy' })).toBeVisible();
  await page.getByLabel('RAG mode').selectOption('maintain');
  await page.getByRole('button', { name: 'Render plan' }).click();
  expect(plannedBody.cache_ref).toBe('cache-111111112222');
  expect(plannedBody).not.toHaveProperty('cache_path');
  await expect(page.getByText('enroll the validated cache and maintenance policy')).toBeVisible();
  if (qaOutput) {
    await mkdir(qaOutput, { recursive: true });
    await page.setViewportSize({ width: 1487, height: 1058 });
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.screenshot({ path: path.join(qaOutput, 'maintenance-plan-viewport.png') });
    await page.screenshot({ path: path.join(qaOutput, 'maintenance-plan.png'), fullPage: true });
    if (qaReference) {
      const reference = (await readFile(qaReference)).toString('base64');
      const implementation = (await readFile(path.join(qaOutput, 'maintenance-plan-viewport.png'))).toString('base64');
      const comparePage = await page.context().newPage();
      await comparePage.setViewportSize({ width: 2974, height: 1058 });
      await comparePage.setContent(`<!doctype html><style>html,body{margin:0;background:#111}main{display:flex;width:2974px;height:1058px}figure{position:relative;margin:0;width:1487px;height:1058px;overflow:hidden;background:#f7f6f2}img{display:block;width:1487px;height:1058px;object-fit:cover;object-position:top}figcaption{position:absolute;top:12px;left:12px;padding:7px 10px;border-radius:5px;background:rgba(20,25,24,.86);color:#fff;font:600 13px system-ui}</style><main><figure><img src="data:image/png;base64,${reference}"><figcaption>Selected direction</figcaption></figure><figure><img src="data:image/png;base64,${implementation}"><figcaption>Maintenance plan</figcaption></figure></main>`);
      await comparePage.screenshot({ path: path.join(qaOutput, 'maintenance-plan-comparison.png'), fullPage: true });
      await comparePage.close();
    }
  }
  await page.getByRole('button', { name: 'Confirm & apply' }).first().click();
  await expect(page.getByRole('dialog')).toContainText('Apply this maintenance plan?');
  await page.getByRole('button', { name: 'Confirm action' }).click();
  await expect(page.getByRole('alert')).toContainText('Receipt delivery was interrupted.');
  await expect(page.getByRole('alert')).toContainText('Retry this confirmation.');
  await page.getByRole('button', { name: 'Confirm action' }).click();
  await expect(page.getByRole('status')).toContainText('audit-1');
  expect(applyKeys).toHaveLength(2);
  expect(applyKeys[1]).toBe(applyKeys[0]);
});

test('binding control defaults API in the plan and stale apply stays non-mutating', async ({ page }) => {
  await mockAdmin(page);
  let planBody: Record<string, unknown> = {};
  await page.route('**/api/admin/v1/bindings/plan', async (route) => {
    planBody = route.request().postDataJSON();
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', result: {
      schema_version: 'gitcode-mcp.binding-plan.v1', plan_id: 'binding-plan-safe', status: 'ready', cache_ref: 'cache-111111112222', repo_id: 'example/repo', action: 'no_op', blockers: [],
      binding: { repo_id: 'example/repo', owner: 'example', name: 'repo', api_base_url: 'https://api.gitcode.com/api/v5', scopes: ['issues', 'wiki'], aliases: [], display_name: 'Example repository' },
      effects: [{ id: 'validate-binding', class: 'inspect', status: 'complete', summary: 'validate repository identity, API route, scopes, and aliases' }, { id: 'write-binding', class: 'cache_write', status: 'complete', summary: 'binding already matches the requested intent' }, { id: 'gitcode-network', class: 'network_read', status: 'not_performed', summary: 'no GitCode request is performed while planning or applying the binding' }]
    } }) });
  });
  let applyCalls = 0;
  await page.route('**/api/admin/v1/bindings/apply', async (route) => {
    applyCalls++;
    await route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: { code: 'stale_plan', message: 'The reviewed binding plan no longer matches current cache state.', remediation: 'Render and confirm a new binding plan.' } }) });
  });
  await page.goto('/?view=Maintenance');
  await page.getByRole('button', { name: /example\/repo/ }).last().click();
  await page.getByRole('button', { name: 'Render binding plan' }).click();
  expect(planBody.api_base_url).toBe('');
  expect(planBody).not.toHaveProperty('cache_path');
  await expect(page.getByText('https://api.gitcode.com/api/v5')).toBeVisible();
  await expect(page.getByText('zero GitCode requests')).toBeVisible();
  await page.getByRole('button', { name: 'Confirm & apply' }).last().click();
  await page.getByRole('button', { name: 'Confirm action' }).click();
  await expect(page.getByRole('alert')).toContainText('Render and confirm a new binding plan.');
  expect(applyCalls).toBe(1);
});

test('Search Lab explains hybrid fallback and applies one bounded repair intent', async ({ page }) => {
  await mockAdmin(page);
  let compareBody: Record<string, unknown> = {};
  await page.route('**/api/admin/v1/search/compare', async (route) => {
    compareBody = route.request().postDataJSON();
    const result = { repo_id: 'example/repo', id: 'ISSUE-77', path: 'issues/77.md', title: 'Hybrid search policy', kind: 'issue', status: 'open', provenance: 'live', snippet: 'Hybrid search falls back explicitly when the provider is down.', line_start: 10, line_end: 12, score: 0.032, rank: 1, match: { lexical_rank: 1, semantic_rank: 2, lexical_score: 4.2, semantic_score: 0.81, exact_match: false, fusion_score: 0.032 }, citations: [{ chunk_id: 'chunk-semantic-evidence', line_start: 10, line_end: 12, snippet: 'Semantic evidence with a current chunk hash.' }] };
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', result: { schema_version: 'gitcode-mcp.admin-search-comparison.v1', cache_ref: 'cache-111111112222', repo_id: 'example/repo', query: 'daemon lifecycle', generated_at: new Date().toISOString(), full_text: { requested_mode: 'full_text', effective_mode: 'full_text', rag_state: 'not_requested', coverage: {}, repair: { state: 'not_needed' }, results: [{ ...result, match: { ...result.match, semantic_rank: 0, semantic_score: 0 }, citations: [] }] }, hybrid: { requested_mode: 'hybrid', effective_mode: 'full_text', rag_state: 'unavailable', fallback_reason: 'provider_unavailable', coverage: { eligible_chunks: 20, embedded_chunks: 17, missing_chunks: 2, stale_chunks: 1, failed_chunks: 1, ratio: .85, namespace_id: 'embns-public', content_generation: 9, covered_generation: 8 }, repair: { state: 'needed' }, results: [result] } } }) });
  });
  await page.route('**/api/admin/v1/rag/provider/smoke', (route) => route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', result: { status: 'unavailable', profile_id: 'easy-rag', provider_id: 'ollama', model: 'qwen3-embedding:0.6b', failure_class: 'unavailable', message: 'The configured embedding provider is unavailable.', handoff: 'gitcode-mcp rag setup --yes' } }) }));
  await page.route('**/api/admin/v1/rag/repair/plan', (route) => route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', result: { schema_version: 'gitcode-mcp.admin-rag-repair-plan.v1', plan_id: 'rag-repair-plan-safe', status: 'ready', cache_ref: 'cache-111111112222', repo_id: 'example/repo', profile: 'easy-rag', max_chunks: 64, provider: { status: 'ready', provider_id: 'ollama', model: 'qwen3-embedding:0.6b' }, namespace_id: 'embns-public', coverage: { eligible_chunks: 20, embedded_chunks: 17, missing_chunks: 2, stale_chunks: 1, failed_chunks: 1 }, effects: [{ id: 'inspect', class: 'inspect', status: 'complete', summary: 'inspect current chunk hashes, namespace, and generation coverage' }, { id: 'enqueue', class: 'job_enqueue', status: 'required', summary: 'enqueue at most 64 missing or stale chunks', confirmation_required: true }, { id: 'embed', class: 'provider_request', status: 'required', summary: 'send only the selected bounded cached-text slice to the configured embedding provider', data_boundary: 'configured_embedding_provider', confirmation_required: true }, { id: 'gitcode', class: 'network_read', status: 'not_performed', summary: 'no GitCode request is performed' }] } }) }));
  const repairKeys: string[] = [];
  await page.route('**/api/admin/v1/rag/repair/apply', async (route) => {
    const body = route.request().postDataJSON(); repairKeys.push(body.idempotency_key);
    expect(body.max_chunks).toBe(64); expect(body).not.toHaveProperty('cache_path');
    if (repairKeys.length === 1) return route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { message: 'Receipt delivery was interrupted.', remediation: 'Retry this confirmation.' } }) });
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', result: { outcome: 'created', receipt_id: 'receipt-rag', job_id: 'job-rag', replayed: false } }) });
  });
  await page.goto('/?view=Caches&cache=cache-111111112222&repo=example%2Frepo&tab=search&q=daemon%20lifecycle');
  await page.getByRole('button', { name: 'Compare modes' }).click();
  expect(compareBody.query).toBe('daemon lifecycle'); expect(compareBody).not.toHaveProperty('cache_path');
  await expect(page.getByText('Provider Unavailable').first()).toBeVisible();
  await expect(page.getByText('4.2000 · #1').first()).toBeVisible();
  await expect(page.getByText('0.8100 · #2')).toBeVisible();
  await page.getByText('1 semantic citation').click();
  await expect(page.getByText('Semantic evidence with a current chunk hash.')).toBeVisible();
  await page.getByRole('button', { name: 'Smoke provider' }).click();
  await expect(page.getByText('gitcode-mcp rag setup --yes')).toBeVisible();
  await page.getByLabel('Repair cap').fill('64');
  await page.getByRole('button', { name: 'Plan repair' }).click();
  await expect(page.getByText('enqueue at most 64 missing or stale chunks')).toBeVisible();
  await page.getByLabel('Repair cap').fill('65');
  await expect(page.getByText('enqueue at most 64 missing or stale chunks')).toBeHidden();
  await page.getByLabel('Repair cap').fill('64');
  await page.getByRole('button', { name: 'Plan repair' }).click();
  await page.getByRole('button', { name: 'Confirm bounded repair' }).click();
  await expect(page.getByRole('dialog')).toContainText('Repair at most 64 chunks?');
  await page.getByRole('button', { name: 'Confirm bounded repair' }).last().click();
  await expect(page.getByRole('alert')).toContainText('Retry this confirmation.');
  await page.getByRole('button', { name: 'Confirm bounded repair' }).last().click();
  await expect(page.getByText('Receipt receipt-rag')).toBeVisible();
  expect(repairKeys).toHaveLength(2); expect(repairKeys[1]).toBe(repairKeys[0]);
});
