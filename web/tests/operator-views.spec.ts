import { expect, test, type Page } from '@playwright/test';
import { mkdir, readFile } from 'node:fs/promises';
import path from 'node:path';

const qaOutput = process.env.ADMIN_VIEW_QA_OUTPUT;
const qaReference = process.env.ADMIN_QA_REFERENCE;
const visualBaselines = process.env.ADMIN_VISUAL_BASELINES === '1' && !process.env.CI;

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
      documentation: { state: 'ready', registered: true, registration_id: 'reg-1', source_registration_id: 'source-docs', source_registration_generation: 3, sources: [{ source_registration_id: 'source-docs', source_registration_generation: 3, state: 'ready', git_store_ref: 'git-store-public' }, { source_registration_id: 'source-secondary', source_registration_generation: 1, state: 'registered', git_store_ref: 'git-store-secondary' }], reconcile_state: 'ready', target_commit_oid: '0123456789abcdef0123456789abcdef01234567', next_poll_at: new Date(Date.now() + 60_000).toISOString(), revision_set_id: 'repo-doc-set-public', commit_oid: '0123456789abcdef0123456789abcdef01234567', requested_revision: 'HEAD', policy_source: 'committed', policy_hash: 'policy-public-safe', git_store_ref: 'git-store-public', overlay: false, namespace_id: 'embns-public', eligible_files: 4, eligible_chunks: 10, embedded_chunks: 8, reused_chunks: 2, failed_chunks: 0, excluded_files: 2, exclusions: [{ reason: 'lfs_pointer', count: 1 }, { reason: 'too_large', count: 1 }], missing_objects: 0, updated_at: new Date().toISOString(), revision_set_count: 2, search_available: true, semantic_available: true, retention: { committed_sets_per_identity: 8, overlay_max_age_hours: 24, terminal_max_age_hours: 168, vector_byte_ceiling: 536870912 }, index_handoff: 'gitcode-mcp repo-docs index --repo example/repo --registration-id reg-1 --source-registration-id source-docs --source-registration-generation 3', search_handoff: 'gitcode-mcp repo-docs search --repo example/repo --registration-id reg-1 --source-registration-id source-docs --source-registration-generation 3 "QUERY"' },
      recent_sync_events: [{ id: 'sync-1', kind: 'issue', status: 'succeeded', completed_at: new Date().toISOString(), zero_delta: false }]
    }]
  }],
  jobs: [
    { id: 'job-000001', type: 'rag-index', cache_ref: 'cache-111111112222', repo_id: 'example/repo', registration_id: 'reg-1', status: 'running', created_at: new Date(Date.now() - 120000).toISOString(), started_at: new Date(Date.now() - 110000).toISOString(), updated_at: new Date().toISOString(), steps: 80, completed: 40, work_ref: 'work-active', cancellable: true, retryable: false, progress_retained: 2, progress_limit: 256, throughput_per_second: 0.36, eta_seconds: 111, progress: [{ type: 'started', phase: 'running', collection: 'rag-index' }, { type: 'records', phase: 'running', collection: 'rag-index', records_fetched: 40, rate_limit_state: 'ready' }] },
    { id: 'job-000002', type: 'sync', cache_ref: 'cache-111111112222', repo_id: 'example/repo', registration_id: 'reg-1', status: 'failed', created_at: new Date(Date.now() - 240000).toISOString(), updated_at: new Date(Date.now() - 180000).toISOString(), finished_at: new Date(Date.now() - 180000).toISOString(), failure_class: 'provider_unavailable', failure_collection: 'issues', failure_message: 'The provider was unavailable while syncing issues.', retry_after: '30s', inspect_command: 'gitcode-mcp service job job-000002 --format json', remediation_command: 'gitcode-mcp service maintenance --format json', work_ref: 'work-terminal', cancellable: false, retryable: true, progress_retained: 1, progress_limit: 256, progress: [{ type: 'failed', phase: 'failed', collection: 'issues', records_failed: 1, retry_after: '30s', attempt: 2, rate_limit_state: 'waiting' }] },
    { id: 'job-000003', type: 'sync', cache_ref: 'cache-111111112222', repo_id: 'example/repo', registration_id: 'reg-1', status: 'interrupted', created_at: new Date(Date.now() - 360000).toISOString(), updated_at: new Date(Date.now() - 300000).toISOString(), finished_at: new Date(Date.now() - 300000).toISOString(), work_ref: 'work-interrupted', cancellable: false, retryable: true, progress_retained: 1, progress_limit: 256, progress: [{ type: 'interrupted', phase: 'interrupted', collection: 'sync' }] },
    { id: 'job-000004', type: 'repository-docs-index', cache_ref: 'cache-111111112222', repo_id: 'example/repo', registration_id: 'reg-1', status: 'succeeded', created_at: new Date(Date.now() - 90_000).toISOString(), started_at: new Date(Date.now() - 85_000).toISOString(), updated_at: new Date(Date.now() - 60_000).toISOString(), finished_at: new Date(Date.now() - 60_000).toISOString(), steps: 10, completed: 8, work_ref: 'repository-docs-public-work', cancellable: false, retryable: false, progress_retained: 2, progress_limit: 256, progress: [{ type: 'started', phase: 'indexing', collection: 'repository-docs-index' }, { type: 'succeeded', phase: 'succeeded', collection: 'repository-docs-index', records_fetched: 6, records_skipped: 2 }] }
  ],
  maintenance: [{ registration_id: 'reg-1', cache_ref: 'cache-111111112222', repo_id: 'example/repo', enabled: true, state: 'retry_scheduled', generation: 4, policy: { sync_enabled: true, sync_mode: 'head-and-backfill', rag_enabled: true, collections: ['issues', 'wiki'], head_max_pages: 3, tail_slice_pages: 10, profile: 'easy-rag' } }],
  diagnostics: [
    { id: 'diag-current', severity: 'warning', entity_type: 'maintenance', entity_id: 'reg-1', failure_class: 'cache_busy', message: 'A cache writer is active.', retryable: true, current: true, remediation: 'Wait for the scheduled retry.' },
    { id: 'diag-recovered', severity: 'warning', entity_type: 'cache', entity_id: 'cache-111111112222', failure_class: 'provider_unavailable', message: 'The provider recovered.', retryable: true, current: false }
  ],
  capabilities: [
    { id: 'rag_search', category: 'rag', safety_class: 'read_only', description: 'Search cached RAG chunks.', ui_enabled: false, ui_reason: 'Search lab is delivered separately.', cli_name: 'rag-search', cli_enabled: true, mcp_name: 'rag_search', mcp_enabled: true },
    { id: 'admin_maintenance_plan_apply', category: 'admin', safety_class: 'background_job', description: 'Plan and apply maintenance.', ui_enabled: true, cli_name: 'maintenance', cli_enabled: true, mcp_enabled: false },
    { id: 'admin_maintenance_conflict_resolution', category: 'admin', safety_class: 'audited_write', description: 'Resolve maintenance identity conflicts.', ui_enabled: true, cli_enabled: false, mcp_enabled: false },
    { id: 'admin_binding_plan_apply', category: 'admin', safety_class: 'audited_write', description: 'Plan and apply bindings.', ui_enabled: true, cli_name: 'repo', cli_enabled: true, mcp_enabled: false },
    { id: 'admin_registration_controls', category: 'admin', safety_class: 'background_job', description: 'Reconcile and disable registrations.', ui_enabled: true, cli_name: 'maintenance', cli_enabled: true, mcp_enabled: false },
    { id: 'admin_search_compare', category: 'admin', safety_class: 'read_only', description: 'Compare search modes.', ui_enabled: true, cli_name: 'search_sources', cli_enabled: true, mcp_enabled: false },
    { id: 'admin_provider_smoke', category: 'admin', safety_class: 'read_only', description: 'Smoke provider.', ui_enabled: true, cli_name: 'rag', cli_enabled: true, mcp_enabled: false },
    { id: 'admin_rag_bounded_repair', category: 'admin', safety_class: 'background_job', description: 'Bounded RAG repair.', ui_enabled: true, cli_name: 'rag', cli_enabled: true, mcp_enabled: false },
    { id: 'repository_docs_search', category: 'rag', safety_class: 'read_only', description: 'Search registered repository docs.', ui_enabled: true, cli_name: 'repo-docs', cli_enabled: true, mcp_name: 'repository_docs_search', mcp_enabled: true },
    { id: 'repository_docs_index', category: 'rag', safety_class: 'background_job', description: 'Index registered repository docs.', ui_enabled: true, cli_name: 'repo-docs', cli_enabled: true, mcp_name: 'repository_docs_index', mcp_enabled: true }
  ]
};

