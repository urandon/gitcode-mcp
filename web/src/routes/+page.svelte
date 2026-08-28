<script lang="ts">
  import './+page.css';
  import { onDestroy, onMount, tick } from 'svelte';
  import { Activity, AlertTriangle, ArrowLeft, Blocks, CheckCircle2, ChevronRight, CircleGauge, Clipboard, Clock3, Database, FileCheck2, FileText, FolderCog, Gauge, GitFork, HeartPulse, History, Layers3, Monitor, Moon, Power, RefreshCw, RotateCcw, Search, ShieldCheck, SlidersHorizontal, Sun, Wrench, XCircle, Zap } from '@lucide/svelte';
  import { applyTheme, normalizeTheme, themeStorageKey, type Theme } from '$lib/theme';
  import { adminApiVersion, cliHandoff, emptySnapshot, humanize, isSnapshotStale, laneSummary, type AdminView, type BindingIntent, type BindingPlan, type CacheObservation, type ControlFailure, type ControlReceipt, type Diagnostic, type Job, type JobAction, type JobActionReceipt, type Maintenance, type MaintenanceIntent, type MaintenancePlan, type ObservationSnapshot, type ProviderSmoke, type RAGRepairPlan, type Repository, type RepositoryDocsSearchResult, type RepositoryTab, type SearchComparison } from '$lib/admin';
  import CoverageLaneCard from '$lib/CoverageLaneCard.svelte';
  import StatusChip from '$lib/StatusChip.svelte';

  const navigation: Array<{ name: AdminView; icon: typeof CircleGauge }> = [
    { name: 'Overview', icon: CircleGauge }, { name: 'Caches', icon: Database }, { name: 'Jobs', icon: Activity }, { name: 'Maintenance', icon: Wrench }, { name: 'Diagnostics', icon: HeartPulse }
  ];
  const repositoryTabs: Array<{ value: RepositoryTab; label: string }> = [
    { value: 'coverage', label: 'Coverage' }, { value: 'collections', label: 'Collections' }, { value: 'documentation', label: 'Documentation' }, { value: 'search', label: 'Search status' }, { value: 'activity', label: 'Activity' }
  ];
  const themes = [
    { value: 'light' as Theme, label: 'Light', icon: Sun }, { value: 'dark' as Theme, label: 'Dark', icon: Moon }, { value: 'system' as Theme, label: 'System', icon: Monitor }
  ];
  const activeJobStates = new Set(['queued', 'running']);

  let theme: Theme = 'system';
  let active: AdminView = 'Overview';
  let selectedCacheRef = '';
  let selectedRepoID = '';
  let repoTab: RepositoryTab = 'coverage';
  let diagnosticsFilter: 'current' | 'recovered' | 'all' = 'current';
  let jobStateFilter = '';
  let jobTypeFilter = '';
  let jobCacheFilter = '';
  let jobRepoFilter = '';
  let jobFailureFilter = '';
  let selectedJobID = '';
  let csrfToken = '';
  let pendingConfirmation: JobAction | '' = '';
  let actionRunning = false;
  let actionError = '';
  let actionReceipt: JobActionReceipt | undefined;
  let pendingIdempotencyKey = '';
  let actionTriggerButton: HTMLButtonElement | undefined;
  let confirmationDialog: HTMLDialogElement | undefined;
  let confirmActionButton: HTMLButtonElement | undefined;
  let loading = true;
  let error = '';
  let copied = '';
  let snapshot: ObservationSnapshot = structuredClone(emptySnapshot);
  let eventStream: EventSource | undefined;
  let selectedCache: CacheObservation | undefined;
  let selectedRepo: Repository | undefined;
  let activeJobs = snapshot.jobs.filter((job) => activeJobStates.has(job.status));
  let failedJobs = snapshot.jobs.filter((job) => job.status === 'failed');
  let scopedJobs = snapshot.jobs;
  let visibleDiagnostics = snapshot.diagnostics;
  let filteredJobs = snapshot.jobs;
  let selectedJob: Job | undefined;
  let stale = false;
  let maintenanceTargetKey = '';
  let maintenanceIntent: MaintenanceIntent = { cache_ref: '', repo_id: '', sync_mode: 'head-and-backfill', collections: ['issues', 'wiki'], rag_mode: 'off' };
  let maintenancePlan: MaintenancePlan | undefined;
  let maintenanceReceipt: ControlReceipt | undefined;
  let maintenanceError = '';
  let maintenanceFailure: ControlFailure | undefined;
  let bindingIntent: BindingIntent = { cache_ref: '', repo_id: '', scopes: ['issues'], aliases: [] };
  let bindingPlan: BindingPlan | undefined;
  let bindingReceipt: ControlReceipt | undefined;
  let bindingError = '';
  let controlRunning = false;
  let pendingControl: 'maintenance_apply' | 'binding_apply' | 'rag_repair_apply' | 'disable' | 'reconcile' | 'repository_docs_index' | '' = '';
  let pendingControlKey = '';
  let controlDialog: HTMLDialogElement | undefined;
  let controlConfirmButton: HTMLButtonElement | undefined;
  let controlTriggerButton: HTMLButtonElement | undefined;
  let selectedMaintenance: Maintenance | undefined;
  let maintenanceControlsEnabled = false;
  let bindingControlsEnabled = false;
  let registrationControlsEnabled = false;
  let searchCompareEnabled = false;
  let providerSmokeEnabled = false;
  let ragRepairEnabled = false;
  let searchQuery = '';
  let searchKind = '';
  let searchProvenance = '';
  let searchLimit = 8;
  let searchRunning = false;
  let searchError = '';
  let searchComparison: SearchComparison | undefined;
  let providerSmoke: ProviderSmoke | undefined;
  let repairProfile = '';
  let repairMaxChunks = 128;
  let repairPlan: RAGRepairPlan | undefined;
  let repairReceipt: ControlReceipt | undefined;
  let experimentCopied = false;
  let repositoryDocsSearchEnabled = false;
  let repositoryDocsQuery = '';
  let repositoryDocsRevision = 'HEAD';
  let repositoryDocsMode: 'hybrid' | 'fulltext' = 'hybrid';
  let repositoryDocsLimit = 8;
  let repositoryDocsIncludeWorktree = false;
  let repositoryDocsRunning = false;
  let repositoryDocsError = '';
  let repositoryDocsResult: RepositoryDocsSearchResult | undefined;
  let repoTargets: Array<{ key: string; cache: CacheObservation; repo: Repository }> = [];

  $: selectedCache = snapshot.caches.find((cache) => cache.cache_ref === selectedCacheRef);
  $: selectedRepo = selectedCache?.repositories.find((repo) => repo.repo_id === selectedRepoID);
  $: activeJobs = snapshot.jobs.filter((job) => activeJobStates.has(job.status));
  $: failedJobs = snapshot.jobs.filter((job) => job.status === 'failed');
  $: scopedJobs = snapshot.jobs.filter((job) => job.cache_ref === selectedCache?.cache_ref && job.repo_id === selectedRepo?.repo_id);
  $: visibleDiagnostics = snapshot.diagnostics.filter((item) => diagnosticsFilter === 'all' || (diagnosticsFilter === 'current' ? item.current : !item.current));
  $: filteredJobs = snapshot.jobs.filter((job) => (!jobStateFilter || job.status === jobStateFilter) && (!jobTypeFilter || job.type === jobTypeFilter) && (!jobCacheFilter || job.cache_ref === jobCacheFilter) && (!jobRepoFilter || job.repo_id === jobRepoFilter) && (!jobFailureFilter || job.failure_class === jobFailureFilter));
  $: selectedJob = snapshot.jobs.find((job) => job.id === selectedJobID);
  $: stale = snapshot.revision !== '' && isSnapshotStale(snapshot.generated_at);
  $: selectedMaintenance = snapshot.maintenance.find((item) => `${item.cache_ref}\u0000${item.repo_id}` === maintenanceTargetKey);
  $: maintenanceControlsEnabled = snapshot.capabilities.some((item) => item.id === 'admin_maintenance_plan_apply' && item.ui_enabled);
  $: bindingControlsEnabled = snapshot.capabilities.some((item) => item.id === 'admin_binding_plan_apply' && item.ui_enabled);
  $: registrationControlsEnabled = snapshot.capabilities.some((item) => item.id === 'admin_registration_controls' && item.ui_enabled);
  $: searchCompareEnabled = snapshot.capabilities.some((item) => item.id === 'admin_search_compare' && item.ui_enabled);
  $: providerSmokeEnabled = snapshot.capabilities.some((item) => item.id === 'admin_provider_smoke' && item.ui_enabled);
  $: ragRepairEnabled = snapshot.capabilities.some((item) => item.id === 'admin_rag_bounded_repair' && item.ui_enabled);
  $: repositoryDocsSearchEnabled = snapshot.capabilities.some((item) => item.id === 'repository_docs_search' && item.ui_enabled);
  $: repoTargets = snapshot.caches.flatMap((cache) => cache.repositories.map((repo) => ({ key: `${cache.cache_ref}\u0000${repo.repo_id}`, cache, repo })));

  function normalizeSnapshot(value: ObservationSnapshot): ObservationSnapshot {
    value.attention ||= []; value.caches ||= []; value.jobs ||= []; value.maintenance ||= []; value.diagnostics ||= []; value.capabilities ||= [];
    value.job_retention ||= structuredClone(emptySnapshot.job_retention); value.job_retention.retained_by_status ||= [];
    for (const cache of value.caches) for (const repo of (cache.repositories ||= [])) {
      repo.collections ||= []; repo.recent_sync_events ||= []; repo.execution ||= {}; repo.counts.by_kind ||= [];
      repo.documentation ||= { state: 'not_indexed', registered: false, overlay: false, eligible_files: 0, eligible_chunks: 0, embedded_chunks: 0, reused_chunks: 0, failed_chunks: 0, missing_objects: 0, revision_set_count: 0, search_available: false };
    }
    return value;
  }

  function retentionDuration(seconds: number): string {
    if (!seconds) return 'Not reported';
    if (seconds % 86400 === 0) return `${seconds / 86400} days`;
    if (seconds % 3600 === 0) return `${seconds / 3600} hours`;
    return `${seconds} seconds`;
  }

  function retainedStatusSummary(): string {
    const counts = snapshot.job_retention.retained_by_status;
    return counts.length ? counts.map(({ status, count }) => `${humanize(status)} ${count}`).join(' · ') : 'No retained jobs';
  }

  function uniqueJobValues(values: Array<string | undefined>): string[] {
    return [...new Set(values.filter((value): value is string => Boolean(value)))].sort();
  }

  async function establishSession(): Promise<void> {
    const launchToken = new URLSearchParams(location.hash.slice(1)).get('launch');
    const response = launchToken
      ? await fetch('/api/admin/v1/session', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ launch_token: launchToken }) })
      : await fetch('/api/admin/v1/session');
    if (launchToken) history.replaceState(null, '', location.pathname + location.search);
    if (!response.ok) {
      if (launchToken) throw new Error('The launch link is invalid or has expired. Run admin open again.');
      return;
    }
    try {
      const session = await response.json();
      csrfToken = session.csrf_token || '';
    } catch {
      if (launchToken) throw new Error('The launch response was not compatible with this admin UI.');
    }
  }

  async function refresh(): Promise<void> {
    loading = true; error = '';
    try {
      const response = await fetch('/api/admin/v1/snapshot');
      if (!response.ok) throw new Error(response.status === 401 ? 'Admin session required. Run admin open again.' : 'Observation is unavailable.');
      snapshot = normalizeSnapshot(await response.json());
      if (selectedCacheRef && selectedRepoID) maintenanceTargetKey = `${selectedCacheRef}\u0000${selectedRepoID}`;
      ensureControlSelections();
    } catch (value) { error = value instanceof Error ? value.message : 'Observation is unavailable.'; }
    finally { loading = false; }
  }

  function connectEvents(): void {
    eventStream?.close(); eventStream = new EventSource('/api/admin/v1/events');
    for (const kind of ['snapshot_changed', 'snapshot_required']) eventStream.addEventListener(kind, () => void refresh());
  }

  function hydrateLocation(): void {
    const params = new URLSearchParams(location.search);
    const requestedView = params.get('view');
    if (navigation.some((item) => item.name === requestedView)) active = requestedView as AdminView;
    selectedCacheRef = params.get('cache') || ''; selectedRepoID = params.get('repo') || '';
    const requestedTab = params.get('tab');
    if (repositoryTabs.some((item) => item.value === requestedTab)) repoTab = requestedTab as RepositoryTab;
    searchQuery = (params.get('q') || '').slice(0, 512); searchKind = params.get('kind') || ''; searchProvenance = params.get('provenance') || '';
    const requestedLimit = Number(params.get('limit')); if (Number.isInteger(requestedLimit) && requestedLimit >= 1 && requestedLimit <= 20) searchLimit = requestedLimit;
    const requestedFilter = params.get('diagnostics');
    if (requestedFilter === 'current' || requestedFilter === 'recovered' || requestedFilter === 'all') diagnosticsFilter = requestedFilter;
    selectedJobID = params.get('job') || '';
    jobStateFilter = params.get('job_state') || ''; jobTypeFilter = params.get('job_type') || '';
    jobCacheFilter = params.get('job_cache') || ''; jobRepoFilter = params.get('job_repo') || ''; jobFailureFilter = params.get('job_failure') || '';
  }

  function updateLocation(replace = false): void {
    const params = new URLSearchParams();
    if (active !== 'Overview') params.set('view', active);
    if (selectedCacheRef) params.set('cache', selectedCacheRef);
    if (selectedRepoID) params.set('repo', selectedRepoID);
    if (selectedRepoID && repoTab !== 'coverage') params.set('tab', repoTab);
    if (selectedRepoID && repoTab === 'search') {
      if (searchQuery) params.set('q', searchQuery);
      if (searchKind) params.set('kind', searchKind);
      if (searchProvenance) params.set('provenance', searchProvenance);
      if (searchLimit !== 8) params.set('limit', String(searchLimit));
    }
    if (active === 'Diagnostics' && diagnosticsFilter !== 'current') params.set('diagnostics', diagnosticsFilter);
    if (active === 'Jobs') {
      if (selectedJobID) params.set('job', selectedJobID);
      if (jobStateFilter) params.set('job_state', jobStateFilter);
      if (jobTypeFilter) params.set('job_type', jobTypeFilter);
      if (jobCacheFilter) params.set('job_cache', jobCacheFilter);
      if (jobRepoFilter) params.set('job_repo', jobRepoFilter);
      if (jobFailureFilter) params.set('job_failure', jobFailureFilter);
    }
    history[replace ? 'replaceState' : 'pushState'](null, '', `${location.pathname}${params.size ? `?${params}` : ''}`);
  }

  function selectView(view: AdminView): void {
    active = view;
    if (view !== 'Caches') { selectedCacheRef = ''; selectedRepoID = ''; }
    if (view !== 'Jobs') selectedJobID = '';
    updateLocation();
  }
  function openRepository(cache: CacheObservation, repo: Repository): void { active = 'Caches'; selectedCacheRef = cache.cache_ref; selectedRepoID = repo.repo_id; maintenanceTargetKey = `${cache.cache_ref}\u0000${repo.repo_id}`; repoTab = 'coverage'; searchComparison = undefined; searchError = ''; providerSmoke = undefined; repairPlan = undefined; repairReceipt = undefined; repositoryDocsResult = undefined; repositoryDocsError = ''; updateLocation(); }
  function closeRepository(): void { selectedRepoID = ''; repoTab = 'coverage'; updateLocation(); }
  function selectRepositoryTab(value: RepositoryTab): void { repoTab = value; updateLocation(); }
  function selectDiagnosticFilter(value: 'current' | 'recovered' | 'all'): void { diagnosticsFilter = value; updateLocation(); }
  function setJobFilter(kind: 'state' | 'type' | 'cache' | 'repo' | 'failure', value: string): void {
    if (kind === 'state') jobStateFilter = value; else if (kind === 'type') jobTypeFilter = value; else if (kind === 'cache') jobCacheFilter = value; else if (kind === 'repo') jobRepoFilter = value; else jobFailureFilter = value;
    selectedJobID = ''; updateLocation();
  }
  function openJob(job: Job): void { selectedJobID = job.id; pendingConfirmation = ''; pendingIdempotencyKey = ''; actionError = ''; actionReceipt = undefined; updateLocation(); }
  function closeJob(): void { selectedJobID = ''; pendingConfirmation = ''; pendingIdempotencyKey = ''; actionError = ''; actionReceipt = undefined; updateLocation(); }
  function openRepositoryDocsJobs(): void {
    if (!selectedCache || !selectedRepo) return;
    active = 'Jobs'; jobCacheFilter = selectedCache.cache_ref; jobRepoFilter = selectedRepo.repo_id; jobTypeFilter = 'repository-docs-index';
    const latest = [...scopedJobs].filter((job) => job.type === 'repository-docs-index').sort((a, b) => b.updated_at.localeCompare(a.updated_at))[0];
    selectedJobID = latest?.id || ''; updateLocation();
  }
  async function confirmJobAction(action: JobAction, trigger: HTMLButtonElement): Promise<void> {
    actionTriggerButton = trigger; pendingConfirmation = action; pendingIdempotencyKey = `admin-${action}-${crypto.randomUUID()}`; actionError = ''; actionReceipt = undefined;
    await tick(); confirmationDialog?.showModal(); confirmActionButton?.focus();
  }
  async function cancelJobConfirmation(): Promise<void> { confirmationDialog?.close(); pendingConfirmation = ''; pendingIdempotencyKey = ''; await tick(); actionTriggerButton?.focus(); }
  async function executeJobAction(): Promise<void> {
    if (!selectedJob || !pendingConfirmation || !csrfToken) return;
    const action = pendingConfirmation;
    actionRunning = true; actionError = '';
    try {
      const response = await fetch(`/api/admin/v1/jobs/${encodeURIComponent(selectedJob.id)}/${action}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
        body: JSON.stringify({ idempotency_key: pendingIdempotencyKey })
      });
      const payload = await response.json();
      if (!response.ok) throw new Error([payload.error?.message, payload.error?.remediation].filter(Boolean).join(' '));
      actionReceipt = payload.receipt; confirmationDialog?.close(); pendingConfirmation = ''; pendingIdempotencyKey = ''; await tick(); actionTriggerButton?.focus();
      await refresh();
    } catch (value) { actionError = value instanceof Error ? value.message : 'The job action failed.'; }
    finally { actionRunning = false; }
  }
  async function copyCommand(diagnostic: Diagnostic): Promise<void> {
    try { await navigator.clipboard.writeText(cliHandoff(diagnostic)); copied = diagnostic.id; window.setTimeout(() => (copied = ''), 1800); }
    catch { copied = ''; }
  }

  async function copyDocumentationCommand(kind: 'index' | 'search'): Promise<void> {
    const command = kind === 'index' ? selectedRepo?.documentation.index_handoff : selectedRepo?.documentation.search_handoff;
    if (!command) return;
    try { await navigator.clipboard.writeText(command); copied = `documentation-${kind}`; window.setTimeout(() => (copied = ''), 1800); }
    catch { copied = ''; }
  }

  async function runRepositoryDocsSearch(): Promise<void> {
    if (!csrfToken || !repositoryDocsSearchEnabled || !selectedMaintenance || !repositoryDocsQuery.trim()) return;
    repositoryDocsRunning = true; repositoryDocsError = ''; repositoryDocsResult = undefined;
    try {
      repositoryDocsResult = await controlPost<RepositoryDocsSearchResult>(`/api/admin/v1/repository-docs/${encodeURIComponent(selectedMaintenance.registration_id)}/search`, {
        query: repositoryDocsQuery.trim(), revision: repositoryDocsRevision.trim(), mode: repositoryDocsMode,
        limit: repositoryDocsLimit, include_worktree: repositoryDocsIncludeWorktree
      });
    } catch (value) { repositoryDocsError = value instanceof Error ? value.message : 'Repository documentation search failed.'; }
    finally { repositoryDocsRunning = false; }
  }

  function ensureControlSelections(): void {
    const targets = snapshot.caches.flatMap((cache) => cache.repositories.map((repo) => ({ key: `${cache.cache_ref}\u0000${repo.repo_id}`, cache, repo })));
    if (!targets.some((target) => target.key === maintenanceTargetKey)) {
      const registration = snapshot.maintenance[0];
      const target = registration ? targets.find((item) => item.cache.cache_ref === registration.cache_ref && item.repo.repo_id === registration.repo_id) : targets[0];
      if (target) loadMaintenanceTarget(target.key);
    }
    if (!bindingIntent.cache_ref && snapshot.caches[0]) bindingIntent = { ...bindingIntent, cache_ref: snapshot.caches[0].cache_ref };
  }

  function loadMaintenanceTarget(key: string): void {
    maintenanceTargetKey = key;
    const [cacheRef, repoID] = key.split('\u0000');
    const registration = snapshot.maintenance.find((item) => item.cache_ref === cacheRef && item.repo_id === repoID);
    const repo = snapshot.caches.find((item) => item.cache_ref === cacheRef)?.repositories.find((item) => item.repo_id === repoID);
    maintenanceIntent = {
      cache_ref: cacheRef, repo_id: repoID,
      sync_mode: registration?.policy.sync_enabled === false ? 'off' : registration?.policy.sync_mode || 'head-and-backfill',
      collections: registration?.policy.collections?.length ? [...registration.policy.collections] : [...(repo?.scopes || ['issues'])],
      rag_mode: registration?.policy.rag_enabled ? 'maintain' : 'off', profile: registration?.policy.profile || '',
      head_interval_seconds: registration?.policy.head_interval_seconds || 0, rag_interval_seconds: registration?.policy.rag_interval_seconds || 0,
      head_max_pages: registration?.policy.head_max_pages || 0, tail_slice_pages: registration?.policy.tail_slice_pages || 0, per_page: registration?.policy.per_page || 0
    };
    maintenancePlan = undefined; maintenanceReceipt = undefined; maintenanceError = ''; maintenanceFailure = undefined;
  }

  function toggleCollection(name: string, checked: boolean): void {
    const next = new Set(maintenanceIntent.collections);
    if (checked) next.add(name); else next.delete(name);
    maintenanceIntent = { ...maintenanceIntent, collections: [...next] };
    invalidateMaintenancePlan();
  }

  function invalidateMaintenancePlan(): void {
    maintenancePlan = undefined; maintenanceReceipt = undefined; maintenanceError = ''; maintenanceFailure = undefined;
  }

  function invalidateBindingPlan(): void {
    bindingPlan = undefined; bindingReceipt = undefined; bindingError = '';
  }

  function loadBinding(cache: CacheObservation, repo?: Repository): void {
    const [owner = '', name = ''] = (repo?.repo_id || '').split('/');
    bindingIntent = { cache_ref: cache.cache_ref, repo_id: repo?.repo_id || '', owner, name, api_base_url: '', scopes: [...(repo?.scopes || ['issues'])], aliases: [...(repo?.aliases || [])], display_name: repo?.display_name || '' };
    bindingPlan = undefined; bindingReceipt = undefined; bindingError = '';
  }

  async function controlPost<T>(path: string, body: unknown): Promise<T> {
    const response = await fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken }, body: JSON.stringify(body) });
    const payload = await response.json();
    if (!response.ok) {
      const failure = (payload.error || { code: 'control_failed', message: 'The control request failed.' }) as ControlFailure;
      const message = [failure.message || 'The control request failed.', failure.remediation].filter(Boolean).join(' ');
      throw Object.assign(new Error(message), { failure });
    }
    return payload.result as T;
  }

  async function renderMaintenancePlan(): Promise<void> {
    if (!csrfToken || !maintenanceControlsEnabled) return;
    controlRunning = true; maintenanceError = ''; maintenanceFailure = undefined; maintenanceReceipt = undefined;
    try { maintenancePlan = await controlPost<MaintenancePlan>('/api/admin/v1/maintenance/plan', maintenanceIntent); }
    catch (value) {
      maintenancePlan = undefined;
      maintenanceFailure = value instanceof Error && 'failure' in value ? (value as Error & { failure: ControlFailure }).failure : undefined;
      maintenanceError = maintenanceFailure?.message || (value instanceof Error ? value.message : 'Maintenance planning failed.');
    }
    finally { controlRunning = false; }
  }

  async function renderBindingPlan(): Promise<void> {
    if (!csrfToken || !bindingControlsEnabled) return;
    controlRunning = true; bindingError = ''; bindingReceipt = undefined;
    try { bindingPlan = await controlPost<BindingPlan>('/api/admin/v1/bindings/plan', bindingIntent); }
    catch (value) { bindingPlan = undefined; bindingError = value instanceof Error ? value.message : 'Binding planning failed.'; }
    finally { controlRunning = false; }
  }

  async function runSearchComparison(): Promise<void> {
    if (!csrfToken || !searchCompareEnabled || !selectedCache || !selectedRepo || !searchQuery.trim()) return;
    searchRunning = true; searchError = ''; searchComparison = undefined; providerSmoke = undefined; repairPlan = undefined; repairReceipt = undefined;
    updateLocation(true);
    try {
      searchComparison = await controlPost<SearchComparison>('/api/admin/v1/search/compare', { cache_ref: selectedCache.cache_ref, repo_id: selectedRepo.repo_id, query: searchQuery.trim(), kind: searchKind, provenance: searchProvenance, limit: searchLimit });
      repairProfile = snapshot.maintenance.find((item) => item.cache_ref === selectedCache?.cache_ref && item.repo_id === selectedRepo?.repo_id)?.policy.profile || '';
    } catch (value) { searchError = value instanceof Error ? value.message : 'Search comparison failed.'; }
    finally { searchRunning = false; }
  }

  async function smokeSearchProvider(): Promise<void> {
    if (!csrfToken || !providerSmokeEnabled || !selectedCache || !selectedRepo) return;
    searchRunning = true; searchError = '';
    try { providerSmoke = await controlPost<ProviderSmoke>('/api/admin/v1/rag/provider/smoke', { cache_ref: selectedCache.cache_ref, repo_id: selectedRepo.repo_id, profile: repairProfile }); }
    catch (value) { searchError = value instanceof Error ? value.message : 'Provider smoke test failed.'; }
    finally { searchRunning = false; }
  }

  async function renderRAGRepairPlan(): Promise<void> {
    if (!csrfToken || !ragRepairEnabled || !selectedCache || !selectedRepo) return;
    searchRunning = true; searchError = ''; repairReceipt = undefined;
    try { repairPlan = await controlPost<RAGRepairPlan>('/api/admin/v1/rag/repair/plan', { cache_ref: selectedCache.cache_ref, repo_id: selectedRepo.repo_id, profile: repairProfile, max_chunks: repairMaxChunks }); }
    catch (value) { repairPlan = undefined; searchError = value instanceof Error ? value.message : 'RAG repair planning failed.'; }
    finally { searchRunning = false; }
  }

  function invalidateRAGRepairPlan(): void {
    repairPlan = undefined;
    repairReceipt = undefined;
  }

  async function copyExperimentSummary(): Promise<void> {
    if (!searchComparison) return;
    const hybrid = searchComparison.hybrid;
    const lines = [`Search experiment: ${searchComparison.repo_id}`, `Query: ${searchComparison.query}`, `Requested/effective: ${hybrid.requested_mode}/${hybrid.effective_mode}`, `RAG: ${hybrid.rag_state}${hybrid.fallback_reason ? ` (${hybrid.fallback_reason})` : ''}`, `Coverage: ${hybrid.coverage.embedded_chunks}/${hybrid.coverage.eligible_chunks}; missing ${hybrid.coverage.missing_chunks}; stale ${hybrid.coverage.stale_chunks}`, ...hybrid.results.slice(0, 5).map((item) => `#${item.rank} ${item.id} fusion=${item.match.fusion_score.toFixed(6)} ${item.path}${item.line_start ? `:${item.line_start}` : ''}`)];
    try { await navigator.clipboard.writeText(lines.join('\n')); experimentCopied = true; window.setTimeout(() => (experimentCopied = false), 1800); } catch { experimentCopied = false; }
  }

  async function confirmControl(kind: typeof pendingControl, trigger: HTMLButtonElement): Promise<void> {
    controlTriggerButton = trigger; pendingControl = kind; pendingControlKey = `admin-${kind}-${crypto.randomUUID()}`;
    maintenanceError = ''; maintenanceFailure = undefined; bindingError = ''; await tick(); controlDialog?.showModal(); controlConfirmButton?.focus();
  }

  async function cancelControlConfirmation(): Promise<void> {
    controlDialog?.close(); pendingControl = ''; pendingControlKey = ''; await tick(); controlTriggerButton?.focus();
  }

  async function executeControl(): Promise<void> {
    if (!pendingControl || !csrfToken) return;
    controlRunning = true;
    try {
      if (pendingControl === 'maintenance_apply' && maintenancePlan) {
        maintenanceReceipt = await controlPost<ControlReceipt>('/api/admin/v1/maintenance/apply', { ...maintenanceIntent, plan_id: maintenancePlan.plan_id, idempotency_key: pendingControlKey });
      } else if (pendingControl === 'binding_apply' && bindingPlan) {
        bindingReceipt = await controlPost<ControlReceipt>('/api/admin/v1/bindings/apply', { ...bindingIntent, plan_id: bindingPlan.plan_id, idempotency_key: pendingControlKey });
      } else if (pendingControl === 'rag_repair_apply' && repairPlan && selectedCache && selectedRepo) {
        repairReceipt = await controlPost<ControlReceipt>('/api/admin/v1/rag/repair/apply', { cache_ref: selectedCache.cache_ref, repo_id: selectedRepo.repo_id, profile: repairProfile, max_chunks: repairMaxChunks, plan_id: repairPlan.plan_id, idempotency_key: pendingControlKey });
      } else if ((pendingControl === 'disable' || pendingControl === 'reconcile') && selectedMaintenance) {
        maintenanceReceipt = await controlPost<ControlReceipt>(`/api/admin/v1/maintenance/${encodeURIComponent(selectedMaintenance.registration_id)}/${pendingControl}`, { idempotency_key: pendingControlKey });
      } else if (pendingControl === 'repository_docs_index' && selectedMaintenance) {
        maintenanceReceipt = await controlPost<ControlReceipt>(`/api/admin/v1/repository-docs/${encodeURIComponent(selectedMaintenance.registration_id)}/index`, { idempotency_key: pendingControlKey });
      }
      controlDialog?.close(); pendingControl = ''; pendingControlKey = ''; await refresh(); await tick(); controlTriggerButton?.focus();
    } catch (value) {
      const message = value instanceof Error ? value.message : 'The confirmed control failed.';
      if (pendingControl !== 'binding_apply' && pendingControl !== 'rag_repair_apply') {
        maintenanceFailure = value instanceof Error && 'failure' in value ? (value as Error & { failure: ControlFailure }).failure : undefined;
      }
      if (pendingControl === 'binding_apply') bindingError = message; else if (pendingControl === 'rag_repair_apply') searchError = message; else maintenanceError = maintenanceFailure?.message || message;
    } finally { controlRunning = false; }
  }
  function selectTheme(value: Theme): void { theme = value; applyTheme(value); }
  function onPopState(): void { hydrateLocation(); }

  onMount(async () => {
    theme = normalizeTheme(localStorage.getItem(themeStorageKey)); applyTheme(theme); hydrateLocation(); window.addEventListener('popstate', onPopState);
    try { await establishSession(); await refresh(); connectEvents(); }
    catch (value) { error = value instanceof Error ? value.message : 'Admin session is unavailable.'; loading = false; }
  });
  onDestroy(() => { eventStream?.close(); window.removeEventListener('popstate', onPopState); });
</script>

<svelte:head><title>gitcode-mcp · Local operator console</title></svelte:head>

<div class="shell">
  <aside class="sidebar">
    <a class="brand" href="/" aria-label="gitcode-mcp overview"><span class="brand-mark"><Blocks size={18} strokeWidth={2.2} /></span><span>gitcode-mcp</span></a>
    <nav aria-label="Admin sections">
      {#each navigation as item}<button class:active={active === item.name} aria-current={active === item.name ? 'page' : undefined} onclick={() => selectView(item.name)} title={item.name}><item.icon size={18} strokeWidth={1.8} /><span>{item.name}</span></button>{/each}
    </nav>
    <div class="theme-control"><span class="theme-label">Theme</span><div class="theme-options" role="radiogroup" aria-label="Color theme">{#each themes as item}<button class:selected={theme === item.value} role="radio" aria-checked={theme === item.value} onclick={() => selectTheme(item.value)}><item.icon size={14} />{item.label}</button>{/each}</div><p>System is the default.</p></div>
  </aside>

  <main>
    <header class="topbar"><div class="crumb"><span>Admin</span><ChevronRight size={14} /><strong>{active}</strong></div><div class="version">v{snapshot.service.version || 'dev'}</div></header>
    <section class="content" aria-busy={loading}>
      {#if snapshot.api_version !== adminApiVersion}
        <section class="state-panel danger-panel" role="alert"><AlertTriangle size={22} /><div><strong>UI/API version mismatch</strong><p>This UI expects API v{adminApiVersion}, but the daemon returned v{snapshot.api_version || 'unknown'}. Update browser assets and daemon together.</p></div></section>
      {:else}
        {#if error}<section class="state-panel danger-panel" role="alert"><AlertTriangle size={22} /><div><strong>Observation unavailable</strong><p>{error}</p></div></section>{/if}
        {#if stale}<section class="state-panel warning-panel" role="status"><Clock3 size={21} /><div><strong>Snapshot may be stale</strong><p>Last observation was {new Date(snapshot.generated_at).toLocaleString()}. Recheck before acting.</p></div></section>{/if}

        {#if active === 'Overview'}
          <div class="intro"><p class="eyebrow">LOCAL ADMIN</p><h1>Local operator console</h1><p>Readiness, coverage, active work, and the next safe action across every managed local cache.</p></div>
          {#if loading && !snapshot.revision}<div class="loading-grid" aria-label="Loading operator snapshot"><span></span><span></span><span></span></div>
          {:else}
            <section class="section-block" aria-labelledby="attention-title">
              <div class="section-heading"><div><p class="section-kicker">NEEDS ATTENTION</p><h2 id="attention-title">Attention queue</h2></div><span class="count-badge">{snapshot.attention.length}</span></div>
              {#if snapshot.attention.length === 0}<div class="empty-inline"><CheckCircle2 size={19} /><div><strong>No current blockers</strong><span>Nothing requires an operator decision in this snapshot.</span></div></div>
              {:else}<div class="attention-list">{#each snapshot.attention as item}<article class="attention-item"><AlertTriangle size={18} /><div><strong>{item.message}</strong><span>{humanize(item.entity_type)} · {item.entity_id} · {humanize(item.code)}</span>{#if item.remediation}<p>{item.remediation}</p>{/if}</div><StatusChip value={item.severity} /></article>{/each}</div>{/if}
            </section>

            <section class="section-block" aria-labelledby="readiness-title">
              <div class="section-heading"><div><p class="section-kicker">CURRENT TRUTH</p><h2 id="readiness-title">Readiness</h2></div><span class="as-of">As of {new Date(snapshot.generated_at).toLocaleTimeString()}</span></div>
              <div class="status-list">
                <div class="status-row"><span class:ok={snapshot.service.running} class="status-icon"><Activity size={19} /></span><div><strong>Daemon</strong><span>{snapshot.service.protocol} · {snapshot.service.installed ? humanize(snapshot.service.install_kind) : 'foreground or unmanaged'}</span></div><StatusChip value={snapshot.service.running ? 'running' : 'unavailable'} /></div>
                <div class="status-row"><span class:ok={snapshot.service.admin_secure} class="status-icon"><ShieldCheck size={19} /></span><div><strong>Admin session</strong><span>Loopback-only, bounded, authenticated observation</span></div><StatusChip value={snapshot.service.admin_secure ? 'secure' : 'blocked'} /></div>
                <div class="status-row"><span class:ok={snapshot.caches.length > 0 && snapshot.caches.every((cache) => cache.readiness === 'ready')} class="status-icon"><Database size={19} /></span><div><strong>Cache estate</strong><span>{snapshot.caches.length} caches · {snapshot.caches.reduce((total, cache) => total + cache.repository_count, 0)} repositories</span></div><StatusChip value={snapshot.caches.length === 0 ? 'unavailable' : snapshot.caches.every((cache) => cache.readiness === 'ready') ? 'ready' : 'degraded'} /></div>
              </div>
            </section>

            <section class="section-block" aria-labelledby="active-title">
              <div class="section-heading"><div><p class="section-kicker">EXECUTION</p><h2 id="active-title">Active work</h2></div><span class="count-badge">{activeJobs.length}</span></div>
              {#if activeJobs.length === 0}<div class="empty-inline"><Clock3 size={19} /><div><strong>No queued or running jobs</strong><span>The daemon is idle; terminal history remains in Jobs.</span></div></div>
              {:else}<div class="work-list">{#each activeJobs as job}<article><div class="work-icon"><RefreshCw size={17} class={job.status === 'running' ? 'spin' : ''} /></div><div><strong>{humanize(job.type)}</strong><span>{job.cache_ref || 'unscoped'} · {job.repo_id || 'service-wide'} · {job.completed || 0}/{job.steps || '—'}</span></div><StatusChip value={job.status} /></article>{/each}</div>{/if}
            </section>

            <section class="section-block" aria-labelledby="cache-summary-title">
              <div class="section-heading"><div><p class="section-kicker">TOPOLOGY & COVERAGE</p><h2 id="cache-summary-title">Managed caches</h2></div><button class="text-action" onclick={() => selectView('Caches')}>View topology<ChevronRight size={15} /></button></div>
              {#if snapshot.caches.length === 0}<div class="empty-state"><Database size={24} /><h3>No managed caches</h3><p>Enroll cache maintenance from the CLI before the console can observe repositories.</p></div>
              {:else}<div class="cache-summary-grid">{#each snapshot.caches as cache}<article class="cache-summary"><div class="card-heading"><div><span class="entity-icon"><Database size={17} /></span><div><strong>{cache.cache_ref}</strong><span>{cache.repository_count} repositories · schema {cache.schema_version || 'unknown'}</span></div></div><StatusChip value={cache.readiness} /></div><div class="cohort-strip">{#each ['head', 'tail', 'secondary', 'rag'] as lane}{@const observed = cache.repositories[0]?.coverage[lane as 'head' | 'tail' | 'secondary' | 'rag']}<div><span>{humanize(lane)}</span><strong>{observed ? humanize(observed.state) : 'No repo'}</strong></div>{/each}</div><button class="card-link" onclick={() => { active = 'Caches'; selectedCacheRef = cache.cache_ref; selectedRepoID = ''; updateLocation(); }}>Inspect cache<ChevronRight size={15} /></button></article>{/each}</div>{/if}
            </section>

            {#if snapshot.diagnostics.some((item) => !item.current)}<section class="section-block compact-section" aria-labelledby="recovered-title"><div class="section-heading"><div><p class="section-kicker">RECOVERY</p><h2 id="recovered-title">Recently recovered</h2></div></div><div class="recovered-list">{#each snapshot.diagnostics.filter((item) => !item.current).slice(0, 3) as item}<div><CheckCircle2 size={17} /><span><strong>{humanize(item.failure_class)}</strong>{item.entity_id}</span><time>{item.observed_at ? new Date(item.observed_at).toLocaleString() : 'Recovered'}</time></div>{/each}</div></section>{/if}
            <div class="actions"><button class="primary" onclick={refresh} disabled={loading}><RefreshCw size={16} class={loading ? 'spin' : ''} />Refresh snapshot</button><button class="secondary" onclick={() => selectView('Diagnostics')}><FolderCog size={16} />Open diagnostics</button></div>
            <p class="privacy"><ShieldCheck size={15} />Bound to localhost. Credentials, raw content, and absolute cache paths are never exposed.</p>
          {/if}

        {:else if active === 'Caches'}
          {#if selectedRepo}
            <div class="entity-header"><button class="back-link" onclick={closeRepository}><ArrowLeft size={16} />All caches</button><div class="intro section-intro"><p class="eyebrow">REPOSITORY</p><h1>{selectedRepo.display_name || selectedRepo.repo_id}</h1><p>{selectedCache?.cache_ref} · {selectedRepo.scopes?.join(', ') || 'cached observation'} · {humanize(selectedRepo.binding_state)}</p></div></div>
            <div class="tab-list" role="tablist" aria-label="Repository details">{#each repositoryTabs as tab}<button role="tab" aria-selected={repoTab === tab.value} class:active={repoTab === tab.value} onclick={() => selectRepositoryTab(tab.value)}>{tab.label}</button>{/each}</div>
            {#if repoTab === 'coverage'}
              <section class="repository-section" aria-labelledby="coverage-title"><div class="section-heading"><div><p class="section-kicker">INDEPENDENT LANES</p><h2 id="coverage-title">Corpus coverage</h2></div></div><div class="coverage-grid">{#each Object.entries(selectedRepo.coverage) as [name, lane]}<CoverageLaneCard {name} {lane} />{/each}</div></section>
              <section class="repository-section" aria-labelledby="execution-title"><div class="section-heading"><div><p class="section-kicker">SEPARATE FROM COVERAGE</p><h2 id="execution-title">Execution context</h2></div></div><div class="context-grid"><article><span>Active jobs</span><strong>{selectedRepo.execution.active_job_ids?.length || 0}</strong><p>{selectedRepo.execution.active_job_ids?.join(', ') || 'No current work'}</p></article><article><span>Contention</span><strong>{selectedRepo.execution.contention ? humanize(selectedRepo.execution.contention.state) : 'None'}</strong><p>{selectedRepo.execution.contention?.operation ? `Waiting on ${selectedRepo.execution.contention.operation}` : 'No writer contention observed'}</p></article><article><span>Scheduled retry</span><strong>{selectedRepo.execution.scheduled_retry ? humanize(selectedRepo.execution.scheduled_retry.stage) : 'None'}</strong><p>{selectedRepo.execution.scheduled_retry ? new Date(selectedRepo.execution.scheduled_retry.at).toLocaleString() : 'No backoff scheduled'}</p></article></div>{#if selectedRepo.execution.last_stage_errors?.length}<div class="historical-note"><History size={17} /><div><strong>Last stage error is historical context</strong><p>{selectedRepo.execution.last_stage_errors.map((item) => `${humanize(item.stage)}: ${humanize(item.failure_class)}`).join(' · ')}. It does not replace current lane truth.</p></div></div>{/if}</section>
            {:else if repoTab === 'collections'}
              <section class="repository-section" aria-labelledby="collections-title"><div class="section-heading"><div><p class="section-kicker">CORPUS</p><h2 id="collections-title">Collections and frontiers</h2></div></div>{#if selectedRepo.collections.length === 0}<div class="empty-state"><Layers3 size={24} /><h3>No collection observations</h3><p>The binding exists, but no counts or frontiers have been observed.</p></div>{:else}<div class="table-wrap"><table><caption class="sr-only">Cached collection counts and coverage</caption><thead><tr><th>Collection</th><th>Cached</th><th>Head freshness</th><th>Tail completeness</th></tr></thead><tbody>{#each selectedRepo.collections as collection}<tr><th scope="row">{humanize(collection.kind)}</th><td>{collection.count.toLocaleString()}</td><td><StatusChip value={collection.head.state} label={laneSummary('head', collection.head)} /></td><td><StatusChip value={collection.tail.state} label={laneSummary('tail', collection.tail)} /></td></tr>{/each}</tbody></table></div>{/if}<div class="secondary-summary"><div><span>Secondary total</span><strong>{selectedRepo.counts.secondary.total.toLocaleString()}</strong></div><div><span>Pending</span><strong>{selectedRepo.counts.secondary.pending.toLocaleString()}</strong></div><div><span>Deferred</span><strong>{selectedRepo.counts.secondary.deferred.toLocaleString()}</strong></div><div><span>Complete</span><strong>{selectedRepo.counts.secondary.complete.toLocaleString()}</strong></div></div></section>
            {:else if repoTab === 'documentation'}
              <section class="repository-section" aria-labelledby="documentation-title">
                <div class="section-heading"><div><p class="section-kicker">VERSIONED GIT AUTHORITY</p><h2 id="documentation-title">Repository documentation RAG</h2><p>Policy is committed with the repository. The cache keeps revision membership and vectors only; source text is hydrated from the exact Git blob or an explicit tracked worktree overlay.</p></div><StatusChip value={selectedRepo.documentation.state} /></div>
                <div class="context-grid">
                  <article><span>Revision sets</span><strong>{selectedRepo.documentation.revision_set_count}</strong><p>{selectedRepo.documentation.revision_set_id || 'No index published'}</p></article>
                  <article><span>Coverage</span><strong>{selectedRepo.documentation.embedded_chunks + selectedRepo.documentation.reused_chunks}/{selectedRepo.documentation.eligible_chunks}</strong><p>{selectedRepo.documentation.eligible_files} files · {selectedRepo.documentation.failed_chunks} failed · {selectedRepo.documentation.missing_objects} missing</p></article>
                  <article><span>Authority</span><strong>{selectedRepo.documentation.overlay ? 'Tracked overlay' : 'Committed Git'}</strong><p>{selectedRepo.documentation.commit_oid ? selectedRepo.documentation.commit_oid.slice(0, 12) : 'Run an index job from a worktree'}</p></article>
                  <article><span>Policy</span><strong>{humanize(selectedRepo.documentation.policy_source || 'default preset')}</strong><p>{selectedRepo.documentation.policy_hash ? selectedRepo.documentation.policy_hash.slice(0, 16) : '.gitcode/gitcode-mcp.yaml or conventional docs preset'}</p></article>
                  <article><span>Embedding namespace</span><strong>{selectedRepo.documentation.namespace_id ? 'Bound' : 'Not selected'}</strong><p>{selectedRepo.documentation.namespace_id || 'Provider namespace appears after indexing'}</p></article>
                  <article><span>Updated</span><strong>{selectedRepo.documentation.updated_at ? new Date(selectedRepo.documentation.updated_at).toLocaleDateString() : 'Never'}</strong><p>{selectedRepo.documentation.updated_at ? new Date(selectedRepo.documentation.updated_at).toLocaleString() : 'No repository documentation metadata'}</p></article>
				  <article><span>Automatic reconciliation</span><strong>{selectedRepo.documentation.registered ? humanize(selectedRepo.documentation.reconcile_state || 'registered') : 'Not registered'}</strong><p>{selectedRepo.documentation.next_poll_at ? `Next HEAD/policy poll ${new Date(selectedRepo.documentation.next_poll_at).toLocaleString()}` : 'Run the index command once from the intended worktree to register it privately.'}</p></article>
                </div>
                <div class="rag-operator-panel">
				  <div><p class="section-kicker">REGISTERED GIT AUTHORITY</p><h3>Index and supervise</h3><p>{selectedRepo.documentation.registered ? 'Reconcile resolves current HEAD and committed policy from the private daemon registration, schedules an immutable revision-set job when needed, and never fetches GitCode.' : 'The Admin UI never accepts an absolute worktree path. Run indexing once from the intended worktree to create the private registration, then controls become available here.'}</p></div>
                  <div class="rag-action-form documentation-actions"><button class="primary-action" disabled={!csrfToken || !registrationControlsEnabled || !selectedMaintenance || !selectedRepo.documentation.registered || controlRunning} onclick={(event) => void confirmControl('repository_docs_index', event.currentTarget)}><RotateCcw size={16} />Index current HEAD</button><button onclick={openRepositoryDocsJobs} disabled={!selectedRepo.documentation.registered}><Activity size={16} />Open index jobs</button><button onclick={() => void copyDocumentationCommand('index')} disabled={!selectedRepo.documentation.index_handoff}><FileText size={16} />{copied === 'documentation-index' ? 'Copied index command' : 'Copy CLI handoff'}</button></div>
                  <p class="privacy search-boundary"><ShieldCheck size={15} />The browser sends only the opaque registration id. Filesystem authority remains in the daemon's private registry.</p>
                </div>

                <div class="rag-operator-panel repository-docs-search-panel">
                  <div><p class="section-kicker">EXACT REVISION SEARCH</p><h3>Search repository documentation</h3><p>Select a Git ref or object id. Results are hydrated from Git and carry blob, line, and raw-slice digest citations; document bodies are not persisted in the cache.</p></div>
                  <form class="search-query-form" onsubmit={(event) => { event.preventDefault(); void runRepositoryDocsSearch(); }}>
                    <label class="search-query"><span>Query</span><input required maxlength="512" bind:value={repositoryDocsQuery} placeholder="How is repository documentation indexed?" /></label>
                    <label><span>Revision</span><input maxlength="256" bind:value={repositoryDocsRevision} placeholder="HEAD or object id" /></label>
                    <label><span>Mode</span><select bind:value={repositoryDocsMode}><option value="hybrid">Hybrid</option><option value="fulltext">Full text</option></select></label>
                    <label><span>Limit</span><input type="number" min="1" max="20" bind:value={repositoryDocsLimit} /></label>
                    <button class="primary-action" type="submit" disabled={!csrfToken || !repositoryDocsSearchEnabled || !selectedMaintenance || repositoryDocsRunning}><Search size={16} />{repositoryDocsRunning ? 'Searching…' : 'Search Git'}</button>
                    <label class="worktree-toggle"><input type="checkbox" bind:checked={repositoryDocsIncludeWorktree} /><span>Include tracked worktree overlay</span></label>
                  </form>
                  <p class="privacy search-boundary"><ShieldCheck size={15} />Full-text stays local. Hybrid sends only the query to the configured embedding provider; Git document text remains local authority.</p>
                  {#if repositoryDocsError}<div class="action-result error" role="alert"><AlertTriangle size={17} /><div><strong>Repository documentation search failed</strong><span>{repositoryDocsError}</span></div></div>{/if}
                  {#if repositoryDocsResult}
                    <article class="search-mode-column repository-docs-results">
                      <header><div><p class="section-kicker">{repositoryDocsResult.authority.toUpperCase()}</p><h3>{humanize(repositoryDocsResult.effective_mode)} · {repositoryDocsResult.effective_revision.slice(0, 12)}</h3></div><StatusChip value={repositoryDocsResult.fallback ? 'partial' : 'ready'} label={repositoryDocsResult.fallback ? humanize(repositoryDocsResult.fallback) : `${repositoryDocsResult.hits.length} results`} /></header>
                      {#if repositoryDocsResult.warnings?.length}<ul class="blocker-list">{#each repositoryDocsResult.warnings as warning}<li><AlertTriangle size={15} />{warning}</li>{/each}</ul>{/if}
                      {#if repositoryDocsResult.hits.length === 0}<div class="empty-inline"><Search size={18} /><div><strong>No Git-backed match</strong><span>The selected revision and committed policy produced an empty result.</span></div></div>{:else}
                        <ol class="search-result-list">{#each repositoryDocsResult.hits as hit}<li><div class="search-result-heading"><span class="result-rank">#{hit.rank}</span><div><strong>{hit.citation.path}</strong><small>{hit.chunk_id.slice(0, 12)} · {humanize(hit.citation.authority)}</small></div></div><p>{hit.snippet}</p><dl class="score-row"><div><dt>Lexical</dt><dd>{(hit.lexical_score || 0).toFixed(4)}</dd></div><div><dt>Semantic</dt><dd>{(hit.semantic_score || 0).toFixed(4)}</dd></div><div><dt>Fusion</dt><dd>{hit.score.toFixed(6)}</dd></div></dl><div class="result-location"><code>{hit.citation.path}:L{hit.citation.line_start}–L{hit.citation.line_end}</code><span>{hit.citation.raw_slice_digest.slice(0, 12)}</span></div><details><summary>Digest-verified Git citation</summary><ul><li><code>{hit.citation.commit_oid.slice(0, 12)} · blob {hit.citation.blob_oid.slice(0, 12)}</code><p>Raw slice digest {hit.citation.raw_slice_digest}</p></li></ul></details></li>{/each}</ol>
                      {/if}
                    </article>
                  {/if}
                </div>
                {#if pendingControl === 'repository_docs_index'}<dialog bind:this={controlDialog} class="confirmation-dialog control-confirmation" aria-labelledby="confirm-repo-docs-reconcile-title" oncancel={(event) => { event.preventDefault(); void cancelControlConfirmation(); }}><span class="dialog-icon"><RotateCcw size={21} /></span><div><p class="section-kicker">CONFIRM LOCAL GIT RECONCILIATION</p><h2 id="confirm-repo-docs-reconcile-title">Resolve and index current HEAD?</h2><p>The daemon will inspect the registered worktree and committed policy, then coalesce or start only the immutable repository-document index job. It will not fetch GitCode, start remote sync, or persist document bodies.</p><dl><div><dt>Target</dt><dd>{selectedRepo.repo_id}</dd></div><div><dt>Cache</dt><dd>{selectedCache?.cache_ref}</dd></div><div><dt>Registration</dt><dd>{selectedMaintenance?.registration_id}</dd></div></dl><div class="dialog-actions"><button onclick={() => void cancelControlConfirmation()} disabled={controlRunning}>Keep current state</button><button bind:this={controlConfirmButton} class="primary-action" onclick={() => void executeControl()} disabled={controlRunning}>{controlRunning ? 'Submitting…' : 'Confirm indexing'}</button></div></div></dialog>{/if}
              </section>
            {:else if repoTab === 'search'}
              <section class="repository-section search-lab" aria-labelledby="search-title">
                <div class="section-heading"><div><p class="section-kicker">OBSERVATION-ONLY EXPERIMENT</p><h2 id="search-title">Search Lab</h2><p>Compare deterministic full-text with requested hybrid retrieval. A query never syncs GitCode, starts provider setup, or repairs an index.</p></div><StatusChip value={selectedRepo.coverage.rag.state} label={`RAG ${humanize(selectedRepo.coverage.rag.state)}`} /></div>
                <form class="search-query-form" onsubmit={(event) => { event.preventDefault(); void runSearchComparison(); }}>
                  <label class="search-query"><span>Experiment query</span><input required maxlength="512" bind:value={searchQuery} placeholder="How does daemon maintenance avoid duplicate work?" /></label>
                  <label><span>Kind</span><select bind:value={searchKind}><option value="">All kinds</option>{#each ['issue', 'issue_comment', 'pull_request', 'pr_comment', 'wiki'] as kind}<option value={kind}>{humanize(kind)}</option>{/each}</select></label>
                  <label><span>Provenance</span><select bind:value={searchProvenance}><option value="">All provenance</option>{#each ['live', 'fixture', 'projection', 'bridge'] as provenance}<option value={provenance}>{humanize(provenance)}</option>{/each}</select></label>
                  <label><span>Limit</span><input type="number" min="1" max="20" bind:value={searchLimit} /></label>
                  <button class="primary-action" type="submit" disabled={!csrfToken || !searchCompareEnabled || searchRunning}><Search size={16} />{searchRunning ? 'Comparing…' : 'Compare modes'}</button>
                </form>
                <p class="privacy search-boundary"><ShieldCheck size={15} />Only the query is sent to the configured embedding provider for hybrid mode; cached source text and GitCode are not contacted by search.</p>
                {#if searchError}<div class="action-result error" role="alert"><AlertTriangle size={17} /><div><strong>Search Lab action failed</strong><span>{searchError}</span></div></div>{/if}

                {#if searchComparison}
                  <div class="experiment-summary">
                    <div><span>Requested → effective</span><strong>{humanize(searchComparison.hybrid.requested_mode)} → {humanize(searchComparison.hybrid.effective_mode)}</strong></div>
                    <div><span>RAG state</span><strong>{humanize(searchComparison.hybrid.rag_state)}</strong><small>{searchComparison.hybrid.fallback_reason ? humanize(searchComparison.hybrid.fallback_reason) : 'No fallback'}</small></div>
                    <div><span>Coverage</span><strong>{searchComparison.hybrid.coverage.embedded_chunks}/{searchComparison.hybrid.coverage.eligible_chunks}</strong><small>{searchComparison.hybrid.coverage.missing_chunks} missing · {searchComparison.hybrid.coverage.stale_chunks} stale · {searchComparison.hybrid.coverage.failed_chunks || 0} failed</small></div>
                    <div><span>Generation</span><strong>{searchComparison.hybrid.coverage.covered_generation ?? '—'} / {searchComparison.hybrid.coverage.content_generation ?? '—'}</strong><small>{searchComparison.hybrid.coverage.namespace_id || 'No namespace'}</small></div>
                    <button onclick={() => void copyExperimentSummary()}><Clipboard size={15} />{experimentCopied ? 'Copied report' : 'Copy report'}</button>
                  </div>
                  <div class="search-compare-grid">
                    {#each [{ label: 'Full text', run: searchComparison.full_text }, { label: 'Hybrid requested', run: searchComparison.hybrid }] as column}
                      <article class="search-mode-column">
                        <header><div><p class="section-kicker">{column.label.toUpperCase()}</p><h3>{humanize(column.run.effective_mode)}</h3></div><StatusChip value={column.run.fallback_reason ? 'partial' : 'ready'} label={column.run.fallback_reason ? humanize(column.run.fallback_reason) : `${column.run.results.length} results`} /></header>
                        {#if column.run.results.length === 0}<div class="empty-inline"><Search size={18} /><div><strong>No cached match</strong><span>This is a valid empty result, not an operational failure.</span></div></div>{:else}
                          <ol class="search-result-list">{#each column.run.results as result}<li><div class="search-result-heading"><span class="result-rank">#{result.rank}</span><div><strong>{result.title || result.id}</strong><small>{result.id} · {humanize(result.kind)} · {humanize(result.provenance)}</small></div></div><p>{result.snippet}</p><dl class="score-row"><div><dt>Lexical</dt><dd>{(result.match.lexical_score || 0).toFixed(4)}{result.match.lexical_rank ? ` · #${result.match.lexical_rank}` : ''}</dd></div><div><dt>Semantic</dt><dd>{(result.match.semantic_score || 0).toFixed(4)}{result.match.semantic_rank ? ` · #${result.match.semantic_rank}` : ''}</dd></div><div><dt>Fusion</dt><dd>{result.match.fusion_score.toFixed(6)}</dd></div></dl><div class="result-location"><code>{result.path}{result.line_start ? `:${result.line_start}` : ''}</code>{#if result.match.exact_match}<span>Exact identity</span>{/if}</div>{#if result.citations.length}<details><summary>{result.citations.length} semantic citation{result.citations.length === 1 ? '' : 's'}</summary><ul>{#each result.citations as citation}<li><code>{citation.chunk_id.slice(0, 12)} · L{citation.line_start || '?'}–{citation.line_end || '?'}</code><p>{citation.snippet}</p></li>{/each}</ul></details>{/if}</li>{/each}</ol>
                        {/if}
                      </article>
                    {/each}
                  </div>
                {/if}

                <div class="rag-operator-panel">
                  <div><p class="section-kicker">EXPLICIT PROVIDER ACTIONS</p><h3>Readiness and bounded repair</h3><p>Smoke sends no cached text. Repair embeds at most the confirmed chunk count and never installs, starts, downloads, purges, or rebuilds all namespaces.</p></div>
                  <div class="rag-action-form"><label><span>Profile</span><input bind:value={repairProfile} oninput={invalidateRAGRepairPlan} placeholder="Configured default" /></label><label><span>Repair cap</span><div class="unit-input"><input type="number" min="1" max="1000" bind:value={repairMaxChunks} oninput={invalidateRAGRepairPlan} /><span>chunks</span></div></label><button disabled={!csrfToken || !providerSmokeEnabled || searchRunning} onclick={() => void smokeSearchProvider()}><HeartPulse size={16} />Smoke provider</button><button disabled={!csrfToken || !ragRepairEnabled || searchRunning} onclick={() => void renderRAGRepairPlan()}><FileCheck2 size={16} />Plan repair</button></div>
                  {#if providerSmoke}<div class="action-result" role="status"><StatusChip value={providerSmoke.status} /><div><strong>{providerSmoke.status === 'ready' ? `${providerSmoke.provider_id} · ${providerSmoke.model}` : humanize(providerSmoke.failure_class)}</strong><span>{providerSmoke.status === 'ready' ? `${providerSmoke.dimensions} dimensions · revision ${providerSmoke.revision || 'reported ready'}` : providerSmoke.message}{providerSmoke.handoff ? ` · ${providerSmoke.handoff}` : ''}</span></div></div>{/if}
                  {#if repairPlan}<div class="plan-panel rag-plan"><div class="plan-summary"><div><p class="section-kicker">BOUNDED REPAIR</p><h3>At most {repairPlan.max_chunks} chunks</h3><code>{repairPlan.plan_id}</code></div><StatusChip value={repairPlan.status} /></div>{#if repairPlan.blockers?.length}<ul class="blocker-list">{#each repairPlan.blockers as blocker}<li><AlertTriangle size={15} />{blocker}</li>{/each}</ul>{/if}<div class="effect-ledger">{#each repairPlan.effects as effect}<article><span class="effect-icon"><Zap size={15} /></span><div><strong>{effect.summary}</strong><small>{humanize(effect.class)}{effect.data_boundary ? ` · ${humanize(effect.data_boundary)}` : ''}</small>{#if effect.handoff}<code>{effect.handoff}</code>{/if}</div><StatusChip value={effect.status} /></article>{/each}</div><div class="plan-footer"><div><strong>Current gap</strong><span>{repairPlan.coverage.missing_chunks} missing · {repairPlan.coverage.stale_chunks} stale · {repairPlan.coverage.failed_chunks || 0} failed</span></div><button class="primary-action" disabled={repairPlan.status === 'blocked' || repairPlan.status === 'no_work_needed' || controlRunning} onclick={(event) => void confirmControl('rag_repair_apply', event.currentTarget)}><Power size={16} />Confirm bounded repair</button></div></div>{/if}
                  {#if repairReceipt}<div class="action-result" role="status"><CheckCircle2 size={17} /><div><strong>{humanize(repairReceipt.outcome)}</strong><span>{repairReceipt.receipt_id ? `Receipt ${repairReceipt.receipt_id}` : 'Bounded repair accepted.'}{repairReceipt.replayed ? ' · replayed safely' : ''}</span></div></div>{/if}
                </div>
                {#if pendingControl === 'rag_repair_apply' && repairPlan}<dialog bind:this={controlDialog} class="confirmation-dialog control-confirmation" aria-labelledby="confirm-rag-repair-title" oncancel={(event) => { event.preventDefault(); void cancelControlConfirmation(); }}><span class="dialog-icon"><Zap size={21} /></span><div><p class="section-kicker">CONFIRM PROVIDER DATA BOUNDARY</p><h2 id="confirm-rag-repair-title">Repair at most {repairPlan.max_chunks} chunks?</h2><p>Current plan state will be checked again. Only this bounded cached-text slice may be sent to the configured embedding provider; no GitCode request or provider setup is allowed.</p><dl><div><dt>Target</dt><dd>{selectedRepo.repo_id}</dd></div><div><dt>Cache</dt><dd>{selectedCache?.cache_ref}</dd></div><div><dt>Plan</dt><dd>{repairPlan.plan_id}</dd></div></dl><div class="dialog-actions"><button onclick={() => void cancelControlConfirmation()} disabled={controlRunning}>Keep current state</button><button bind:this={controlConfirmButton} class="primary-action" onclick={() => void executeControl()} disabled={controlRunning}>{controlRunning ? 'Submitting…' : 'Confirm bounded repair'}</button></div></div></dialog>{/if}
              </section>
            {:else}
              <section class="repository-section" aria-labelledby="activity-title"><div class="section-heading"><div><p class="section-kicker">BOUNDED HISTORY</p><h2 id="activity-title">Repository activity</h2></div></div><div class="activity-columns"><div><h3>Recent sync events</h3>{#if selectedRepo.recent_sync_events.length === 0}<div class="empty-inline"><History size={18} /><div><strong>No retained sync events</strong><span>Coverage may still contain frontier evidence.</span></div></div>{:else}<div class="timeline">{#each selectedRepo.recent_sync_events as event}<article><span class="timeline-mark"></span><div><strong>{humanize(event.kind)}</strong><span>{event.zero_delta ? 'No content delta' : humanize(event.status)}</span></div><time>{new Date(event.completed_at).toLocaleString()}</time></article>{/each}</div>{/if}</div><div><h3>Scoped jobs</h3>{#if scopedJobs.length === 0}<div class="empty-inline"><Clock3 size={18} /><div><strong>No retained jobs</strong><span>No work history for this pair.</span></div></div>{:else}<div class="mini-job-list">{#each scopedJobs.slice(-8).reverse() as job}<div><span><strong>{humanize(job.type)}</strong>{new Date(job.updated_at).toLocaleString()}</span><StatusChip value={job.status} /></div>{/each}</div>{/if}</div></div></section>
            {/if}
          {:else}
            <div class="intro section-intro"><p class="eyebrow">TOPOLOGY</p><h1>Caches</h1><p>Cache identity, repository bindings, maintenance registration, and coverage—without absolute paths.</p></div>
            {#if snapshot.caches.length === 0}<div class="empty-state large-empty"><Database size={27} /><h2>No managed caches</h2><p>The daemon has no enrolled or primary cache to observe.</p></div>{:else}<div class="topology-list">{#each snapshot.caches as cache}<article class="topology-cache"><div class="topology-heading"><div><span class="entity-icon"><Database size={18} /></span><div><h2>{cache.cache_ref}</h2><p>{cache.storage_mode || 'managed'} · fingerprint {cache.path_fingerprint || 'not available'}</p></div></div><StatusChip value={cache.readiness} /></div><dl class="cache-metadata"><div><dt>Schema</dt><dd>{cache.schema_version || 'Unknown'}</dd></div><div><dt>WAL</dt><dd>{cache.wal_capable ? humanize(cache.journal_mode || 'capable') : 'Unavailable'}</dd></div><div><dt>Records</dt><dd>{cache.record_count.toLocaleString()}</dd></div><div><dt>Chunks</dt><dd>{cache.chunk_count.toLocaleString()}</dd></div></dl><div class="repository-list">{#if cache.repositories.length === 0}<div class="empty-inline"><Layers3 size={18} /><div><strong>No repository bindings</strong><span>The cache is open but contains no repositories.</span></div></div>{:else}{#each cache.repositories as repo}<button onclick={() => openRepository(cache, repo)}><span class="repo-main"><strong>{repo.display_name || repo.repo_id}</strong><small>{repo.counts.records.toLocaleString()} records · {repo.counts.chunks.toLocaleString()} chunks · {humanize(repo.binding_state)}</small></span><span class="repo-lanes"><StatusChip value={repo.coverage.head.state} label={`Head ${humanize(repo.coverage.head.state)}`} /><StatusChip value={repo.coverage.tail.state} label={`Tail ${humanize(repo.coverage.tail.state)}`} /><StatusChip value={repo.coverage.rag.state} label={`RAG ${humanize(repo.coverage.rag.state)}`} /></span><ChevronRight size={17} /></button>{/each}{/if}</div></article>{/each}</div>{/if}
          {/if}

        {:else if active === 'Jobs'}
          {#if selectedJobID}
            <button class="back-link" onclick={closeJob}><ArrowLeft size={15} />Back to retained jobs</button>
            {#if selectedJob}
              <div class="intro section-intro job-heading"><p class="eyebrow">JOB DETAIL</p><h1>{humanize(selectedJob.type)}</h1><p>{selectedJob.id} · {selectedJob.repo_id || 'service-wide'} · {selectedJob.cache_ref || 'unscoped'}</p></div>
              <section class="job-state-card" aria-labelledby="job-state-title">
                <div class="job-state-heading"><div><span class="entity-icon"><Gauge size={18} /></span><div><p class="section-kicker">CURRENT STATE</p><h2 id="job-state-title">{humanize(selectedJob.status)}</h2></div></div><StatusChip value={selectedJob.status} /></div>
                <div class="job-progress" aria-label={`Job progress ${selectedJob.completed || 0} of ${selectedJob.steps || 0}`}><span style={`width: ${selectedJob.steps ? Math.min(100, ((selectedJob.completed || 0) / selectedJob.steps) * 100) : 0}%`}></span></div>
                <dl class="job-metrics"><div><dt>Progress</dt><dd>{selectedJob.completed || 0}/{selectedJob.steps || '—'}</dd></div><div><dt>Throughput</dt><dd>{selectedJob.throughput_per_second ? `${selectedJob.throughput_per_second.toFixed(2)}/s` : 'Not enough data'}</dd></div><div><dt>ETA</dt><dd>{selectedJob.eta_seconds ? `${selectedJob.eta_seconds}s` : 'Not available'}</dd></div><div><dt>Work identity</dt><dd>{selectedJob.work_ref || 'Legacy job'}</dd></div></dl>
                <div class="job-actions">
                  <div><strong>Safe controls</strong><span>{selectedJob.action_reason || 'Available for this daemon-owned work.'}</span></div>
                  <button class="danger-action" disabled={!selectedJob.cancellable || !csrfToken || actionRunning} onclick={(event) => void confirmJobAction('cancel', event.currentTarget)}><XCircle size={16} />Cancel job</button>
                  <button disabled={!selectedJob.retryable || !csrfToken || actionRunning} onclick={(event) => void confirmJobAction('retry', event.currentTarget)}><RotateCcw size={16} />Retry job</button>
                </div>
                {#if !csrfToken}<p class="control-note"><ShieldCheck size={15} />Controls are unavailable until this browser has an authenticated CSRF-bound admin session.</p>{/if}
                {#if actionError}<div class="action-result error" role="alert"><AlertTriangle size={17} /><div><strong>Action failed</strong><span>{actionError}</span></div></div>{/if}
                {#if actionReceipt}<div class="action-result" role="status"><CheckCircle2 size={17} /><div><strong>{humanize(actionReceipt.outcome)}</strong><span>Receipt {actionReceipt.receipt_id} · result {actionReceipt.result_job_id || actionReceipt.target_job_id} · {humanize(actionReceipt.job_status)}{actionReceipt.replayed ? ' · replayed safely' : ''}</span></div></div>{/if}
              </section>

              <section class="repository-section" aria-labelledby="job-context-title"><div class="section-heading"><div><p class="section-kicker">IDENTITY & RETENTION</p><h2 id="job-context-title">Execution context</h2></div></div><dl class="job-context"><div><dt>Created</dt><dd>{new Date(selectedJob.created_at).toLocaleString()}</dd></div><div><dt>Started</dt><dd>{selectedJob.started_at ? new Date(selectedJob.started_at).toLocaleString() : 'Not started'}</dd></div><div><dt>Finished</dt><dd>{selectedJob.finished_at ? new Date(selectedJob.finished_at).toLocaleString() : 'Not terminal'}</dd></div><div><dt>Registration</dt><dd>{selectedJob.registration_id || 'Manual work'}</dd></div><div><dt>Profile</dt><dd>{selectedJob.profile_id || 'Not applicable'}</dd></div><div><dt>Progress retention</dt><dd>{selectedJob.progress_retained || 0}/{selectedJob.progress_limit || '—'} events</dd></div></dl>{#if selectedJob.failure_class}<div class="action-result error"><AlertTriangle size={17} /><div><strong>{humanize(selectedJob.failure_class)}{selectedJob.failure_collection ? ` · ${humanize(selectedJob.failure_collection)}` : ''}</strong><span>{selectedJob.failure_message || 'Use Diagnostics for typed remediation.'}{selectedJob.retry_after ? ` Retry after ${selectedJob.retry_after}.` : ''}</span>{#if selectedJob.inspect_command}<code>{selectedJob.inspect_command}</code>{/if}{#if selectedJob.remediation_command}<code>{selectedJob.remediation_command}</code>{/if}</div></div>{/if}</section>

              <section class="repository-section" aria-labelledby="job-timeline-title"><div class="section-heading"><div><p class="section-kicker">STRUCTURED, BOUNDED</p><h2 id="job-timeline-title">Progress timeline</h2></div><span class="count-badge">{selectedJob.progress?.length || 0}</span></div>{#if !selectedJob.progress?.length}<div class="empty-state"><History size={23} /><h3>No retained progress events</h3><p>The current state and timestamps remain authoritative.</p></div>{:else}<ol class="job-timeline">{#each selectedJob.progress as event, index}<li><span class="timeline-mark"></span><div><strong>{humanize(event.phase || event.type || `Event ${index + 1}`)}</strong><span>{[event.collection && humanize(event.collection), event.page ? `page ${event.page}` : '', event.records_fetched ? `${event.records_fetched} fetched` : '', event.records_skipped ? `${event.records_skipped} skipped` : '', event.records_failed ? `${event.records_failed} failed` : ''].filter(Boolean).join(' · ') || 'State transition'}</span>{#if event.rate_limit_state || event.retry_after}<small>{event.rate_limit_state ? `Rate limit: ${humanize(event.rate_limit_state)}` : ''}{event.retry_after ? ` · retry after ${event.retry_after}` : ''}</small>{/if}</div></li>{/each}</ol>{/if}</section>
            {:else}
              <div class="empty-state large-empty"><History size={27} /><h2>Job is no longer retained</h2><p>The bounded daemon history expired this job. Cached records, maintenance frontiers, and audit receipts are unaffected.</p><button class="text-action" onclick={closeJob}>Show retained jobs</button></div>
            {/if}
          {:else}
            <div class="intro section-intro"><p class="eyebrow">EXECUTION</p><h1>Jobs</h1><p>Daemon-owned work, bounded history, and deliberate supervision with durable receipts.</p></div>
            <div class="metric-strip jobs-metrics"><div><span>Active</span><strong>{activeJobs.length}</strong></div><div><span>Failed</span><strong>{failedJobs.length}</strong><button class="text-action" onclick={() => setJobFilter('state', 'failed')}>Show failed</button></div><div><span>Terminal</span><strong>{snapshot.jobs.length - activeJobs.length}</strong></div><div><span>Retained</span><strong>{snapshot.jobs.length}</strong></div></div>
            <section class="retention-summary" aria-labelledby="retention-title">
              <div><p class="section-kicker">LIFECYCLE POLICY</p><h2 id="retention-title">Bounded job history</h2><span>Routine successes expire after {retentionDuration(snapshot.job_retention.success_ttl_seconds)}; failures and interruptions after {retentionDuration(snapshot.job_retention.diagnostic_ttl_seconds)}.</span></div>
              <dl><div><dt>Terminal cap</dt><dd>{snapshot.job_retention.max_terminal_jobs || '—'}</dd></div><div><dt>Diagnostic cohort</dt><dd>{snapshot.job_retention.max_diagnostic_jobs || '—'}</dd></div><div><dt>Progress cap</dt><dd>{snapshot.job_retention.max_progress_events || '—'} / job</dd></div><div><dt>Oldest retained</dt><dd>{snapshot.job_retention.oldest_retained_at ? new Date(snapshot.job_retention.oldest_retained_at).toLocaleString() : 'None'}</dd></div></dl>
              <p>Retained by state: {retainedStatusSummary()}. Expired this runtime: {snapshot.job_retention.expired_total}; cap-truncated: {snapshot.job_retention.truncated_total}.{snapshot.job_retention.last_pruned_at ? ` Last prune ${new Date(snapshot.job_retention.last_pruned_at).toLocaleString()}.` : ''}</p>
              <p>Expiry removes only operational job history. Cached GitCode records, maintenance frontiers and policies, RAG indexes, and audit receipts remain intact.</p>
            </section>
            <div class="job-filter-bar" aria-label="Job filters">
              <label><span>State</span><select value={jobStateFilter} onchange={(event) => setJobFilter('state', event.currentTarget.value)}><option value="">All states</option>{#each [...new Set(snapshot.jobs.map((job) => job.status))].sort() as value}<option value={value}>{humanize(value)}</option>{/each}</select></label>
              <label><span>Type</span><select value={jobTypeFilter} onchange={(event) => setJobFilter('type', event.currentTarget.value)}><option value="">All types</option>{#each [...new Set(snapshot.jobs.map((job) => job.type))].sort() as value}<option value={value}>{humanize(value)}</option>{/each}</select></label>
              <label><span>Cache</span><select value={jobCacheFilter} onchange={(event) => setJobFilter('cache', event.currentTarget.value)}><option value="">All caches</option>{#each uniqueJobValues(snapshot.jobs.map((job) => job.cache_ref)) as value}<option value={value}>{value}</option>{/each}</select></label>
              <label><span>Repository</span><select value={jobRepoFilter} onchange={(event) => setJobFilter('repo', event.currentTarget.value)}><option value="">All repositories</option>{#each uniqueJobValues(snapshot.jobs.map((job) => job.repo_id)) as value}<option value={value}>{value}</option>{/each}</select></label>
              <label><span>Failure</span><select value={jobFailureFilter} onchange={(event) => setJobFilter('failure', event.currentTarget.value)}><option value="">All failures</option>{#each uniqueJobValues(snapshot.jobs.map((job) => job.failure_class)) as value}<option value={value}>{humanize(value)}</option>{/each}</select></label>
            </div>
            {#if snapshot.jobs.length === 0}<div class="empty-state large-empty"><Activity size={27} /><h2>No retained jobs</h2><p>The coordinator has no active or terminal work.</p></div>{:else if filteredJobs.length === 0}<div class="empty-state large-empty"><Search size={27} /><h2>No jobs match these filters</h2><p>Change one or more filters to return to retained work.</p></div>{:else}<div class="table-wrap"><table><caption class="sr-only">Coordinator jobs</caption><thead><tr><th>Job</th><th>Scope</th><th>Status</th><th>Progress</th><th>Updated</th></tr></thead><tbody>{#each [...filteredJobs].reverse() as job}<tr><th scope="row"><button class="job-link" onclick={() => openJob(job)}><span class="table-primary">{job.id}</span><small>{humanize(job.type)} · {job.work_ref || 'legacy identity'}</small></button></th><td><span class="table-primary">{job.repo_id || 'service-wide'}</span><small>{job.cache_ref || 'unscoped'}</small></td><td><StatusChip value={job.status} /></td><td>{job.completed || 0}/{job.steps || '—'}</td><td>{new Date(job.updated_at).toLocaleString()}</td></tr>{/each}</tbody></table></div>{/if}
          {/if}

          {#if pendingConfirmation && selectedJob}
            <dialog bind:this={confirmationDialog} class="confirmation-dialog" aria-labelledby="confirm-action-title" oncancel={(event) => { event.preventDefault(); void cancelJobConfirmation(); }}><span class:danger={pendingConfirmation === 'cancel'} class="dialog-icon">{#if pendingConfirmation === 'cancel'}<XCircle size={21} />{:else}<RotateCcw size={21} />{/if}</span><div><p class="section-kicker">CONFIRM {pendingConfirmation.toUpperCase()}</p><h2 id="confirm-action-title">{pendingConfirmation === 'cancel' ? 'Cancel active work?' : 'Retry terminal work?'}</h2><p>{pendingConfirmation === 'cancel' ? 'The daemon will request graceful cancellation and preserve the terminal observation.' : 'Equivalent active work will be coalesced; otherwise maintenance may create one new job.'}</p><dl><div><dt>Job</dt><dd>{selectedJob.id}</dd></div><div><dt>Scope</dt><dd>{selectedJob.repo_id || 'service-wide'} · {selectedJob.cache_ref || 'unscoped'}</dd></div><div><dt>Identity</dt><dd>{selectedJob.work_ref || 'legacy job'}</dd></div></dl><div class="dialog-actions"><button onclick={() => void cancelJobConfirmation()} disabled={actionRunning}>Keep current state</button><button bind:this={confirmActionButton} class:danger-action={pendingConfirmation === 'cancel'} class="primary-action" onclick={() => void executeJobAction()} disabled={actionRunning}>{actionRunning ? 'Submitting…' : pendingConfirmation === 'cancel' ? 'Confirm cancel' : 'Confirm retry'}</button></div></div></dialog>
          {/if}

        {:else if active === 'Maintenance'}
          <div class="intro section-intro"><p class="eyebrow">POLICY & BINDINGS</p><h1>Maintenance</h1><p>Render every local effect before it runs. Browser controls never install services, download models, accept cache paths, or contact GitCode for binding changes.</p></div>
          {#if !csrfToken}<div class="state-panel warning-panel"><ShieldCheck size={20} /><div><strong>Controls need an authenticated admin session</strong><p>Reopen the console with <code>gitcode-mcp admin open</code>. Observation remains available.</p></div></div>{/if}

          <section class="control-workbench" aria-labelledby="policy-editor-title">
            <div class="control-heading"><div><span class="large-icon"><SlidersHorizontal size={22} /></span><div><p class="section-kicker">PLAN → CONFIRM → APPLY</p><h2 id="policy-editor-title">Maintenance policy</h2><p>Change a managed repository policy, review the effect ledger, then confirm the exact plan id.</p></div></div><StatusChip value={maintenancePlan?.status || selectedMaintenance?.state || 'not_planned'} label={maintenancePlan ? humanize(maintenancePlan.status) : selectedMaintenance ? humanize(selectedMaintenance.state) : 'Not planned'} /></div>
            {#if repoTargets.length === 0}<div class="empty-state"><Database size={23} /><h3>No bound repositories</h3><p>Add a repository binding below before planning maintenance.</p></div>{:else}
              <form class="control-form" oninput={invalidateMaintenancePlan} onsubmit={(event) => { event.preventDefault(); void renderMaintenancePlan(); }}>
                <label class="span-two"><span>Managed target</span><select value={maintenanceTargetKey} onchange={(event) => loadMaintenanceTarget(event.currentTarget.value)}>{#each repoTargets as target}<option value={target.key}>{target.repo.repo_id} · {target.cache.cache_ref}</option>{/each}</select><small>Opaque cache identity only; no filesystem path crosses the browser boundary.</small></label>
                <label><span>Sync mode</span><select bind:value={maintenanceIntent.sync_mode}><option value="off">Off</option><option value="head">Head only</option><option value="head-and-backfill">Head + bounded backfill</option></select></label>
                <label><span>RAG mode</span><select bind:value={maintenanceIntent.rag_mode}><option value="off">Off</option><option value="maintain">Maintain index</option></select></label>
                <fieldset class="span-two collection-field"><legend>Collections</legend>{#each ['issues', 'issue-comments', 'wiki', 'pulls', 'pr-comments'] as collection}<label><input type="checkbox" checked={maintenanceIntent.collections.includes(collection)} onchange={(event) => toggleCollection(collection, event.currentTarget.checked)} />{humanize(collection)}</label>{/each}</fieldset>
                <label><span>RAG profile</span><input bind:value={maintenanceIntent.profile} placeholder="Configured default" /></label>
                <label><span>Head interval</span><div class="unit-input"><input type="number" min="0" max="2592000" bind:value={maintenanceIntent.head_interval_seconds} /><span>sec</span></div></label>
                <label><span>RAG interval</span><div class="unit-input"><input type="number" min="0" max="2592000" bind:value={maintenanceIntent.rag_interval_seconds} /><span>sec</span></div></label>
                <label><span>Head pages</span><input type="number" min="0" max="1000" bind:value={maintenanceIntent.head_max_pages} /></label>
                <label><span>Tail slice</span><input type="number" min="0" max="1000" bind:value={maintenanceIntent.tail_slice_pages} /></label>
                <label><span>Per page</span><input type="number" min="0" max="100" bind:value={maintenanceIntent.per_page} /></label>
                <div class="form-actions span-two"><span>{maintenanceControlsEnabled ? 'Capability available in this daemon.' : 'Capability registry does not expose maintenance controls.'}</span><button class="primary-action" type="submit" disabled={!csrfToken || !maintenanceControlsEnabled || controlRunning}><FileCheck2 size={16} />{controlRunning ? 'Planning…' : 'Render plan'}</button></div>
              </form>
            {/if}

            {#if maintenanceError}<div class="action-result error" role="alert"><AlertTriangle size={17} /><div><strong>Maintenance control failed{maintenanceFailure?.field ? ` · ${humanize(maintenanceFailure.field)}` : ''}</strong><span>{maintenanceError}</span>{#if maintenanceFailure?.remediation}<span>{maintenanceFailure.remediation}</span>{/if}{#if maintenanceFailure?.blockers?.length}<ul class="blocker-list">{#each maintenanceFailure.blockers as blocker}<li>{blocker}</li>{/each}</ul>{/if}{#if maintenanceFailure?.cli_handoff}<code>{maintenanceFailure.cli_handoff}</code>{/if}</div></div>{/if}
            {#if maintenancePlan}<div class="plan-panel"><div class="plan-summary"><div><p class="section-kicker">REVIEWED INTENT</p><h3>{maintenancePlan.repo_id}</h3><code>{maintenancePlan.plan_id}</code></div><StatusChip value={maintenancePlan.status} /></div>{#if maintenancePlan.blockers?.length}<ul class="blocker-list">{#each maintenancePlan.blockers as blocker}<li><AlertTriangle size={15} />{blocker}</li>{/each}</ul>{/if}<div class="effect-ledger">{#each maintenancePlan.actions as effect}<article><span class="effect-icon"><Zap size={15} /></span><div><strong>{effect.summary}</strong><small>{humanize(effect.class)}{effect.data_boundary ? ` · ${humanize(effect.data_boundary)}` : ''}</small>{#if effect.handoff}<code>{effect.handoff}</code>{/if}</div><StatusChip value={effect.status} /></article>{/each}</div><div class="plan-footer"><div><strong>Next safe action</strong><span>{maintenancePlan.next_action || 'Confirm this exact plan.'}</span></div><button class="primary-action" disabled={maintenancePlan.status === 'blocked' || controlRunning} onclick={(event) => void confirmControl('maintenance_apply', event.currentTarget)}><Power size={16} />Confirm & apply</button></div></div>{/if}
            {#if maintenanceReceipt}<div class="action-result" role="status"><CheckCircle2 size={17} /><div><strong>{humanize(maintenanceReceipt.outcome || maintenanceReceipt.status || 'applied')}</strong><span>{maintenanceReceipt.receipt_id ? `Receipt ${maintenanceReceipt.receipt_id}` : maintenanceReceipt.audit_receipt ? `Audit ${maintenanceReceipt.audit_receipt}` : 'The confirmed plan was accepted.'}{maintenanceReceipt.replayed ? ' · replayed safely' : ''}{maintenanceReceipt.jobs_started?.length ? ` · jobs ${maintenanceReceipt.jobs_started.join(', ')}` : ''}</span></div></div>{/if}
            {#if selectedMaintenance}<div class="registration-actions"><div><strong>Registration {selectedMaintenance.registration_id}</strong><span>Generation {selectedMaintenance.generation} · {selectedMaintenance.enabled ? 'enabled' : 'disabled'} · {registrationControlsEnabled ? 'reconcile is coalesced with active work.' : 'registration controls are unavailable in this daemon.'}</span></div><button disabled={!csrfToken || !registrationControlsEnabled || controlRunning} onclick={(event) => void confirmControl('reconcile', event.currentTarget)}><RotateCcw size={15} />Reconcile now</button><button class="danger-action" disabled={!csrfToken || !registrationControlsEnabled || !selectedMaintenance.enabled || controlRunning} onclick={(event) => void confirmControl('disable', event.currentTarget)}><Power size={15} />Disable</button></div>{/if}
          </section>

          <section class="control-workbench" aria-labelledby="binding-editor-title">
            <div class="control-heading"><div><span class="large-icon"><GitFork size={22} /></span><div><p class="section-kicker">LOCAL ROUTING</p><h2 id="binding-editor-title">Repository binding</h2><p>Add or update one stable repository identity. API URL defaults to the configured GitCode v5 endpoint; existing custom routes are preserved when omitted.</p></div></div><StatusChip value={bindingPlan?.status || 'not_planned'} label={bindingPlan ? humanize(bindingPlan.status) : 'Not planned'} /></div>
            {#if snapshot.caches.length === 0}<div class="empty-state"><Database size={23} /><h3>No managed cache</h3><p>The browser cannot create or choose arbitrary cache paths. Enroll a cache from the CLI first.</p></div>{:else}
              <div class="binding-shortcuts"><span>Edit existing</span>{#each snapshot.caches as cache}{#each cache.repositories as repo}<button onclick={() => loadBinding(cache, repo)}>{repo.repo_id}<small>{cache.cache_ref}</small></button>{/each}{/each}<button onclick={() => loadBinding(snapshot.caches[0])}>+ New binding<small>{snapshot.caches[0].cache_ref}</small></button></div>
              <form class="control-form" oninput={invalidateBindingPlan} onsubmit={(event) => { event.preventDefault(); void renderBindingPlan(); }}>
                <label><span>Managed cache</span><select bind:value={bindingIntent.cache_ref}>{#each snapshot.caches as cache}<option value={cache.cache_ref}>{cache.cache_ref}</option>{/each}</select></label>
                <label><span>Repository id</span><input required bind:value={bindingIntent.repo_id} placeholder="owner/repository" /></label>
                <label><span>Owner</span><input bind:value={bindingIntent.owner} placeholder="Derived from repository id" /></label>
                <label><span>Name</span><input bind:value={bindingIntent.name} placeholder="Derived from repository id" /></label>
                <label class="span-two"><span>API base URL</span><input type="url" bind:value={bindingIntent.api_base_url} placeholder="Configured GitCode v5 default" /><small>Leave blank to use the configured default or preserve an existing custom endpoint.</small></label>
                <label><span>Display name</span><input bind:value={bindingIntent.display_name} placeholder="Optional operator label" /></label>
                <label><span>Aliases</span><input value={bindingIntent.aliases.join(', ')} oninput={(event) => { bindingIntent = { ...bindingIntent, aliases: event.currentTarget.value.split(',').map((item) => item.trim()).filter(Boolean) }; invalidateBindingPlan(); }} placeholder="legacy/name, short-name" /></label>
                <fieldset class="span-two collection-field"><legend>Scopes</legend>{#each ['issues', 'wiki'] as scope}<label><input type="checkbox" checked={bindingIntent.scopes.includes(scope)} onchange={(event) => { const next = new Set(bindingIntent.scopes); if (event.currentTarget.checked) next.add(scope); else next.delete(scope); bindingIntent = { ...bindingIntent, scopes: [...next] }; invalidateBindingPlan(); }} />{humanize(scope)}</label>{/each}</fieldset>
                <div class="form-actions span-two"><span>{bindingControlsEnabled ? 'Atomic local cache write; zero GitCode requests.' : 'Capability registry does not expose binding controls.'}</span><button class="primary-action" type="submit" disabled={!csrfToken || !bindingControlsEnabled || controlRunning}><FileCheck2 size={16} />{controlRunning ? 'Planning…' : 'Render binding plan'}</button></div>
              </form>
            {/if}
            {#if bindingError}<div class="action-result error" role="alert"><AlertTriangle size={17} /><div><strong>Binding control failed</strong><span>{bindingError}</span></div></div>{/if}
            {#if bindingPlan}<div class="plan-panel"><div class="plan-summary"><div><p class="section-kicker">{bindingPlan.action.toUpperCase()} BINDING</p><h3>{bindingPlan.repo_id}</h3><code>{bindingPlan.plan_id}</code></div><StatusChip value={bindingPlan.status} /></div>{#if bindingPlan.blockers?.length}<ul class="blocker-list">{#each bindingPlan.blockers as blocker}<li><AlertTriangle size={15} />{blocker}</li>{/each}</ul>{/if}<dl class="binding-preview"><div><dt>API route</dt><dd>{bindingPlan.binding.api_base_url}</dd></div><div><dt>Scopes</dt><dd>{bindingPlan.binding.scopes.map(humanize).join(', ')}</dd></div><div><dt>Aliases</dt><dd>{bindingPlan.binding.aliases.join(', ') || 'None'}</dd></div></dl><div class="effect-ledger">{#each bindingPlan.effects as effect}<article><span class="effect-icon"><Zap size={15} /></span><div><strong>{effect.summary}</strong><small>{humanize(effect.class)}</small>{#if effect.handoff}<code>{effect.handoff}</code>{/if}</div><StatusChip value={effect.status} /></article>{/each}</div><div class="plan-footer"><div><strong>Mutation boundary</strong><span>No unbind, cache deletion, credentials, provider changes, or remote requests.</span></div><button class="primary-action" disabled={bindingPlan.status === 'blocked' || controlRunning} onclick={(event) => void confirmControl('binding_apply', event.currentTarget)}><GitFork size={16} />Confirm & apply</button></div></div>{/if}
            {#if bindingReceipt}<div class="action-result" role="status"><CheckCircle2 size={17} /><div><strong>{humanize(bindingReceipt.outcome)}</strong><span>Receipt {bindingReceipt.receipt_id}{bindingReceipt.replayed ? ' · replayed safely' : ''}</span></div></div>{/if}
          </section>

          {#if pendingControl}<dialog bind:this={controlDialog} class="confirmation-dialog control-confirmation" aria-labelledby="confirm-control-title" oncancel={(event) => { event.preventDefault(); void cancelControlConfirmation(); }}><span class:danger={pendingControl === 'disable'} class="dialog-icon">{#if pendingControl === 'binding_apply'}<GitFork size={21} />{:else if pendingControl === 'reconcile'}<RotateCcw size={21} />{:else}<Power size={21} />{/if}</span><div><p class="section-kicker">CONFIRM LOCAL CONTROL</p><h2 id="confirm-control-title">{pendingControl === 'maintenance_apply' ? 'Apply this maintenance plan?' : pendingControl === 'binding_apply' ? 'Write this repository binding?' : pendingControl === 'disable' ? 'Disable this registration?' : 'Reconcile this registration now?'}</h2><p>The daemon will validate current state again. A stale plan is rejected; an interrupted retry reuses the same durable idempotency key.</p><dl><div><dt>Target</dt><dd>{pendingControl === 'binding_apply' ? bindingIntent.repo_id : maintenanceIntent.repo_id}</dd></div><div><dt>Cache</dt><dd>{pendingControl === 'binding_apply' ? bindingIntent.cache_ref : maintenanceIntent.cache_ref}</dd></div><div><dt>Plan</dt><dd>{pendingControl === 'binding_apply' ? bindingPlan?.plan_id : pendingControl === 'maintenance_apply' ? maintenancePlan?.plan_id : selectedMaintenance?.registration_id}</dd></div></dl><div class="dialog-actions"><button onclick={() => void cancelControlConfirmation()} disabled={controlRunning}>Keep current state</button><button bind:this={controlConfirmButton} class:danger-action={pendingControl === 'disable'} class="primary-action" onclick={() => void executeControl()} disabled={controlRunning}>{controlRunning ? 'Submitting…' : 'Confirm action'}</button></div></div></dialog>{/if}

        {:else}
          <div class="intro section-intro"><p class="eyebrow">GOVERNANCE & RECOVERY</p><h1>Diagnostics</h1><p>Typed failures, recovered state, exact remediation, and capability boundaries.</p></div>
          <div class="filter-bar" aria-label="Diagnostic state filter">{#each ['current', 'recovered', 'all'] as value}<button class:active={diagnosticsFilter === value} aria-pressed={diagnosticsFilter === value} onclick={() => selectDiagnosticFilter(value as 'current' | 'recovered' | 'all')}>{humanize(value)}</button>{/each}</div>
          <section class="repository-section" aria-labelledby="diagnostics-title"><div class="section-heading"><div><p class="section-kicker">TYPED FAILURES</p><h2 id="diagnostics-title">{humanize(diagnosticsFilter)} diagnostics</h2></div><span class="count-badge">{visibleDiagnostics.length}</span></div>{#if visibleDiagnostics.length === 0}<div class="empty-state"><CheckCircle2 size={24} /><h3>No {diagnosticsFilter} diagnostics</h3><p>No matching typed failure is retained.</p></div>{:else}<div class="diagnostic-list">{#each visibleDiagnostics as item}<article class:recovered={!item.current}><div class="diagnostic-heading"><div><span class="entity-icon"><AlertTriangle size={17} /></span><div><strong>{humanize(item.failure_class)}</strong><span>{humanize(item.entity_type)} · {item.entity_id}</span></div></div><StatusChip value={item.current ? item.severity : 'current'} label={item.current ? humanize(item.severity) : 'Recovered'} /></div><p>{item.message}</p>{#if item.remediation}<div class="remediation"><strong>Safe remediation</strong><span>{item.remediation}</span></div>{/if}<div class="command-row"><code>{cliHandoff(item)}</code><button onclick={() => void copyCommand(item)} aria-label={`Copy CLI handoff for ${humanize(item.failure_class)}`}><Clipboard size={15} />{copied === item.id ? 'Copied' : 'Copy CLI'}</button></div></article>{/each}</div>{/if}</section>
          <section class="repository-section" aria-labelledby="capabilities-title"><div class="section-heading"><div><p class="section-kicker">SAFETY CATALOG</p><h2 id="capabilities-title">Capabilities</h2></div><span class="count-badge">{snapshot.capabilities.length}</span></div>{#if snapshot.capabilities.length === 0}<div class="empty-state"><ShieldCheck size={24} /><h3>No capability catalog</h3><p>The daemon did not expose capability metadata.</p></div>{:else}<div class="table-wrap"><table><caption class="sr-only">Capability safety and availability</caption><thead><tr><th>Capability</th><th>Safety</th><th>UI</th><th>CLI</th><th>MCP</th></tr></thead><tbody>{#each snapshot.capabilities as capability}<tr><th scope="row"><span class="table-primary">{capability.id}</span><small>{capability.description}</small></th><td>{humanize(capability.safety_class)}</td><td><StatusChip value={capability.ui_enabled ? 'ready' : 'disabled'} label={capability.ui_enabled ? 'Available' : capability.ui_reason || 'Unavailable'} /></td><td>{capability.cli_enabled ? capability.cli_name || 'Available' : 'Unavailable'}</td><td>{capability.mcp_enabled ? capability.mcp_name || 'Available' : 'Unavailable'}</td></tr>{/each}</tbody></table></div>{/if}</section>
        {/if}
      {/if}
    </section>
  </main>
</div>
