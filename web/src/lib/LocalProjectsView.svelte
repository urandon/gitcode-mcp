<script lang="ts">
  import { onDestroy, tick } from 'svelte';
  import { AlertTriangle, FileText, GitFork, RefreshCw, Search, ShieldCheck } from '@lucide/svelte';
  import { humanize, type Capability } from './admin';
  import StatusChip from './StatusChip.svelte';
  import { localEntityID, localMutation, localRequest, localSelector, LocalProjectError, type LocalCitation, type LocalEntity, type LocalIndexIntent, type LocalKind, type LocalMutationIntent, type LocalPlan, type LocalQuery, type LocalSearch, type LocalStatus } from './local-projects';

  export let ready = false;
  export let csrf = '';
  export let capabilities: Capability[] = [];
  export let query: LocalQuery;
  export let onquery: (next: LocalQuery) => void;

  let projects: LocalStatus[] = [];
  let entities: LocalEntity[] = [];
  let entity: LocalEntity | undefined;
  let history: LocalCitation[] | undefined;
  let result: LocalSearch | undefined;
  let searchText = '';
  let searchMode: 'fulltext' | 'hybrid' = 'hybrid';
  let maxChunks = 64;
  let draft: { title: string; body: string; state: string; parent: string } | undefined;
  let original: LocalEntity | undefined;
  let plan: LocalPlan | undefined;
  let pending: { kind: 'mutation' | 'index'; intent: LocalMutationIntent | LocalIndexIntent; attempted: boolean } | undefined;
  let busy = false;
  let loaded = false;
  let error = '';
  let receipt = '';
  let epoch = 0;
  let disposed = false;
  let controller: AbortController | undefined;
  let seenQuery = '';
  let current: LocalQuery = { project: '', kind: 'issue', id: '', offset: 0 };
  let dialog: HTMLDialogElement;
  let cancelButton: HTMLButtonElement;
  let trigger: HTMLButtonElement | undefined;
  $: enabled = capabilities.some(c => c.id === 'local_project_status' && c.ui_enabled);
  $: canWrite = capabilities.some(c => c.id === 'local_project_mutate' && c.ui_enabled);
  $: canIndex = capabilities.some(c => c.id === 'local_project_index' && c.ui_enabled);
  $: project = projects.find(p => p.project === current.project);
  $: locked = Boolean(draft || pending?.attempted || busy);
  $: if (ready && enabled && !loaded) { loaded = true; void refreshProjects(); }
  $: if (loaded && projects.length && JSON.stringify(query) !== seenQuery) {
    seenQuery = JSON.stringify(query);
    if (draft || pending?.attempted) {
      error = 'Finish or discard the draft before changing the target. An uncertain action must be retried with the same key.';
      onquery(current);
    } else void loadSelection(query);
  }

  function begin(): { token: number; signal: AbortSignal } {
    controller?.abort(); controller = new AbortController(); busy = true; error = '';
    return { token: ++epoch, signal: controller.signal };
  }
  const retain = (token: number) => !disposed && token === epoch;
  const message = (e: unknown) => e instanceof LocalProjectError ? e.message : 'Local project request is unavailable. Retain the draft and confirmed key.';
  async function refreshProjects(): Promise<void> {
    const { token, signal } = begin();
    try {
      const status = await localRequest<{ projects: LocalStatus[] }>('status', undefined, csrf, signal);
      if (!retain(token)) return;
      if (!Array.isArray(status.projects) || status.projects.length > 32 || status.projects.some(p => !localSelector(p.project))) throw new LocalProjectError();
      projects = status.projects;
      const next = { ...query, project: query.project || projects[0]?.project || '' };
      if (!draft && !pending?.attempted) {
        seenQuery = JSON.stringify(next); onquery(next);
        await loadSelection(next);
      }
    } catch (e) { if (retain(token)) error = message(e); }
    finally { if (retain(token)) busy = false; }
  }
  async function loadSelection(next: LocalQuery): Promise<void> {
    const { token, signal } = begin();
    current = { ...next }; seenQuery = JSON.stringify(next);
    entities = []; entity = undefined; history = undefined; result = undefined; plan = undefined; pending = undefined;
    const selected = projects.find(p => p.project === next.project);
    if (!selected?.readable) { busy = false; error = selected ? 'The configured project is unavailable. Use the CLI handoff.' : 'Select a configured local project; arbitrary identities are never sent.'; return; }
    try {
      const list = await localRequest<LocalEntity[]>('list', { project: next.project, kind: next.kind, limit: 20, offset: next.offset }, csrf, signal);
      if (!retain(token)) return;
      if (!Array.isArray(list) || list.length > 20) throw new LocalProjectError();
      entities = list;
      if (next.id) {
        const read = await localRequest<LocalEntity>('get', { project: next.project, kind: next.kind, id: next.id }, csrf, signal);
        if (retain(token)) entity = read;
      }
    } catch (e) { if (retain(token)) error = message(e); }
    finally { if (retain(token)) busy = false; }
  }
  function navigate(next: LocalQuery): void { if (!locked) { onquery(next); } }
  function edit(value?: LocalEntity): void {
    original = value ? structuredClone(value) : undefined;
    draft = { title: value?.metadata.title || '', body: value?.body || '', state: value?.metadata.state || (current.kind === 'issue' ? 'open' : 'active'), parent: value?.metadata.parent_id || '' };
    invalidate(); error = ''; receipt = '';
  }
  function invalidate(): void { if (!pending?.attempted) { plan = undefined; pending = undefined; receipt = ''; } }
  function discard(): void { if (pending?.attempted) return; draft = undefined; original = undefined; plan = undefined; pending = undefined; error = ''; }
  async function prepare(remove = false): Promise<void> {
    if (!project || !draft || project.read_only || !canWrite || pending?.attempted) return;
    const q = localMutation(project, current.kind, draft, crypto.randomUUID(), original, remove);
    const { token, signal } = begin(); plan = undefined; pending = undefined; receipt = '';
    try {
      const value = await localRequest<LocalPlan>('mutation-plan', q, csrf, signal);
      if (retain(token)) { plan = value; pending = { kind: 'mutation', intent: { ...q, plan_id: value.plan_id }, attempted: false }; }
    } catch (e) { if (retain(token)) error = message(e); }
    finally { if (retain(token)) busy = false; }
  }
  async function prepareIndex(): Promise<void> {
    if (!project || project.read_only || !canIndex || draft || pending?.attempted) return;
    const q: LocalIndexIntent = { project: current.project, write_mode: 'local', max_chunks: maxChunks, idempotency_key: crypto.randomUUID() };
    const { token, signal } = begin(); plan = undefined; pending = undefined; receipt = '';
    try {
      const value = await localRequest<LocalPlan>('index-plan', q, csrf, signal);
      if (retain(token)) { plan = value; pending = { kind: 'index', intent: { ...q, plan_id: value.plan_id }, attempted: false }; }
    } catch (e) { if (retain(token)) error = message(e); }
    finally { if (retain(token)) busy = false; }
  }
  async function confirm(event: MouseEvent): Promise<void> {
    trigger = event.currentTarget as HTMLButtonElement; dialog.showModal(); await tick(); cancelButton.focus();
  }
  async function closeDialog(): Promise<void> { dialog.close(); await tick(); trigger?.focus(); }
  async function apply(): Promise<void> {
    if (!pending || !plan || busy) return;
    const captured = structuredClone(pending); pending = { ...pending, attempted: true };
    const { token, signal } = begin();
    try {
      const value = await localRequest<{ entity?: LocalEntity; replayed?: boolean; commit?: string; state?: string; embedded_chunks?: number }>(`${captured.kind}-apply`, captured.intent, csrf, signal);
      if (!retain(token)) return;
      const key = captured.kind === 'mutation' ? (captured.intent as LocalMutationIntent).mutation.idempotency_key : (captured.intent as LocalIndexIntent).idempotency_key;
      receipt = `${value.replayed ? 'Replayed original result' : 'Confirmed'} · ${value.entity?.citation.commit || value.commit || ''} · key ${key}${captured.kind === 'index' ? ` · ${value.state} · ${value.embedded_chunks} new chunks` : ''}`;
      pending = undefined; plan = undefined; draft = undefined; original = undefined;
      await closeDialog(); await refreshProjects();
    } catch (e) {
      if (!retain(token)) return;
      error = message(e);
      if (e instanceof LocalProjectError && !e.uncertain) { pending = undefined; plan = undefined; await closeDialog(); }
      // An ambiguous delivery retains the immutable intent and key. No automatic
      // replay and no editable fields until explicit retry resolves the result.
    } finally { if (retain(token)) busy = false; }
  }
  async function loadHistory(): Promise<void> {
    if (!entity) return;
    const { token, signal } = begin();
    try {
      const value = await localRequest<LocalCitation[]>('history', { project: current.project, kind: current.kind, id: entity.metadata.id, max_commits: 64 }, csrf, signal);
      if (retain(token)) history = value;
    } catch (e) { if (retain(token)) error = message(e); }
    finally { if (retain(token)) busy = false; }
  }
  async function inspectCurrent(): Promise<void> {
    if (!original || pending?.attempted) return;
    const { token, signal } = begin();
    try {
      const value = await localRequest<LocalEntity>('get', { project: current.project, kind: current.kind, id: original.metadata.id }, csrf, signal);
      if (retain(token)) entity = value;
    } catch (e) { if (retain(token)) error = message(e); }
    finally { if (retain(token)) busy = false; }
  }
  function adoptInspectedRevision(): void {
    if (!entity || !original || pending?.attempted) return;
    original = structuredClone(entity); invalidate();
    error = 'Inspected revision adopted explicitly. The draft text is unchanged; render and review a new plan.';
  }
  async function search(): Promise<void> {
    const { token, signal } = begin(); result = undefined;
    try {
      const value = await localRequest<LocalSearch>('search', { project: current.project, query: searchText, mode: searchMode, limit: 8 }, csrf, signal);
      if (retain(token)) result = value;
    } catch (e) { if (retain(token)) error = message(e); }
    finally { if (retain(token)) busy = false; }
  }
  onDestroy(() => { disposed = true; epoch++; controller?.abort(); });