test('canonical registration deep links redirect and conflict resolution requires an explicit candidate', async ({ page }) => {
  const conflictSnapshot: any = structuredClone(snapshot);
  conflictSnapshot.maintenance = [{
    registration_id: 'reg-canonical', cache_ref: 'cache-111111112222', repo_id: 'example/repo', aliases: ['legacy/repo'], legacy_registration_ids: ['reg-legacy'], enabled: false, state: 'identity_conflict', generation: 7,
    policy: { sync_enabled: true, sync_mode: 'head', rag_enabled: false, collections: ['issues'] },
    identity_conflict: {
      kind: 'identity_conflict', details_available: true, candidate_registration_ids: ['reg-canonical', 'reg-legacy'], policy_hashes: ['policy-a', 'policy-b'], config_hashes: ['config-a', 'config-b'], path_fingerprints: ['path-a'],
      candidates: [
        { candidate_ref: 'candidate-a', registration_id: 'reg-canonical', repo_id: 'example/repo', policy: { sync_enabled: true, sync_mode: 'head', rag_enabled: false, collections: ['issues'] }, policy_hash: 'policy-a', config_hash: 'config-a', path_fingerprint: 'path-a', source_authority_hash: 'source-authority-a', source_refs: ['source-docs-a'], was_enabled: true },
        { candidate_ref: 'candidate-b', registration_id: 'reg-legacy', repo_id: 'legacy/repo', policy: { sync_enabled: false, sync_mode: 'off', rag_enabled: false, collections: [] }, policy_hash: 'policy-b', config_hash: 'config-b', path_fingerprint: 'path-a', source_authority_hash: 'source-authority-b', source_refs: ['source-docs-b'], was_enabled: false }
      ]
    }
  }];
  await mockAdmin(page, conflictSnapshot);
  let planBody: Record<string, unknown> = {};
  let applyBody: Record<string, unknown> = {};
  await page.route('**/api/admin/v1/maintenance/reg-canonical/conflict-resolution/plan', async (route) => {
    planBody = route.request().postDataJSON();
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', result: { schema_version: 'gitcode-mcp.maintenance-conflict-resolution-plan.v1', plan_id: 'conflict-plan-1', status: 'ready', registration_id: 'reg-canonical', canonical_registration_id: 'reg-canonical', conflict_kind: 'identity_conflict', expected_generation: 7, selected: conflictSnapshot.maintenance[0].identity_conflict!.candidates![0], effects: [{ class: 'identity', summary: 'Promote the selected candidate.', status: 'planned' }] } }) });
  });
  await page.route('**/api/admin/v1/maintenance/reg-canonical/conflict-resolution/apply', async (route) => {
    applyBody = route.request().postDataJSON();
	delete conflictSnapshot.maintenance[0].identity_conflict;
	Object.assign(conflictSnapshot.maintenance[0], { enabled: true, state: 'enrolled', generation: 8 });
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', result: { outcome: 'resolved', receipt_id: 'maintenance-conflict-receipt-1', plan_id: 'conflict-plan-1', registration_id: 'reg-canonical', replayed: false } }) });
  });
  await page.goto('/?view=Maintenance&registration=reg-legacy');
  await expect(page).toHaveURL(/registration=reg-canonical/);
  await expect(page.getByText('Redirected legacy registration reg-legacy to canonical reg-canonical.')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Policy or source conflict' })).toBeVisible();
  await expect(page.getByText(/source source-authority-a/)).toBeVisible();
  await expect(page.getByText(/source-docs-a/)).toBeVisible();
  const candidateRadios = page.getByRole('radio', { name: /previously/ });
  await expect(candidateRadios).toHaveCount(2);
  await expect(candidateRadios.nth(0)).not.toBeChecked();
  await expect(candidateRadios.nth(1)).not.toBeChecked();
  await expect(page.getByRole('button', { name: 'Review selected candidate' })).toBeDisabled();
  await candidateRadios.nth(0).check();
  await page.getByRole('button', { name: 'Review selected candidate' }).click();
  expect(planBody).toEqual({ candidate_ref: 'candidate-a', expected_generation: 7 });
  await expect(page.getByText('conflict-plan-1')).toBeVisible();
  await page.getByRole('button', { name: 'Confirm resolution' }).click();
  await expect(page.getByRole('heading', { name: 'Resolve this identity conflict?' })).toBeVisible();
  await page.getByRole('button', { name: 'Confirm selected candidate' }).click();
  expect(applyBody).toMatchObject({ candidate_ref: 'candidate-a', expected_generation: 7, plan_id: 'conflict-plan-1' });
  expect(String(applyBody.idempotency_key)).toMatch(/^admin-conflict_resolution_apply-/);
	await expect(page.getByRole('heading', { name: 'Policy or source conflict' })).toHaveCount(0);
  await expect(page.getByText('Receipt maintenance-conflict-receipt-1')).toBeVisible();
});

test('stale conflict apply closes confirmation, refreshes candidates, and requires a new plan', async ({ page }) => {
  const conflictSnapshot: any = structuredClone(snapshot);
  conflictSnapshot.maintenance = [{
    registration_id: 'reg-canonical', cache_ref: 'cache-111111112222', repo_id: 'example/repo', enabled: false, state: 'identity_conflict', generation: 7,
    policy: { sync_enabled: true, sync_mode: 'head', rag_enabled: false, collections: ['issues'] },
    identity_conflict: { kind: 'identity_conflict', details_available: true, candidate_registration_ids: ['reg-canonical', 'reg-legacy'], policy_hashes: ['a', 'b'], config_hashes: ['a', 'b'], path_fingerprints: ['path-a'], candidates: [
      { candidate_ref: 'candidate-a', registration_id: 'reg-canonical', repo_id: 'example/repo', policy: { sync_enabled: true, sync_mode: 'head', rag_enabled: false, collections: ['issues'] }, policy_hash: 'a', config_hash: 'a', path_fingerprint: 'path-a', source_authority_hash: 'source-a', source_refs: ['docs-a'], was_enabled: true },
      { candidate_ref: 'candidate-b', registration_id: 'reg-legacy', repo_id: 'legacy/repo', policy: { sync_enabled: false, sync_mode: 'off', rag_enabled: false, collections: [] }, policy_hash: 'b', config_hash: 'b', path_fingerprint: 'path-a', source_authority_hash: 'source-b', source_refs: ['docs-b'], was_enabled: false }
    ] }
  }];
  await mockAdmin(page, conflictSnapshot);
  await page.route('**/api/admin/v1/maintenance/reg-canonical/conflict-resolution/plan', (route) => route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', result: { schema_version: 'gitcode-mcp.maintenance-conflict-resolution-plan.v1', plan_id: 'stale-conflict-plan', status: 'ready', registration_id: 'reg-canonical', canonical_registration_id: 'reg-canonical', result_registration_ids: ['reg-canonical'], conflict_kind: 'identity_conflict', expected_generation: 7, selected: conflictSnapshot.maintenance[0].identity_conflict.candidates[0], effects: [{ class: 'identity', summary: 'Promote selected candidate.', status: 'planned' }] } }) }));
  await page.route('**/api/admin/v1/maintenance/reg-canonical/conflict-resolution/apply', (route) => route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: { code: 'stale_plan', message: 'Candidate generation changed.', remediation: 'Refresh and render a new plan.' } }) }));
  await page.goto('/?view=Maintenance&registration=reg-canonical');
  await page.getByRole('radio', { name: /example\/repo/ }).check();
  await page.getByRole('button', { name: 'Review selected candidate' }).click();
  await page.getByRole('button', { name: 'Confirm resolution' }).click();
  await page.getByRole('button', { name: 'Confirm selected candidate' }).click();
  await expect(page.getByRole('dialog')).not.toBeVisible();
  await expect(page.getByRole('alert').filter({ hasText: 'Conflict resolution failed' })).toContainText('Refresh and render a new plan.');
  await expect(page.getByText('stale-conflict-plan')).not.toBeVisible();
  await expect(page.locator('input[name="conflict-candidate"]:checked')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Review selected candidate' })).toBeDisabled();
});

