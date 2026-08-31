export const adminApiVersion = '1';

export type CoverageLane = {
  state: string;
  status: string;
  updated_at?: string;
  stop_reason?: string;
  pages_listed?: number;
  records_listed?: number;
  current_generation?: number;
  covered_generation?: number;
  eligible?: number;
  embedded?: number;
  missing?: number;
};

export type Execution = {
  active_job_ids?: string[];
  contention?: { state: string; operation?: string };
  scheduled_retry?: { stage: string; at: string };
  last_stage_errors?: Array<{ stage: string; failure_class: string; message: string; observed_at?: string }>;
};

export type Repository = {
  repo_id: string;
  display_name?: string;
  aliases?: string[];
  scopes?: string[];
  binding_state: string;
  counts: {
    records: number;
    comments: number;
    chunks: number;
    by_kind?: Array<{ kind: string; count: number }>;
    secondary: { pending: number; deferred: number; complete: number; total: number };
  };
  coverage: Record<'head' | 'tail' | 'secondary' | 'projection' | 'rag', CoverageLane>;
  execution: Execution;
  collections: Array<{ kind: string; count: number; head: CoverageLane; tail: CoverageLane }>;
  recent_sync_events: Array<{ id: string; kind: string; status: string; completed_at: string; zero_delta: boolean }>;
  documentation: RepositoryDocumentation;
};

export type RepositoryDocumentation = {
  state: string;
  registered: boolean;
  registration_id?: string;
  source_registration_id?: string;
  source_registration_generation?: number;
  reconcile_state?: string;
  target_commit_oid?: string;
  next_poll_at?: string;
  last_error_class?: string;
  last_error?: string;
  revision_set_id?: string;
  commit_oid?: string;
  requested_revision?: string;
  policy_source?: string;
  policy_hash?: string;
  git_store_ref?: string;
  worktree_ref?: string;
  overlay: boolean;
  namespace_id?: string;
  eligible_files: number;
  eligible_chunks: number;
  embedded_chunks: number;
  reused_chunks: number;
  failed_chunks: number;
  missing_objects: number;
  excluded_files: number;
  exclusions?: Array<{ reason: string; count: number }>;
  active_revision_set_id?: string;
  active_state?: string;
  last_failure_class?: string;
  updated_at?: string;
  revision_set_count: number;
  search_available: boolean;
  semantic_available?: boolean;
  search_handoff?: string;
  index_handoff?: string;
  sources?: Array<{ source_registration_id: string; source_registration_generation: number; state: string; git_store_ref?: string; worktree_ref?: string; commit_oid?: string; policy_hash?: string }>;
  retention: { committed_sets_per_identity: number; overlay_max_age_hours: number; terminal_max_age_hours: number; vector_byte_ceiling: number };
};

export type RepositoryDocsPlan = {
  repo_id: string;
  commit_oid: string;
  include_worktree: boolean;
  git_store_ref: string;
  worktree_ref?: string;
  overlay_digest?: string;
  eligible_files: number;
  eligible_bytes: number;
  excluded_files: number;
  missing_objects: number;
  tracked_changes?: number;
  effective_include: string[];
  effective_exclude: string[];
  policy: { source: string; policy_hash: string; policy: { schema: number; enabled: boolean; preset: string; include?: string[]; exclude?: string[] } };
};

export type CacheObservation = {
  cache_ref: string;
  path_fingerprint?: string;
  storage_mode?: string;
  readiness: string;
  schema_version?: number;
  wal_capable: boolean;
  journal_mode?: string;
  record_count: number;
  chunk_count: number;
  repository_count: number;
  repositories: Repository[];
};

