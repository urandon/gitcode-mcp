import { expect, test, type Page } from '@playwright/test';
import { emptySnapshot } from '../src/lib/admin';
import type { LocalCitation, LocalEntity, LocalKind, LocalMutationIntent } from '../src/lib/local-projects';

function citation(kind: LocalKind, id: string, digit = '1'): LocalCitation {
  return { project_id: 'lab', kind, id, commit: digit.repeat(40), blob: digit.repeat(40), path: kind === 'wiki' ? `wiki/${id}.md` : kind === 'comment' ? `issues/ISSUE-1/comments/${id}.md` : `issues/${id}/issue.md` };
}
async function localFixture(page: Page, options: { enabled?: boolean; readonly?: boolean; lostReply?: boolean; conflict?: boolean; delayedList?: Promise<void> } = {}) {
  const calls: Array<{ operation: string; body: any }> = [];
  const applies: LocalMutationIntent[] = [];
  const records: Record<string, LocalEntity> = {
    'ISSUE-1': { metadata: { schema: 1, id: 'ISSUE-1', kind: 'issue', title: 'Question', state: 'open', labels: ['evidence'], links: [{ kind: 'wiki', id: 'DOC-1' }] }, body: 'Original evidence', citation: citation('issue', 'ISSUE-1') },
    'COMMENT-1': { metadata: { schema: 1, id: 'COMMENT-1', kind: 'comment', parent_id: 'ISSUE-1', title: 'Reply', state: 'active' }, body: 'Comment evidence', citation: citation('comment', 'COMMENT-1') },
    'DOC-1': { metadata: { schema: 1, id: 'DOC-1', kind: 'wiki', title: 'Design', state: 'active' }, body: 'Wiki evidence', citation: citation('wiki', 'DOC-1') }
  };
  let head = '1'.repeat(40), lost = Boolean(options.lostReply), conflict = Boolean(options.conflict), indexState = 'missing';
  const receipts = new Map<string, any>();
  const view = structuredClone(emptySnapshot); view.revision = 'local-fixture'; view.generated_at = new Date().toISOString();
  view.service = { ...view.service, version: 'fixture', running: true, admin_secure: true };
  view.capabilities = options.enabled === false ? [] : ['status', 'get', 'list', 'history', 'mutate', 'search', 'index'].map(name => ({ id: `local_project_${name}`, category: 'local_project', safety_class: 'read_only', description: 'Local fixture capability', cli_enabled: true, mcp_enabled: true, ui_enabled: true }));
  await page.route('**/api/admin/v1/session', route => route.fulfill({ contentType: 'application/json', body: JSON.stringify({ csrf_token: 'fixture-csrf' }) }));
  await page.route('**/api/admin/v1/snapshot', route => route.fulfill({ contentType: 'application/json', body: JSON.stringify(view) }));
  await page.route('**/api/admin/v1/events', route => route.fulfill({ contentType: 'text/event-stream', body: ': ready\n\n' }));
  await page.route('**/api/admin/v1/local-projects**', async route => {
    const request = route.request(), operation = new URL(request.url()).pathname.split('/').pop()!;
    const body = request.method() === 'POST' ? request.postDataJSON() : undefined;
    calls.push({ operation, body });
    const reply = (result: unknown) => route.fulfill({ contentType: 'application/json', body: JSON.stringify({ api_version: '1', result }) });
    const failure = (code: string) => route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: { code, message: 'must-not-render-provider-body' } }) });
    if (operation !== 'local-projects' && request.headers()['x-csrf-token'] !== 'fixture-csrf') return failure('csrf_validation_failed');
    if (operation === 'local-projects') return reply({ projects: [{ project: 'lab', project_id: 'lab', readable: true, read_only: Boolean(options.readonly), state: options.readonly ? 'read_only' : 'ready', commit: head }, { project: 'other', project_id: 'other', readable: false, read_only: true, state: 'unavailable' }] });
    if (operation === 'list') { if (options.delayedList) await options.delayedList; return reply(Object.values(records).filter(e => e.metadata.kind === body.kind)); }
    if (operation === 'get') return reply(records[body.id]);
    if (operation === 'history') return reply([records[body.id].citation]);
    if (operation === 'search') return reply({ project: 'lab', commit: head, requested_mode: body.mode, effective_mode: body.mode === 'fulltext' ? 'fulltext' : indexState === 'ready' ? 'hybrid' : 'fulltext', index_state: body.mode === 'fulltext' ? 'not_requested' : indexState, fallback: body.mode === 'fulltext' ? '' : `index_${indexState}`, hits: [{ citation: records['ISSUE-1'].citation, snippet: records['ISSUE-1'].body, line_start: 1, line_end: 1 }] });
    if (operation.endsWith('-plan')) return reply({ plan_id: `local-project-plan-${'a'.repeat(64)}`, project: 'lab', project_id: 'lab', operation: body.mutation?.operation || 'index', commit: head, data_boundary: body.mutation ? 'local_git' : 'local_process', max_chunks: body.max_chunks, effects: [body.mutation ? 'publish_entity_counters_and_receipt_in_one_git_commit' : 'embed_at_most_confirmed_new_chunks'] });
    if (operation === 'mutation-apply') {
      applies.push(body);
      const m = body.mutation;
      if (receipts.has(m.idempotency_key)) return reply({ ...receipts.get(m.idempotency_key), replayed: true });
      if (conflict) { conflict = false; head = '2'.repeat(40); records['ISSUE-1'].body = 'Concurrent author evidence'; records['ISSUE-1'].citation = citation('issue', 'ISSUE-1', '2'); return failure('local_git_write_conflict'); }
      const kind: LocalKind = m.operation.endsWith('wiki') ? 'wiki' : m.operation.endsWith('comment') ? 'comment' : 'issue';
      const id = m.id || (kind === 'wiki' ? 'DOC-2' : kind === 'comment' ? 'COMMENT-2' : 'ISSUE-2');
      head = '3'.repeat(40); indexState = 'stale';
      const item: LocalEntity = { metadata: { schema: 1, kind, id, title: m.title || records[id]?.metadata.title, state: m.operation.startsWith('delete') ? 'deleted' : m.state || (kind === 'issue' ? 'open' : 'active'), ...(m.parent_id ? { parent_id: m.parent_id } : {}), labels: m.labels, links: m.links }, body: m.body || '', citation: citation(kind, id, '3') };
      records[id] = item;
      const result = { entity: item, replayed: false }; receipts.set(m.idempotency_key, result);
      if (lost) { lost = false; return route.abort('connectionreset'); }
      return reply(result);
    }
    if (operation === 'index-apply') { indexState = body.max_chunks === 1 ? 'partial' : 'ready'; return reply({ project: 'lab', commit: head, state: indexState, embedded_chunks: body.max_chunks === 1 ? 1 : 3, replayed: false }); }
    return failure('local_git_invalid_argument');
  });
  return { calls, applies, records };
}