test('ambiguous conflict apply keeps the exact plan and idempotency key for replay', async ({ page }) => {
	const conflictSnapshot: any = structuredClone(snapshot);
	conflictSnapshot.maintenance = [{
		registration_id: 'reg-canonical', cache_ref: 'cache-111111112222', repo_id: 'example/repo', enabled: false, state: 'identity_conflict', generation: 7,
		policy: { sync_enabled: true, sync_mode: 'head', rag_enabled: false, collections: ['issues'] },
		identity_conflict: { kind: 'identity_conflict', details_available: true, candidate_registration_ids: ['reg-canonical'], policy_hashes: ['a'], config_hashes: ['a'], path_fingerprints: ['path-a'], candidates: [
			{ candidate_ref: 'candidate-a', registration_id: 'reg-canonical', repo_id: 'example/repo', policy: { sync_enabled: true, sync_mode: 'head', rag_enabled: false, collections: ['issues'] }, policy_hash: 'a', config_hash: 'a', path_fingerprint: 'path-a', source_authority_hash: 'source-a', source_refs: ['docs-a'], was_enabled: true }
		] }
	}];
	await mockAdmin(page, conflictSnapshot);
	await page.route('**/api/admin/v1/maintenance/reg-canonical/conflict-resolution/plan', (route) => route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', result: { schema_version: 'gitcode-mcp.maintenance-conflict-resolution-plan.v1', plan_id: 'retry-conflict-plan', status: 'ready', registration_id: 'reg-canonical', canonical_registration_id: 'reg-canonical', result_registration_ids: ['reg-canonical'], conflict_kind: 'identity_conflict', expected_generation: 7, selected: conflictSnapshot.maintenance[0].identity_conflict.candidates[0], effects: [{ class: 'identity', summary: 'Promote selected candidate.', status: 'planned' }] } }) }));
	const applyBodies: Array<Record<string, unknown>> = [];
	await page.route('**/api/admin/v1/maintenance/reg-canonical/conflict-resolution/apply', async (route) => {
		applyBodies.push(route.request().postDataJSON());
		if (applyBodies.length === 1) {
			await route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'temporarily_unavailable', message: 'Receipt delivery was interrupted.', remediation: 'Retry this confirmation.' } }) });
			return;
		}
		await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', result: { outcome: 'resolved', receipt_id: 'maintenance-conflict-receipt-replayed', plan_id: 'retry-conflict-plan', registration_id: 'reg-canonical', replayed: true } }) });
	});
	await page.goto('/?view=Maintenance&registration=reg-canonical');
	await page.getByRole('radio', { name: /example\/repo/ }).check();
	await page.getByRole('button', { name: 'Review selected candidate' }).click();
	await page.getByRole('button', { name: 'Confirm resolution' }).click();
	await page.getByRole('button', { name: 'Confirm selected candidate' }).click();
	const dialog = page.getByRole('dialog');
	await expect(dialog).toBeVisible();
	await expect(dialog.getByRole('alert')).toContainText('Receipt delivery was interrupted.');
	await expect(dialog).toContainText('retry-conflict-plan');
	await dialog.getByRole('button', { name: 'Confirm selected candidate' }).click();
	await expect(dialog).not.toBeVisible();
	expect(applyBodies).toHaveLength(2);
	expect(applyBodies[1]).toMatchObject({ plan_id: 'retry-conflict-plan', idempotency_key: applyBodies[0].idempotency_key });
	await expect(page.getByRole('status').filter({ hasText: 'maintenance-conflict-receipt-replayed' })).toContainText('replayed safely');
});

