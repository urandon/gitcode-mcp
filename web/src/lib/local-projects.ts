export type LocalKind = 'issue' | 'comment' | 'wiki';
export type LocalQuery = { project: string; kind: LocalKind; id: string; offset: number };
export type LocalStatus = { project: string; project_id: string; readable: boolean; read_only: boolean; state: string; commit?: string; diagnostic_code?: string; last_conflict?: string };
export type LocalCitation = { project_id: string; kind: LocalKind; id: string; commit: string; blob: string; path: string };
export type LocalEntity = { metadata: { schema: number; id: string; kind: LocalKind; title: string; state: string; parent_id?: string; labels?: string[]; milestone?: string; links?: Array<{ kind: string; id: string }> }; body: string; citation: LocalCitation };
export type LocalMutation = { operation: string; id?: string; parent_id?: string; title?: string; body?: string; state?: string; labels?: string[]; milestone?: string; links?: LocalEntity['metadata']['links']; expected_head?: string; expected_revision?: string; idempotency_key: string; write_mode: 'local' };
export type LocalMutationIntent = { project: string; mutation: LocalMutation; plan_id?: string };
export type LocalIndexIntent = { project: string; write_mode: 'local'; max_chunks: number; idempotency_key: string; plan_id?: string };
export type LocalPlan = { plan_id: string; project: string; project_id: string; operation: string; target_id?: string; commit: string; data_boundary: string; max_chunks?: number; effects: string[] };
export type LocalSearch = { project: string; commit: string; requested_mode: string; effective_mode: string; index_state: string; fallback?: string; hits: Array<{ citation: LocalCitation; snippet: string; line_start: number; line_end: number }> };
export const emptyLocalQuery = (): LocalQuery => ({ project: '', kind: 'issue', id: '', offset: 0 });
export const localSelector = (value: string): boolean => /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(value);
export const localEntityID = (value: string, kind: LocalKind): boolean => new RegExp(`^${{ issue: 'ISSUE', comment: 'COMMENT', wiki: 'DOC' }[kind]}-[1-9][0-9]{0,15}$`).test(value) && Number.isSafeInteger(Number(value.split('-')[1]));

export function readLocalQuery(params: URLSearchParams): LocalQuery {
  const kind = params.get('local_kind');
  const q = emptyLocalQuery();
  q.kind = kind === 'comment' || kind === 'wiki' ? kind : 'issue';
  const project = params.get('project') || '';
  if (localSelector(project)) q.project = project;
  const id = params.get('entity') || '';
  if (localEntityID(id, q.kind)) q.id = id;
  const offset = Number(params.get('local_offset'));
  if (Number.isSafeInteger(offset) && offset >= 0 && offset <= 100000) q.offset = offset;
  return q;
}
export function writeLocalQuery(params: URLSearchParams, q: LocalQuery): void {
  if (localSelector(q.project)) params.set('project', q.project);
  if (q.kind !== 'issue') params.set('local_kind', q.kind);
  if (localEntityID(q.id, q.kind)) params.set('entity', q.id);
  if (q.offset > 0 && q.offset <= 100000) params.set('local_offset', String(q.offset));
}

// Updates replace editable fields. Preserve labels, links and milestone even
// when this compact editor only changes the title/body/state.
export function localMutation(project: LocalStatus, kind: LocalKind, draft: { title: string; body: string; state: string; parent: string }, key: string, original?: LocalEntity, remove = false): LocalMutationIntent {
  const mutation: LocalMutation = { operation: original ? `${remove ? 'delete' : 'update'}_${kind}` : kind === 'comment' ? 'add_comment' : `create_${kind}`, idempotency_key: key, write_mode: 'local' };
  if (original) { mutation.id = original.metadata.id; mutation.expected_revision = original.citation.blob; }
  else { mutation.expected_head = project.commit; if (kind === 'comment') mutation.parent_id = draft.parent; }
  if (!remove) {
    mutation.title = draft.title; mutation.body = draft.body;
    if (original) { mutation.state = draft.state; mutation.labels = original.metadata.labels; mutation.milestone = original.metadata.milestone; mutation.links = original.metadata.links; }
  }
  return { project: project.project, mutation };
}

const failures: Record<string, string> = {
  local_git_write_conflict: 'Git revision changed. Your draft is retained; inspect the latest revision before rendering a new plan.',
  local_git_stale_plan: 'Confirmed intent or Git revision changed. Your draft is retained; render a fresh plan.',
  local_git_read_only: 'This project registration is read-only.',
  local_git_idempotency_conflict: 'This retry key belongs to another intent. Inspect current state before preparing a new action.',
  local_git_writer_busy: 'Writer busy. Retry the exact confirmed action and key.',
  local_git_publication_ambiguous: 'Publication is uncertain. Retry the exact confirmed action and key; do not generate another key.',
  local_git_invalid_argument: 'Correct the typed request fields; your draft is retained.',
  local_git_limit_exceeded: 'The bounded request or response exceeds its limit. Use a smaller page or draft.',
  local_git_not_found: 'The selected entity is unavailable.',
  local_git_unknown_project: 'The configured project is unavailable. Refresh the project list.',
  local_git_invalid_configuration: 'Trusted local configuration is unavailable. Use the CLI handoff.',
  local_git_history_unavailable: 'The bounded history cannot establish a complete result.',
  capability_unavailable: 'Local projects are unavailable in this daemon. Use the local CLI configuration.',
  unauthorized: 'Admin session expired. Open a new protected Admin session.',
  csrf_validation_failed: 'Admin session validation failed. Reload the protected session.'
};
export class LocalProjectError extends Error {
  constructor(public code = 'local_git_unavailable', public uncertain = false) { super(failures[code] || 'Local project request failed. Retain the draft and confirmed key; inspect current state.'); }
}
export async function localRequest<T>(operation: string, input: unknown, csrf: string, signal?: AbortSignal): Promise<T> {
  const controller = new AbortController();
  const abort = () => controller.abort();
  signal?.addEventListener('abort', abort, { once: true });
  if (signal?.aborted) controller.abort();
  const timer = setTimeout(abort, 12000);
  try {
    const status = operation === 'status';
    const response = await fetch(`/api/admin/v1/local-projects${status ? '' : `/${operation}`}`, { method: status ? 'GET' : 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf }, ...(status ? {} : { body: JSON.stringify(input) }), signal: controller.signal, cache: 'no-store' });
    const reader = response.body?.getReader();
    if (!reader) throw new LocalProjectError(undefined, true);
    const chunks: Uint8Array[] = []; let length = 0;
    for (;;) { const next = await reader.read(); if (next.done) break; length += next.value.length; if (length > (64 << 20)) { await reader.cancel(); throw new LocalProjectError('local_git_limit_exceeded', true); } chunks.push(next.value); }
    const bytes = new Uint8Array(length); let offset = 0; for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length; }
    let value: { api_version?: string; result?: T; error?: { code?: string } };
    try { value = JSON.parse(new TextDecoder().decode(bytes)); } catch { throw new LocalProjectError(undefined, true); }
    if (!response.ok) {
      const code = typeof value.error?.code === 'string' && Object.hasOwn(failures, value.error.code) ? value.error.code : 'local_git_unavailable';
      throw new LocalProjectError(code, response.status >= 500 || code === 'local_git_writer_busy');
    }
    if (value.api_version !== '1' || value.result === undefined) throw new LocalProjectError(undefined, true);
    return value.result;
  } catch (error) { if (error instanceof LocalProjectError) throw error; throw new LocalProjectError(undefined, true); }
  finally { clearTimeout(timer); signal?.removeEventListener('abort', abort); }
}