test('local edits confirm/cancel, invalidate plans and replay lost replies with the exact durable key', async ({ page }) => {
  const fixture = await localFixture(page, { lostReply: true });
  await page.goto('/?view=Local+projects&project=lab&entity=ISSUE-1');
  await expect(page.getByRole('heading', { name: 'Local projects', exact: true })).toBeVisible();
  await expect(page.getByRole('region', { name: 'Local entity detail' })).toContainText('Original evidence');
  await page.getByRole('button', { name: 'Read bounded history' }).click();
  await expect(page.locator('.local-history')).toContainText('issues/ISSUE-1/issue.md');
  await page.getByRole('button', { name: 'Edit entity', exact: true }).click();
  await page.getByRole('textbox', { name: 'Entity body' }).fill('Draft first');
  await page.getByRole('button', { name: 'Render mutation plan' }).click();
  const confirm = page.getByRole('button', { name: 'Confirm local action', exact: true });
  await confirm.click();
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByRole('button', { name: 'Keep current state' })).toBeFocused();
  await page.keyboard.press('Escape'); await expect(dialog).not.toBeVisible(); await expect(confirm).toBeFocused();
  expect(fixture.applies).toHaveLength(0);
  await page.getByRole('textbox', { name: 'Entity body' }).fill('Retained local draft');
  await expect(page.getByRole('region', { name: 'Reviewed local intent' })).toHaveCount(0);
  await page.getByRole('button', { name: 'Render mutation plan' }).click();
  await confirm.click(); await dialog.getByRole('button', { name: 'Confirm action', exact: true }).click();
  await expect(dialog.getByRole('alert')).toContainText('Retain the draft and confirmed key');
  await dialog.getByRole('button', { name: 'Keep current state' }).click();
  await expect(page.getByRole('textbox', { name: 'Entity body' })).toBeDisabled();
  await expect(page.getByRole('textbox', { name: 'Entity body' })).toHaveValue('Retained local draft');
  await page.getByRole('button', { name: 'Retry exact confirmed action' }).click();
  await dialog.getByRole('button', { name: 'Retry same intent and key' }).click();
  await expect(page.getByRole('status', { name: 'Local action receipt' })).toContainText('Replayed original result');
  expect(fixture.applies).toHaveLength(2); expect(fixture.applies[1]).toEqual(fixture.applies[0]);
  expect(fixture.applies[0].mutation.labels).toEqual(['evidence']); expect(fixture.applies[0].mutation.links).toEqual([{ kind: 'wiki', id: 'DOC-1' }]);
  await expect(page.getByRole('region', { name: 'Local entity detail' })).toContainText('Retained local draft');
  expect(page.url()).not.toContain('Retained'); expect(page.url()).not.toContain('idempotency');
  const browserStorage = await page.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } }));
  expect(browserStorage).not.toContain('Retained local draft'); expect(browserStorage).not.toContain(fixture.applies[0].mutation.idempotency_key);
});