test('clone conflict choices are physical path cohorts and show every retained repository authority', async ({ page }) => {
  const cloneSnapshot: any = structuredClone(snapshot);
  const member = (repo: string, registration: string, source: string) => ({ candidate_ref: `member-${registration}`, registration_id: registration, repo_id: repo, policy: { sync_enabled: repo.endsWith('first'), sync_mode: repo.endsWith('first') ? 'head' : 'off', rag_enabled: false, collections: [] }, policy_hash: `policy-${registration}`, config_hash: `config-${registration}`, path_fingerprint: 'path-a', source_authority_hash: source, source_refs: [`${source}-ref`], was_enabled: true });
  const pathA = [member('owner/first', 'reg-first', 'source-first'), member('owner/second', 'reg-second', 'source-second')];
  const pathB = pathA.map((value: any) => ({ ...value, candidate_ref: `${value.candidate_ref}-clone`, path_fingerprint: 'path-b' }));
  cloneSnapshot.maintenance = [{ registration_id: 'clone-conflict-1', legacy_registration_ids: ['deep-legacy-reg-first'], cache_ref: 'cache-111111112222', repo_id: 'owner/first', enabled: false, state: 'cache_clone_conflict', generation: 9, policy: pathA[0].policy, identity_conflict: { kind: 'cache_clone_conflict', details_available: true, candidate_registration_ids: ['reg-first', 'reg-second'], policy_hashes: ['p'], config_hashes: ['c'], path_fingerprints: ['path-a', 'path-b'], candidates: [
    { candidate_ref: 'clone-path-a', selection_kind: 'physical_cache_authority', registration_id: '', repo_id: '', policy: {}, policy_hash: '', path_fingerprint: 'path-a', source_authority_hash: 'cohort-source-a', was_enabled: true, cohort_registration_ids: ['reg-first', 'reg-second'], cohort_repo_ids: ['owner/first', 'owner/second'], members: pathA },
    { candidate_ref: 'clone-path-b', selection_kind: 'physical_cache_authority', registration_id: '', repo_id: '', policy: {}, policy_hash: '', path_fingerprint: 'path-b', source_authority_hash: 'cohort-source-b', was_enabled: true, cohort_registration_ids: ['reg-first', 'reg-second'], cohort_repo_ids: ['owner/first', 'owner/second'], members: pathB }
  ] } }];
  await mockAdmin(page, cloneSnapshot);
  await page.goto('/?view=Maintenance&registration=deep-legacy-reg-first');
  await expect(page.getByRole('heading', { name: 'Cache clone conflict' })).toBeVisible();
  await expect(page.getByText('Select one physical cache authority')).toBeVisible();
  const cloneChoices = page.locator('input[name="conflict-candidate"]');
  await expect(cloneChoices).toHaveCount(2);
  await expect(page.getByText(/owner\/first · Head sync/).first()).toBeVisible();
  await expect(page.getByText(/owner\/second · Off sync/).first()).toBeVisible();
  await expect(page.getByText(/source-first/).first()).toBeVisible();
  await expect(cloneChoices.first()).not.toBeChecked();
});

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
  const docsSnapshot = structuredClone(snapshot);
  const docsJob = docsSnapshot.jobs.find((job) => job.id === 'job-000004')!;
  docsJob.status = 'running'; docsJob.cancellable = true; docsJob.finished_at = undefined;
  docsSnapshot.jobs.push({ ...docsJob, id: 'job-000099', repo_id: 'other/repository', work_ref: 'wrong-repository-work', updated_at: new Date(Date.now() + 60_000).toISOString() });
  await mockAdmin(page, docsSnapshot);
  let indexBody: Record<string, string> = {};
  let cancelledJob = '';
  await page.route('**/api/admin/v1/repository-docs/reg-1/index', async (route) => {
    indexBody = route.request().postDataJSON();
    expect(route.request().headers()['x-csrf-token']).toBe('csrf-test');
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', result: { outcome: 'accepted', job_id: 'job-000004', job_status: 'succeeded' } }) });
  });
  await page.route('**/api/admin/v1/repository-docs/reg-1/search', async (route) => {
    expect(route.request().headers()['x-csrf-token']).toBe('csrf-test');
    expect(route.request().postDataJSON()).toMatchObject({ query: 'private registration', revision: 'HEAD', mode: 'hybrid', limit: 8, include_worktree: false, source_registration_id: 'source-docs', source_registration_generation: 3 });
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', result: {
      repo_id: 'example/repo', corpus_kind: 'repository_docs', query: 'private registration', requested_revision: 'HEAD', effective_revision: '0123456789abcdef0123456789abcdef01234567', requested_mode: 'hybrid', effective_mode: 'hybrid', authority: 'git', revision_set_id: 'repo-doc-set-public', policy_hash: 'policy-public-safe', policy_source: 'committed', namespace_id: 'embns-public', coverage: { state: 'ready', eligible_files: 4, eligible_chunks: 10, embedded_chunks: 8, reused_chunks: 2, failed_chunks: 0, missing_objects: 0 }, warning_details: [{ code: 'git_object_unavailable', message: 'Fetch the required object and retry.' }], hits: [{ rank: 1, chunk_id: 'chunk-public-safe', snippet: 'The daemon resolves Git authority from a private registration.', score: 0.031, lexical_score: 1.2, semantic_score: 0.8, citation: { authority: 'git', commit_oid: '0123456789abcdef0123456789abcdef01234567', blob_oid: 'abcdef0123456789abcdef0123456789abcdef01', path: 'docs/architecture.md', line_start: 3, line_end: 4, raw_slice_digest: 'digest-public-safe' } }]
    } }) });
  });
  await page.route('**/api/admin/v1/repository-docs/reg-1/plan', async (route) => {
    expect(route.request().headers()['x-csrf-token']).toBe('csrf-test');
    expect(route.request().postDataJSON()).toMatchObject({ revision: 'HEAD', include_worktree: false, source_registration_id: 'source-docs', source_registration_generation: 3 });
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', result: {
      repo_id: 'example/repo', commit_oid: '0123456789abcdef0123456789abcdef01234567', include_worktree: false,
      git_store_ref: 'git-store-public', eligible_files: 4, eligible_bytes: 8192, excluded_files: 2, missing_objects: 0,
      effective_include: ['README*', 'AGENTS.md', 'docs/**'], effective_exclude: [],
      policy: { source: 'committed', policy_hash: 'policy-public-safe', policy: { schema: 1, enabled: true, preset: 'conventional-docs-v1', include: [], exclude: [] } }
    } }) });
  });
  await page.route('**/api/admin/v1/jobs/job-000004/cancel', async (route) => {
    cancelledJob = 'job-000004';
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', receipt: { outcome: 'accepted', receipt_id: 'receipt-doc-cancel', target_job_id: 'job-000004', job_status: 'cancel_requested' } }) });
  });
  await page.route('**/api/admin/v1/jobs/job-000099/cancel', async (route) => {
    cancelledJob = 'job-000099';
    await route.fulfill({ status: 500, body: 'wrong repository job selected' });
  });
  await page.goto('/?view=Caches&cache=cache-111111112222&repo=example%2Frepo&tab=documentation');
  await expect(page.getByRole('heading', { name: 'Repository documentation RAG' })).toBeVisible();
  await expect(page.getByText('Committed Git')).toBeVisible();
  await expect(page.getByText('10/10')).toBeVisible();
  await expect(page.getByText('repo-doc-set-public')).toBeVisible();
  await expect(page.getByText('Automatic reconciliation')).toBeVisible();
  await expect(page.getByText('Source generation')).toBeVisible();
  await expect(page.getByText('Derived-state retention')).toBeVisible();
  await expect(page.getByText('8 committed sets')).toBeVisible();
  await expect(page.getByText(/vectors ≤ 512 MiB/)).toBeVisible();
  await expect(page.getByLabel('Git authority')).toHaveValue('source-docs');
  await expect(page.getByText('Lfs Pointer')).toBeVisible();
  await Promise.all([
    page.waitForResponse((response) => response.url().endsWith('/api/admin/v1/repository-docs/reg-1/plan') && response.status() === 200),
    page.getByRole('button', { name: 'Preview exact plan' }).click()
  ]);
  await expect(page.getByText('4 eligible files · 8192 bytes')).toBeVisible();
  await expect(page.getByText('Effective include: README*, AGENTS.md, docs/** · exclude: none')).toBeVisible();
  await page.getByRole('button', { name: 'Index current HEAD' }).click();
  await expect(page.getByRole('heading', { name: 'Resolve and index current HEAD?' })).toBeVisible();
  await Promise.all([
    page.waitForResponse((response) => response.url().endsWith('/api/admin/v1/repository-docs/reg-1/index') && response.status() === 200),
    page.waitForResponse((response) => response.url().endsWith('/api/admin/v1/snapshot') && response.status() === 200),
    page.getByRole('button', { name: 'Confirm indexing' }).click()
  ]);
  expect(indexBody.idempotency_key).toMatch(/^admin-repository_docs_index-/);
  expect(indexBody).toMatchObject({ source_registration_id: 'source-docs', source_registration_generation: 3 });
  await page.getByLabel('Query').fill('private registration');
  await Promise.all([
    page.waitForResponse((response) => response.url().endsWith('/api/admin/v1/repository-docs/reg-1/search') && response.status() === 200),
    page.getByRole('button', { name: 'Search Git' }).click()
  ]);
  await expect(page.getByText('The daemon resolves Git authority from a private registration.')).toBeVisible();
  await expect(page.getByText('docs/architecture.md:L3–L4')).toBeVisible();
  await expect(page.getByText('Digest-verified Git citation')).toBeVisible();
  await expect(page.getByText('Git Object Unavailable')).toBeVisible();
  await expect(page.getByText("The browser sends only the opaque registration id. Filesystem authority remains in the daemon's private registry.")).toBeVisible();
  await page.getByRole('button', { name: 'Open index jobs' }).click();
  await expect(page.getByRole('heading', { name: 'Repository Docs Index' })).toBeVisible();
  await expect(page.getByText('repository-docs-public-work')).toBeVisible();
  await expect(page.getByText('wrong-repository-work')).toHaveCount(0);
  await page.getByRole('button', { name: 'Cancel job' }).click();
  await page.getByRole('button', { name: 'Confirm cancel' }).click();
  expect(cancelledJob).toBe('job-000004');
  await page.goto('/?view=Caches&cache=cache-111111112222&repo=example%2Frepo&tab=documentation');
  await page.reload();
  await expect(page).toHaveURL(/tab=documentation/);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