export type Job = {
  id: string;
  type: string;
  cache_ref?: string;
  repo_id?: string;
  profile_id?: string;
  namespace_id?: string;
  registration_id?: string;
  status: string;
  created_at: string;
  started_at?: string;
  updated_at: string;
  finished_at?: string;
  steps?: number;
  completed?: number;
  failure_class?: string;
  failure_message?: string;
  failure_collection?: string;
  retry_after?: string;
  inspect_command?: string;
  remediation_command?: string;
  work_ref?: string;
  cancellable: boolean;
  retryable: boolean;
  action_reason?: string;
  progress_retained: number;
  progress_limit: number;
  throughput_per_second?: number;
  eta_seconds?: number;
  progress?: Array<{ type?: string; phase?: string; collection?: string; page?: number; records_listed?: number; records_fetched?: number; records_inserted?: number; records_updated?: number; records_skipped?: number; records_deferred?: number; records_failed?: number; retry_after?: string; attempt?: number; rate_limit_state?: string }>;
};

export type JobAction = 'cancel' | 'retry';

export type JobActionReceipt = {
  receipt_id: string;
  action: JobAction;
  target_job_id: string;
  result_job_id?: string;
  outcome: string;
  job_status: string;
  replayed: boolean;
  created_at: string;
};

export type Diagnostic = {
  id: string;
  severity: string;
  entity_type: string;
  entity_id: string;
  failure_class: string;
  message: string;
  retryable: boolean;
  current: boolean;
  observed_at?: string;
  remediation?: string;
};

export type Capability = {
  id: string;
  category: string;
  safety_class: string;
  description: string;
  ui_enabled: boolean;
  ui_reason?: string;
  cli_name?: string;
  cli_enabled: boolean;
  mcp_name?: string;
  mcp_enabled: boolean;
};

export type Maintenance = {
  registration_id: string;
  cache_ref: string;
  repo_id: string;
  aliases?: string[];
  legacy_registration_ids?: string[];
  identity_conflict?: {
    kind: string;
    details_available: boolean;
    candidate_registration_ids: string[];
    policy_hashes: string[];
    config_hashes: string[];
    path_fingerprints?: string[];
    candidates?: MaintenanceIdentityCandidate[];
  };
  namespace_id?: string;
  enabled: boolean;
  state: string;
  generation: number;
  next_reconcile_at?: string;
  policy: {
    sync_enabled: boolean;
    sync_mode?: string;
    rag_enabled: boolean;
    collections?: string[];
    head_interval_seconds?: number;
    rag_interval_seconds?: number;
    head_max_pages?: number;
    tail_slice_pages?: number;
    per_page?: number;
    profile?: string;
  };
};

export type MaintenanceIdentityCandidate = {
  candidate_ref: string;
  selection_kind?: string;
  registration_id: string;
  repo_id: string;
  policy: Maintenance['policy'];
  policy_hash: string;
  config_hash?: string;
  path_fingerprint: string;
  source_authority_hash?: string;
  source_refs?: string[];
  was_enabled: boolean;
  cohort_registration_ids?: string[];
  cohort_repo_ids?: string[];
  members?: MaintenanceIdentityCandidate[];
};

export type MaintenanceConflictResolutionPlan = {
  schema_version: string;
  plan_id: string;
  status: string;
  registration_id: string;
  canonical_registration_id: string;
  result_registration_ids?: string[];
  conflict_kind: string;
  expected_generation: number;
  selected: MaintenanceIdentityCandidate;
  effects: Array<{ class: string; summary: string; status: string }>;
};

export type ControlEffect = {
  id: string;
  class: string;
  status: string;
  summary: string;
  data_boundary?: string;
  confirmation_required?: boolean;
  handoff?: string;
};

export type MaintenanceIntent = {
  cache_ref: string;
  repo_id: string;
  sync_mode: string;
  collections: string[];
  rag_mode: string;
  profile?: string;
  head_interval_seconds?: number;
  rag_interval_seconds?: number;
  head_max_pages?: number;
  tail_slice_pages?: number;
  per_page?: number;
  plan_id?: string;
  idempotency_key?: string;
};

export type MaintenancePlan = {
  schema_version: string;
  plan_id: string;
  configuration_hash: string;
  status: string;
  repo_id: string;
  cache: { cache_ref: string; path_fingerprint: string; location_kind: string; schema_version: number; scopes: string[] };
  provider: { profile?: string; provider?: string; provider_type?: string; model?: string; data_boundary?: string; installed?: boolean; running?: boolean; model_available?: boolean; embedding_smoke_status?: string };
  policy: Record<string, string | number | boolean>;
  actions: ControlEffect[];
  blockers?: string[];
  next_action?: string;
};

