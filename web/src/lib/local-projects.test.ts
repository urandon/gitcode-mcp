import { afterEach, describe, expect, it, vi } from 'vitest';
import { localEntityID, localMutation, localRequest, LocalProjectError, readLocalQuery, writeLocalQuery, type LocalEntity, type LocalStatus } from './local-projects';

afterEach(() => vi.unstubAllGlobals());
describe('Local project protected browser contract', () => {
  it('round trips only bounded selectors and never a draft, path, query, endpoint or key', () => {
    const input = new URLSearchParams({ project: 'lab', local_kind: 'wiki', entity: 'DOC-1', local_offset: '20', body: 'private-marker', q: 'private-marker', path: 'private-marker', idempotency_key: 'private-marker' });
    const q = readLocalQuery(input);
    expect(q).toEqual({ project: 'lab', kind: 'wiki', id: 'DOC-1', offset: 20 });
    const out = new URLSearchParams(); writeLocalQuery(out, q);
    expect(out.toString()).not.toContain('private-marker'); expect(out.size).toBe(4);
    expect(readLocalQuery(new URLSearchParams('project=%2Fprivate&entity=ISSUE-0&local_offset=9007199254740992'))).toEqual({ project: '', kind: 'issue', id: '', offset: 0 });
    expect(localEntityID('ISSUE-9007199254740991', 'issue')).toBe(true); expect(localEntityID('ISSUE-9007199254740992', 'issue')).toBe(false);
  });
  it('preserves replacement metadata and exact blob while creates use HEAD and deletes omit source payload', () => {
    const project: LocalStatus = { project: 'lab', project_id: 'stable', readable: true, read_only: false, state: 'ready', commit: 'head' };
    const original: LocalEntity = { metadata: { schema: 1, id: 'COMMENT-1', kind: 'comment', title: 'Before', state: 'active', parent_id: 'ISSUE-1', labels: ['evidence'], milestone: 'phase-1', links: [{ kind: 'wiki', id: 'DOC-1' }] }, body: 'Before body', citation: { project_id: 'stable', kind: 'comment', id: 'COMMENT-1', commit: 'old', blob: 'blob', path: 'issues/ISSUE-1/comments/COMMENT-1.md' } };
    const draft = { title: 'After', body: 'After body', state: 'active', parent: 'ISSUE-1' };
    const edit = localMutation(project, 'comment', draft, 'key', original);
    expect(edit.mutation).toEqual({ operation: 'update_comment', id: 'COMMENT-1', expected_revision: 'blob', write_mode: 'local', idempotency_key: 'key', title: 'After', body: 'After body', state: 'active', labels: ['evidence'], milestone: 'phase-1', links: [{ kind: 'wiki', id: 'DOC-1' }] });
    expect(localMutation(project, 'comment', draft, 'key').mutation).toMatchObject({ operation: 'add_comment', expected_head: 'head', parent_id: 'ISSUE-1' });
    expect(localMutation(project, 'wiki', draft, 'key').mutation.operation).toBe('create_wiki');
    const deleted = localMutation(project, 'comment', draft, 'key', original, true);
    expect(deleted.mutation).toEqual({ operation: 'delete_comment', id: 'COMMENT-1', expected_revision: 'blob', write_mode: 'local', idempotency_key: 'key' });
    expect(original.body).toBe('Before body');
  });
  it('uses explicit POST plus CSRF for content reads and never query-string bodies', async () => {
    const fetcher = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ api_version: '1', result: [] }))));
    vi.stubGlobal('fetch', fetcher);
    await expect(localRequest('list', { project: 'lab', kind: 'issue' }, 'session-csrf')).resolves.toEqual([]);
    expect(fetcher).toHaveBeenCalledWith('/api/admin/v1/local-projects/list', expect.objectContaining({ method: 'POST', cache: 'no-store', body: '{"project":"lab","kind":"issue"}', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': 'session-csrf' } }));
    await localRequest('status', undefined, 'session-csrf');
    expect(fetcher.mock.calls[1][0]).toBe('/api/admin/v1/local-projects'); expect(fetcher.mock.calls[1][1].method).toBe('GET');
  });
  it('never renders raw error bodies and distinguishes definite conflict from uncertain delivery', async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ error: { code: 'local_git_write_conflict', message: 'private-marker', path: 'private-marker' } }), { status: 409 }))
      .mockResolvedValueOnce(new Response('private-provider-body', { status: 503 }))
      .mockRejectedValueOnce(new Error('private-network-coordinate'));
    vi.stubGlobal('fetch', fetcher);
    for (const uncertain of [false, true, true]) {
      try { await localRequest('mutation-apply', {}, 'csrf'); throw new Error('expected rejection'); }
      catch (error) { expect(error).toBeInstanceOf(LocalProjectError); const value = error as LocalProjectError; expect(value.uncertain).toBe(uncertain); expect(value.message).not.toMatch(/private/); }
    }
    expect(fetcher).toHaveBeenCalledTimes(3); // no automatic retry
  });
});