const repositoryDocsStateMatrix = [
  { name: 'disabled', state: 'disabled', reconcile: 'disabled', registered: true, lexical: true, semantic: false, active: '' },
  { name: 'unavailable', state: 'unavailable', reconcile: 'unavailable', registered: false, lexical: false, semantic: false, active: '' },
  { name: 'empty', state: 'ready', reconcile: 'ready', registered: true, lexical: true, semantic: true, active: '', empty: true },
  { name: 'partial', state: 'partial', reconcile: 'partial', registered: true, lexical: true, semantic: false, active: 'partial' },
  { name: 'building', state: 'building', reconcile: 'indexing', registered: true, lexical: true, semantic: false, active: 'building' },
  { name: 'blocked', state: 'blocked', reconcile: 'blocked', registered: true, lexical: true, semantic: false, active: 'blocked' },
  { name: 'superseded', state: 'superseded', reconcile: 'registered', registered: true, lexical: true, semantic: false, active: 'superseded' },
  { name: 'stale', state: 'ready', reconcile: 'stale', registered: true, lexical: true, semantic: true, active: 'superseded' },
  { name: 'registered-without-ready-set', state: 'not_indexed', reconcile: 'registered', registered: true, lexical: true, semantic: false, active: '' },
  { name: 'ready', state: 'ready', reconcile: 'ready', registered: true, lexical: true, semantic: true, active: '' }
] as const;