export type BindingIntent = {
  cache_ref: string;
  repo_id: string;
  owner?: string;
  name?: string;
  api_base_url?: string;
  scopes: string[];
  aliases: string[];
  display_name?: string;
  plan_id?: string;
  idempotency_key?: string;
};

export type BindingPlan = {
  schema_version: string;
  plan_id: string;
  status: string;
  cache_ref: string;
  repo_id: string;
  action: string;
  binding: { repo_id: string; owner: string; name: string; api_base_url: string; scopes: string[]; aliases: string[]; display_name?: string };
  effects: ControlEffect[];
  blockers?: string[];
};

export type ControlReceipt = {
  outcome: string;
  receipt_id?: string;
  plan_id?: string;
  replayed?: boolean;
  created_at?: string;
  jobs_started?: string[];
  checked_at?: string;
  next_action?: string;
  audit_receipt?: string;
  status?: string;
};

export type ControlFailure = {
  code: string;
  message: string;
  remediation?: string;
  field?: string;
  blockers?: string[];
  cli_handoff?: string;
};

export type SearchMatch = {
  lexical_rank?: number;
  semantic_rank?: number;
  lexical_score?: number;
  semantic_score?: number;
  exact_match: boolean;
  fusion_score: number;
};

export type SearchResult = {
  repo_id: string;
  id: string;
  path: string;
  title: string;
  kind: string;
  status: string;
  provenance: string;
  snippet: string;
  line_start?: number;
  line_end?: number;
  score: number;
  rank: number;
  match: SearchMatch;
  citations: Array<{ chunk_id: string; line_start?: number; line_end?: number; snippet: string }>;
};

export type SearchRun = {
  requested_mode: string;
  effective_mode: string;
  rag_state: string;
  fallback_reason?: string;
  coverage: RAGCoverage;
  repair: { state: string };
  results: SearchResult[];
};

export type RAGCoverage = {
  eligible_chunks: number;
  embedded_chunks: number;
  missing_chunks: number;
  stale_chunks: number;
  failed_chunks: number;
  ratio: number;
  namespace_id?: string;
  content_generation?: number;
  covered_generation?: number;
};

export type SearchComparison = {
  schema_version: string;
  cache_ref: string;
  repo_id: string;
  query: string;
  full_text: SearchRun;
  hybrid: SearchRun;
  generated_at: string;
};

export type RepositoryDocsSearchResult = {
  repo_id: string;
  corpus_kind: string;
  query: string;
  requested_revision: string;
  effective_revision: string;
  requested_mode: string;
  effective_mode: string;
  authority: string;
  overlay_digest?: string;
  revision_set_id?: string;
  policy_hash: string;
  policy_source: string;
  namespace_id?: string;
  coverage: {
    state: string;
    eligible_files: number;
    eligible_chunks: number;
    embedded_chunks: number;
    reused_chunks: number;
    failed_chunks: number;
    missing_objects: number;
  };
  hits: Array<{
    rank: number;
    chunk_id: string;
    snippet: string;
    score: number;
    lexical_score?: number;
    semantic_score?: number;
    citation: {
      authority: string;
      commit_oid: string;
      blob_oid: string;
      path: string;
      line_start: number;
      line_end: number;
      raw_slice_digest: string;
    };
  }>;
  warnings?: string[];
  warning_details?: Array<{ code: string; message: string }>;
  fallback?: string;
};

export type ProviderSmoke = {
  status: string;
  profile_id?: string;
  provider_id?: string;
  provider_type?: string;
  model?: string;
  revision?: string;
  dimensions?: number;
  failure_class?: string;
  message?: string;
  handoff?: string;
};

export type RAGRepairPlan = {
  schema_version: string;
  plan_id: string;
  status: string;
  cache_ref: string;
  repo_id: string;
  profile?: string;
  max_chunks: number;
  provider: ProviderSmoke;
  namespace_id?: string;
  coverage: RAGCoverage;
  effects: ControlEffect[];
  blockers?: string[];
};