</script>

<div class="local-projects">
  <div class="intro"><p class="eyebrow">GIT-BACKED HARNESS</p><h1>Local projects</h1><p>Issues, comments and wiki in Git. No GitCode access, credential discovery or implicit setup.</p></div>
  {#if !ready}
    <p role="status">Loading Local projects capability…</p>
  {:else if !enabled}
    <section class="state-panel warning-panel" role="status"><ShieldCheck size={22} /><div><strong>Local projects capability unavailable</strong><p>The default local host is private IPC only. Start the local host with explicit <code>gitcode-mcp service run --admin</code>, then use <code>gitcode-mcp admin open</code>.</p></div></section>
  {:else}
    {#if error}<section class="state-panel danger-panel" role="alert"><AlertTriangle size={22} /><div><strong>Local project action unavailable</strong><p>{error}</p></div></section>{/if}
    {#if receipt}<section class="state-panel" role="status" aria-label="Local action receipt"><ShieldCheck size={22} /><div><strong>Local action receipt</strong><p>{receipt}</p><p>A replay is the original result, not proof of current HEAD. Current state is refreshed separately.</p></div></section>{/if}
    <section class="control-workbench" aria-label="Local project selection" aria-busy={busy}>
      <div class="control-heading"><div><GitFork size={22} /><div><h2>Configured projects</h2><p>Only trusted opaque project selectors cross this browser boundary.</p></div></div><button class="text-action" disabled={locked} onclick={refreshProjects}><RefreshCw size={16} />Refresh projects</button></div>
      <div class="control-form">
        <label><span>Local project</span><select aria-label="Local project" value={current.project} disabled={locked} onchange={e => navigate({ project: e.currentTarget.value, kind: 'issue', id: '', offset: 0 })}><option value="">Select configured project</option>{#each projects as p}<option value={p.project}>{p.project} · {p.state}</option>{/each}</select></label>
        <label><span>Entity kind</span><select aria-label="Entity kind" value={current.kind} disabled={locked} onchange={e => navigate({ ...current, kind: e.currentTarget.value as LocalKind, id: '', offset: 0 })}><option value="issue">Issues</option><option value="comment">Comments</option><option value="wiki">Wiki</option></select></label>
      </div>
      {#if project}<div class="local-identity"><StatusChip value={project.state} /><span>Project {project.project_id}</span><code>HEAD {project.commit || 'unavailable'}</code>{#if project.last_conflict}<span>{humanize(project.last_conflict)}</span>{/if}</div>{/if}
      {#if loaded && !projects.length && !busy}<p>No configured local projects. Use the CLI handoff below.</p>{/if}
    </section>
    {#if project?.readable}
      <section class="control-workbench" aria-label="Local entity list">
        <div class="section-heading"><h2>{humanize(current.kind)} collection</h2><button class="primary-action" disabled={locked || project.read_only || !canWrite} onclick={() => edit()}>New {current.kind}</button></div>
        {#if !entities.length && !busy}<p>No entities in this bounded page.</p>{/if}
        <ul class="local-entity-list">{#each entities as item}<li><button disabled={locked} aria-current={current.id === item.metadata.id ? 'true' : undefined} onclick={() => navigate({ ...current, id: item.metadata.id })}><FileText size={16} /><span><strong>{item.metadata.id} · {item.metadata.title}</strong><small>{item.metadata.state}</small></span></button></li>{/each}</ul>
        <div class="local-actions"><button class="text-action" disabled={locked || current.offset === 0} onclick={() => navigate({ ...current, id: '', offset: Math.max(0, current.offset - 20) })}>Previous page</button><span>Offset {current.offset} · up to 20 entities</span><button class="text-action" disabled={locked || entities.length < 20 || current.offset >= 100000} onclick={() => navigate({ ...current, id: '', offset: current.offset + 20 })}>Next page</button></div>
      </section>
      {#if entity}
        <section class="control-workbench" aria-label="Local entity detail"><div class="section-heading"><h2>{entity.metadata.id} · {entity.metadata.title}</h2><StatusChip value={entity.metadata.state} /></div><pre class="local-body">{entity.body}</pre><p class="local-citation">Git citation · {entity.citation.project_id} · {entity.citation.path} · commit {entity.citation.commit} · blob {entity.citation.blob}</p><div class="local-actions"><button class="text-action" disabled={locked || project.read_only || !canWrite || entity.metadata.state === 'deleted'} onclick={() => edit(entity)}>Edit entity</button><button class="text-action" disabled={busy || Boolean(pending?.attempted)} onclick={loadHistory}>Read bounded history</button></div>
          {#if history}<p>Up to 64 first-parent commits inspected; citations below are exact entity revisions.</p><ol class="local-history">{#each history as citation}<li><code>{citation.commit}</code><code>{citation.blob}</code><span>{citation.path}</span></li>{/each}</ol>{/if}
        </section>
      {/if}
      {#if draft}
        <section class="control-workbench" aria-label="Local entity editor"><h2>{original ? `Edit ${original.metadata.id}` : `New ${current.kind}`}</h2><p>Explicit replacement of editable fields. Existing labels, milestone and links are preserved. Drafts stay in memory only.</p>
          <fieldset class="local-draft" disabled={busy || Boolean(pending?.attempted)} oninput={invalidate}>
            <label>Title<input aria-label="Entity title" bind:value={draft.title} maxlength="1024" /></label>
            <label>Body<textarea aria-label="Entity body" bind:value={draft.body} rows="10" maxlength="262144"></textarea></label>
            {#if original}<label>State<select aria-label="Entity state" bind:value={draft.state} onchange={invalidate}>{#if current.kind === 'issue'}<option value="open">Open</option><option value="closed">Closed</option>{:else}<option value="active">Active</option>{/if}</select></label>{:else if current.kind === 'comment'}<label>Parent issue ID<input aria-label="Parent issue ID" bind:value={draft.parent} placeholder="ISSUE-1" /></label>{/if}
          </fieldset>
          <p class="local-citation">Expected {original ? `blob ${original.citation.blob}` : `HEAD ${project.commit}`}. On conflict the draft and original revision remain unchanged.</p>
          <div class="local-actions"><button class="text-action" disabled={busy || Boolean(pending?.attempted)} onclick={discard}>Discard draft</button><button class="primary-action" disabled={busy || Boolean(pending?.attempted) || !draft.title || (!original && current.kind === 'comment' && !localEntityID(draft.parent, 'issue'))} onclick={() => prepare()}>Render mutation plan</button>{#if original}<button class="text-action" disabled={busy || Boolean(pending?.attempted)} onclick={() => prepare(true)}>Render tombstone plan</button>{/if}</div>
          {#if original}<div class="local-actions"><button class="text-action" disabled={busy || Boolean(pending?.attempted)} onclick={inspectCurrent}>Inspect current entity; retain draft</button>{#if entity && entity.citation.blob !== original.citation.blob}<button class="text-action" disabled={busy || Boolean(pending?.attempted) || entity.metadata.state === 'deleted'} onclick={adoptInspectedRevision}>Use inspected revision for this draft</button>{/if}</div>{/if}
        </section>
      {/if}
      <section class="control-workbench" aria-label="Local project search"><div class="control-heading"><div><Search size={22} /><div><h2>Search Git content</h2><p>Lexical reads need no index. Hybrid sends only the query to the configured provider and visibly falls back when coverage is missing or stale.</p></div></div></div>
        <form class="control-form" onsubmit={e => { e.preventDefault(); void search(); }}><label><span>Query</span><input aria-label="Local search query" bind:value={searchText} maxlength="4096" /></label><label><span>Mode</span><select aria-label="Local search mode" bind:value={searchMode}><option value="hybrid">Hybrid requested</option><option value="fulltext">Explicit full-text</option></select></label><button class="primary-action" disabled={busy || !searchText.trim() || Boolean(pending?.attempted)}>Search local project</button></form>
        {#if result}<div role="status"><p>{humanize(result.requested_mode)} → {humanize(result.effective_mode)} · RAG {humanize(result.index_state)} · {humanize(result.fallback || 'no fallback')}</p><code class="local-citation">Commit {result.commit}</code></div>{#if !result.hits.length}<p>No current Git match. This is a valid empty result.</p>{/if}<ul class="local-results">{#each result.hits as hit}<li><button class="text-action" disabled={locked} onclick={() => navigate({ ...current, kind: hit.citation.kind, id: hit.citation.id, offset: 0 })}>{hit.citation.id}</button><pre class="local-body">{hit.snippet}</pre><p class="local-citation">{hit.citation.project_id} · {hit.citation.path} · lines {hit.line_start}–{hit.line_end} · commit {hit.citation.commit} · blob {hit.citation.blob}</p></li>{/each}</ul>{/if}
        <div class="control-form"><label><span>Maximum new chunks</span><input aria-label="Maximum new chunks" type="number" min="1" max="128" bind:value={maxChunks} disabled={busy || Boolean(pending?.attempted)} oninput={invalidate} /></label><div class="form-actions"><button class="text-action" disabled={locked || project.read_only || !canIndex || !Number.isInteger(maxChunks) || maxChunks < 1 || maxChunks > 128} onclick={prepareIndex}>Render bounded index plan</button></div></div><p>No model install, download, provider start or automatic repair. Partial indexing is resumable through another explicitly confirmed bounded call.</p>
      </section>
      {#if plan && pending}<section class="control-workbench" aria-label="Reviewed local intent"><h2>Reviewed local intent</h2><p>{humanize(plan.operation)} · {plan.project_id} · {plan.target_id || 'configured project'}</p><p class="local-citation">Plan {plan.plan_id}<br />Commit {plan.commit}<br />Boundary {plan.data_boundary}{#if plan.max_chunks} · at most {plan.max_chunks} new chunks{/if}</p><p class="local-citation">Retry key: {pending.kind === 'mutation' ? (pending.intent as LocalMutationIntent).mutation.idempotency_key : (pending.intent as LocalIndexIntent).idempotency_key}</p><ul>{#each plan.effects as effect}<li>{humanize(effect)}</li>{/each}</ul>{#if pending.attempted}<p role="status">Uncertain action: editing and target changes are locked. Retry reuses the exact confirmed intent and key.</p>{/if}<button class="primary-action" disabled={busy} onclick={confirm}>{pending.attempted ? 'Retry exact confirmed action' : 'Confirm local action'}</button></section>{/if}
    {/if}
  {/if}
  <section class="control-workbench" aria-label="Local project CLI handoff"><h2>Trusted setup stays in the CLI</h2><p>Storage registration, initialization, bundle export/restore, history rewriting and model provisioning are not browser actions. Inspect <code>gitcode-mcp project --help</code> with trusted machine configuration. No filesystem path or endpoint is accepted here.</p></section>
</div>
<dialog class="confirmation-dialog local-confirmation" bind:this={dialog} oncancel={e => { e.preventDefault(); if (!busy) void closeDialog(); }} aria-labelledby="local-confirm-title">
  <div class="dialog-icon"><ShieldCheck size={22} /></div><div><p class="section-kicker">CONFIRM LOCAL INTENT</p><h2 id="local-confirm-title">Apply this local project plan?</h2><p>The current Git revision is checked again. After uncertain delivery, retry only the exact intent and key. Nothing contacts GitCode.</p>{#if plan}<dl><div><dt>Project</dt><dd>{plan.project_id}</dd></div><div><dt>Plan</dt><dd>{plan.plan_id}</dd></div><div><dt>Boundary</dt><dd>{plan.data_boundary}</dd></div></dl>{/if}{#if error}<p role="alert">{error}</p>{/if}<div class="dialog-actions"><button class="text-action" bind:this={cancelButton} disabled={busy} onclick={closeDialog}>Keep current state</button><button class="primary-action" disabled={busy} onclick={apply}>{pending?.attempted ? 'Retry same intent and key' : 'Confirm action'}</button></div></div>
</dialog>