test('conflict preserves a draft and requires explicit inspection and revision adoption', async ({ page }) => {
  const fixture = await localFixture(page, { conflict: true });
  await page.goto('/?view=Local+projects&project=lab&entity=ISSUE-1');
  await page.getByRole('button', { name: 'Edit entity', exact: true }).click();
  await page.getByRole('textbox', { name: 'Entity body' }).fill('Preserved conflict draft');
  await page.getByRole('button', { name: 'Render mutation plan' }).click();
  await page.getByRole('button', { name: 'Confirm local action', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Confirm action', exact: true }).click();
  await expect(page.getByRole('dialog')).not.toBeVisible();
  await expect(page.getByRole('alert')).toContainText('Git revision changed');
  await expect(page.getByRole('textbox', { name: 'Entity body' })).toHaveValue('Preserved conflict draft');
  await page.getByRole('button', { name: 'Inspect current entity; retain draft' }).click();
  await expect(page.getByRole('region', { name: 'Local entity detail' })).toContainText('Concurrent author evidence');
  await page.getByRole('button', { name: 'Use inspected revision for this draft' }).click();
  await expect(page.getByRole('textbox', { name: 'Entity body' })).toHaveValue('Preserved conflict draft');
  await page.getByRole('button', { name: 'Render mutation plan' }).click();
  const plans = fixture.calls.filter(c => c.operation === 'mutation-plan');
  expect(plans[1].body.mutation.expected_revision).toBe('2'.repeat(40)); expect(plans[0].body.mutation.expected_revision).toBe('1'.repeat(40));
  expect(plans[1].body.mutation.idempotency_key).not.toBe(plans[0].body.mutation.idempotency_key);
  await expect(page.locator('body')).not.toContainText('must-not-render-provider-body');
});

test('search fallback, bounded index confirmation and every entity kind stay in the same cohort', async ({ page }) => {
  const fixture = await localFixture(page);
  await page.goto('/?view=Local+projects&project=lab');
  await page.getByRole('textbox', { name: 'Local search query' }).fill('evidence');
  await page.getByRole('button', { name: 'Search local project' }).click();
  await expect(page.getByRole('region', { name: 'Local project search' })).toContainText('Hybrid → Fulltext · RAG Missing');
  await page.getByRole('spinbutton', { name: 'Maximum new chunks' }).fill('1');
  await page.getByRole('button', { name: 'Render bounded index plan' }).click();
  await expect(page.getByRole('region', { name: 'Reviewed local intent' })).toContainText('at most 1 new chunks');
  await page.getByRole('button', { name: 'Confirm local action', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Confirm action', exact: true }).click();
  await expect(page.getByRole('status', { name: 'Local action receipt' })).toContainText('partial');
  expect(fixture.calls.filter(c => c.operation === 'index-apply')).toHaveLength(1);
  for (const [kind, id, title] of [['comment', 'COMMENT-1', 'Reply'], ['wiki', 'DOC-1', 'Design']] as const) {
    await page.getByRole('combobox', { name: 'Entity kind' }).selectOption(kind);
    await page.getByRole('button', { name: `${id} · ${title}` }).click();
    await expect(page.getByRole('region', { name: 'Local entity detail' })).toContainText(`${id} · ${title}`);
    await page.getByRole('button', { name: 'Edit entity', exact: true }).click();
    await page.getByRole('button', { name: 'Render tombstone plan' }).click();
    const intent = fixture.calls.filter(c => c.operation === 'mutation-plan').at(-1)!.body;
    expect(intent.mutation.operation).toBe(`delete_${kind}`); expect(intent.mutation).not.toHaveProperty('body');
    await page.getByRole('button', { name: 'Discard draft' }).click();
  }
});

test('read-only and absent capabilities do not offer writes or automatic setup', async ({ page }) => {
  const fixture = await localFixture(page, { readonly: true });
  await page.goto('/?view=Local+projects&project=lab&entity=ISSUE-1');
  await expect(page.getByRole('button', { name: 'Edit entity', exact: true })).toBeDisabled();
  await expect(page.getByRole('button', { name: 'New issue', exact: true })).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Render bounded index plan' })).toBeDisabled();
  await page.getByRole('combobox', { name: 'Local project' }).selectOption('other');
  await expect(page.getByRole('alert')).toContainText('configured project is unavailable');
  expect(fixture.calls.filter(c => c.body?.project === 'other')).toHaveLength(0);
  await localFixture(page, { enabled: false });
  await page.reload();
  await expect(page.getByText('Local projects capability unavailable', { exact: true })).toBeVisible();
  await expect(page.getByRole('region', { name: 'Local project CLI handoff' })).toContainText('gitcode-mcp project --help');
});

test('creates issue/comment/wiki with explicit intent and tombstones only after confirmation', async ({ page }) => {
  const fixture = await localFixture(page);
  await page.goto('/?view=Local+projects&project=lab');
  for (const [kind, id] of [['issue', 'ISSUE-2'], ['comment', 'COMMENT-2'], ['wiki', 'DOC-2']] as const) {
    await page.getByRole('combobox', { name: 'Entity kind' }).selectOption(kind);
    await page.getByRole('button', { name: `New ${kind}`, exact: true }).click();
    await page.getByRole('textbox', { name: 'Entity title' }).fill(`Created ${kind}`);
    await page.getByRole('textbox', { name: 'Entity body' }).fill(`Created ${kind} evidence`);
    if (kind === 'comment') await page.getByRole('textbox', { name: 'Parent issue ID' }).fill('ISSUE-1');
    await page.getByRole('button', { name: 'Render mutation plan' }).click();
    await page.getByRole('button', { name: 'Confirm local action', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: 'Confirm action', exact: true }).click();
    await expect(page.getByRole('status', { name: 'Local action receipt' })).toContainText('Confirmed');
    const intent = fixture.applies.at(-1)!.mutation;
    expect(intent.write_mode).toBe('local'); expect(intent.expected_head).toMatch(/^[0-9a-f]{40}$/); expect(intent.idempotency_key).not.toBe('');
    if (kind === 'comment') expect(intent.parent_id).toBe('ISSUE-1');
    await page.getByRole('button', { name: new RegExp(`${id} · Created ${kind}`) }).click();
    await expect(page.getByRole('region', { name: 'Local entity detail' })).toContainText(`Created ${kind} evidence`);
  }
  await page.getByRole('button', { name: 'Edit entity', exact: true }).click();
  await page.getByRole('button', { name: 'Render tombstone plan' }).click();
  expect(fixture.applies).toHaveLength(3);
  await page.getByRole('button', { name: 'Confirm local action', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Confirm action', exact: true }).click();
  expect(fixture.applies).toHaveLength(4); expect(fixture.applies[3].mutation.operation).toBe('delete_wiki');
  await expect(page.getByRole('region', { name: 'Local entity detail' })).toContainText('deleted');
  await expect(page.getByRole('button', { name: 'Edit entity', exact: true })).toBeDisabled();
});

test('unconfigured URL selectors never reach project operations; themes and narrow layouts retain labels', async ({ page }) => {
  const fixture = await localFixture(page);
  await page.goto('/?view=Local+projects&project=unconfigured&body=private-draft&q=private-query');
  await expect(page.getByRole('alert')).toContainText('Select a configured local project');
  expect(fixture.calls.every(c => c.operation === 'local-projects')).toBe(true);
  expect(page.url()).not.toContain('private-');
  await page.getByRole('combobox', { name: 'Local project' }).selectOption('lab');
  for (const theme of ['Light', 'Dark', 'System']) {
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.getByRole('radio', { name: theme, exact: true }).click();
    await expect(page.getByRole('radio', { name: theme, exact: true })).toHaveAttribute('aria-checked', 'true');
    for (const width of [390, 320]) {
      await page.setViewportSize({ width, height: 900 });
      await expect(page.getByRole('combobox', { name: 'Local project' })).toBeVisible();
      const overflow = await page.locator('body').evaluate(body => [...body.querySelectorAll('*')].filter(e => e.getBoundingClientRect().right > innerWidth + 1).map(e => ({ tag: e.tagName, class: e.className, right: Math.round(e.getBoundingClientRect().right) })));
      expect(overflow).toEqual([]);
    }
  }
});

test('late local reads cannot replace another view or write after navigation', async ({ page }) => {
  let release!: () => void;
  const pending = new Promise<void>(resolve => { release = resolve; });
  const fixture = await localFixture(page, { delayedList: pending });
  await page.goto('/?view=Local+projects&project=lab');
  await expect.poll(() => fixture.calls.some(c => c.operation === 'list')).toBe(true);
  await page.getByRole('button', { name: 'Overview', exact: true }).click();
  release();
  await expect(page.getByRole('heading', { name: 'Local operator console' })).toBeVisible();
  await expect(page.getByRole('region', { name: 'Local entity list' })).toHaveCount(0);
  expect(fixture.applies).toHaveLength(0);
});