export type ObservationSnapshot = {
  api_version: string;
  revision: string;
  generated_at: string;
  service: { version: string; protocol: string; running: boolean; installed: boolean; install_kind?: string; started_at?: string; admin_secure: boolean };
  attention: Array<{ id: string; severity: string; entity_type: string; entity_id: string; code: string; message: string; remediation?: string }>;
  caches: CacheObservation[];
  jobs: Job[];
  job_retention: {
    success_ttl_seconds: number;
    diagnostic_ttl_seconds: number;
    max_terminal_jobs: number;
    max_diagnostic_jobs: number;
    max_progress_events: number;
    active: number;
    terminal: number;
    retained_by_status: Array<{ status: string; count: number }>;
    oldest_retained_at?: string;
    last_pruned_at?: string;
    expired_total: number;
    truncated_total: number;
    last_expired: number;
    last_truncated: number;
  };
  maintenance: Maintenance[];
  diagnostics: Diagnostic[];
  capabilities: Capability[];
};

export type AdminView = 'Overview' | 'Caches' | 'Jobs' | 'Maintenance' | 'Diagnostics';
export type RepositoryTab = 'coverage' | 'collections' | 'documentation' | 'search' | 'activity';

export const emptySnapshot: ObservationSnapshot = {
  api_version: adminApiVersion,
  revision: '',
  generated_at: new Date(0).toISOString(),
  service: { version: 'dev', protocol: 'admin.v1', running: false, installed: false, admin_secure: false },
  attention: [],
  caches: [],
  jobs: [],
  job_retention: { success_ttl_seconds: 0, diagnostic_ttl_seconds: 0, max_terminal_jobs: 0, max_diagnostic_jobs: 0, max_progress_events: 0, active: 0, terminal: 0, retained_by_status: [], expired_total: 0, truncated_total: 0, last_expired: 0, last_truncated: 0 },
  maintenance: [],
  diagnostics: [],
  capabilities: []
};

export function humanize(value: string | undefined): string {
  if (!value) return 'Unknown';
  return value.replaceAll('_', ' ').replaceAll('-', ' ').replace(/\b\w/g, (letter) => letter.toUpperCase());
}

export function statusTone(value: string | undefined): 'good' | 'warn' | 'bad' | 'neutral' {
  switch (value) {
    case 'ready':
    case 'current':
    case 'complete':
    case 'fresh':
    case 'running':
    case 'succeeded':
    case 'secure':
    case 'bound':
      return 'good';
    case 'failed':
    case 'error':
    case 'blocked':
    case 'unavailable':
      return 'bad';
    case 'partial':
    case 'degraded':
    case 'warning':
    case 'queued':
    case 'cancelling':
    case 'retry_scheduled':
    case 'backfilling':
    case 'deferred':
    case 'interrupted':
    case 'waiting':
      return 'warn';
    default:
      return 'neutral';
  }
}

export function laneSummary(name: string, lane: CoverageLane): string {
  if (lane.state === 'current') {
    if (name === 'tail') return 'Complete with end-of-collection evidence';
    if (name === 'rag') return `Current generation ${lane.covered_generation ?? lane.current_generation ?? 'verified'}`;
    return humanize(lane.status);
  }
  if (lane.state === 'partial') {
    if (lane.stop_reason) return `Partial · stopped by ${humanize(lane.stop_reason)}`;
    if (lane.missing) return `Partial · ${lane.missing.toLocaleString()} missing`;
    return humanize(lane.status);
  }
  if (lane.state === 'unconfigured') return 'Not configured';
  return 'No observation yet';
}

export function isSnapshotStale(generatedAt: string, now = Date.now()): boolean {
  const generated = Date.parse(generatedAt);
  return Number.isFinite(generated) && now-generated > 5 * 60 * 1000;
}

export function cliHandoff(diagnostic: Diagnostic): string {
	if (diagnostic.entity_type === 'cache') return `gitcode-mcp service doctor`;
	if (diagnostic.entity_type === 'maintenance') return `gitcode-mcp service maintenance --format json`;
	return `gitcode-mcp doctor --format json`;
}