const humanizedState = (value: string) => value
  .replaceAll('_', ' ')
  .replaceAll('-', ' ')
  .replace(/\b\w/g, (letter) => letter.toUpperCase());

for (const scenario of repositoryDocsStateMatrix) {
  test(`repository documentation state invariants: ${scenario.name}`, async ({ page }) => {
    const stateSnapshot = structuredClone(snapshot);
    const docs = stateSnapshot.caches[0].repositories[0].documentation as Record<string, unknown>;
    Object.assign(docs, {
      state: scenario.state,
      registered: scenario.registered,
      reconcile_state: scenario.reconcile,
      search_available: scenario.lexical,
      semantic_available: scenario.semantic,
      active_state: scenario.active || undefined,
      active_revision_set_id: scenario.active ? `active-${scenario.name}` : undefined,
      next_poll_at: undefined,
      updated_at: undefined,
      last_error_class: scenario.reconcile === 'unavailable' ? 'repository_docs_source_unavailable' : undefined,
      last_failure_class: scenario.state === 'blocked' ? 'repository_docs_provider_boundary_blocked' : undefined
    });
    if (!scenario.registered) {
      Object.assign(docs, {
        registration_id: undefined, source_registration_id: undefined, source_registration_generation: undefined,
        sources: [], search_handoff: undefined, index_handoff: undefined
      });
    }
    const hasReadySet = scenario.semantic;
    if (!hasReadySet) {
      Object.assign(docs, {
        revision_set_id: undefined, commit_oid: undefined, namespace_id: undefined, policy_hash: undefined,
        eligible_files: 0, eligible_chunks: 0, embedded_chunks: 0, reused_chunks: 0, failed_chunks: 0,
        missing_objects: 0, excluded_files: 0, exclusions: [], revision_set_count: scenario.active ? 1 : 0
      });
    }
    if ('empty' in scenario && scenario.empty) {
      Object.assign(docs, { eligible_files: 0, eligible_chunks: 0, embedded_chunks: 0, reused_chunks: 0, excluded_files: 0, exclusions: [] });
    }

    await mockAdmin(page, stateSnapshot);
    if (scenario.name === 'registered-without-ready-set') {
      await page.route('**/api/admin/v1/repository-docs/reg-1/search', async (route) => {
        expect(route.request().postDataJSON()).toMatchObject({ mode: 'fulltext', query: 'offline lexical contract' });
        await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', result: {
          repo_id: 'example/repo', corpus_kind: 'repository_docs', query: 'offline lexical contract', requested_revision: 'HEAD',
          effective_revision: '0123456789abcdef0123456789abcdef01234567', requested_mode: 'fulltext', effective_mode: 'fulltext',
          authority: 'git', coverage: { state: 'not_indexed', eligible_files: 0, eligible_chunks: 0, embedded_chunks: 0, reused_chunks: 0, failed_chunks: 0, missing_objects: 0 }, hits: []
        } }) });
      });
    }
    await page.setViewportSize({ width: 1280, height: 1400 });
    await page.goto('/?view=Caches&cache=cache-111111112222&repo=example%2Frepo&tab=documentation');
    const availability = page.getByLabel('Repository documentation search availability');
    await expect(availability).toContainText(scenario.lexical ? 'Offline full textAvailable' : 'Offline full textUnavailable');
    await expect(availability).toContainText(scenario.semantic ? 'Semantic rankingReady' : 'Semantic rankingLexical fallback');
    if (scenario.lexical) await expect(page.getByRole('button', { name: 'Search Git' })).toBeEnabled();
    else await expect(page.getByRole('button', { name: 'Search Git' })).toBeDisabled();
    const documentation = page.getByRole('region', { name: 'Repository documentation RAG' });
    await expect(documentation.locator('.section-heading .status-chip')).toHaveText(scenario.state.replaceAll('_', ' '));
    const reconciliation = documentation.locator('article').filter({ hasText: 'Automatic reconciliation' });
    await expect(reconciliation.locator('strong')).toHaveText(scenario.registered ? humanizedState(scenario.reconcile) : 'Not registered');
    const activeAttempt = documentation.locator('article').filter({ hasText: 'Active attempt' });
    await expect(activeAttempt.locator('strong')).toHaveText(humanizedState(scenario.active || 'idle'));
    await expect(activeAttempt.locator('p')).toHaveText(scenario.active ? `active-${scenario.name}` : 'No competing generation');
    if (visualBaselines) {
      await expect(page).toHaveScreenshot(`repository-docs-${scenario.name}.png`, { animations: 'disabled', maxDiffPixelRatio: 0.02 });
    }
    if (scenario.name === 'registered-without-ready-set') {
      await page.getByLabel('Mode').selectOption('fulltext');
      await page.getByLabel('Query').fill('offline lexical contract');
      await page.getByRole('button', { name: 'Search Git' }).click();
      await expect(page.getByText('No Git-backed match')).toBeVisible();
    }
  });
}

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
