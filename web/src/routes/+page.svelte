<script lang="ts">
  import './+page.css';
  import { onMount } from 'svelte';
  import {
    Activity,
    Blocks,
    ChevronRight,
    CircleGauge,
    Database,
    FolderCog,
    HeartPulse,
    Monitor,
    Moon,
    RefreshCw,
    ShieldCheck,
    Sun,
    Wrench
  } from '@lucide/svelte';
  import { applyTheme, normalizeTheme, themeStorageKey, type Theme } from '$lib/theme';

  type Readiness = {
    api_version: string;
    version: string;
    daemon_running: boolean;
    session_secure: boolean;
    cache_connected: boolean;
    cache_reference?: string;
    schema_version?: number;
    checked_at: string;
  };

  const navigation = [
    { name: 'Overview', icon: CircleGauge },
    { name: 'Caches', icon: Database },
    { name: 'Jobs', icon: Activity },
    { name: 'Maintenance', icon: Wrench },
    { name: 'Diagnostics', icon: HeartPulse }
  ];

  const themes = [
    { value: 'light' as Theme, label: 'Light', icon: Sun },
    { value: 'dark' as Theme, label: 'Dark', icon: Moon },
    { value: 'system' as Theme, label: 'System', icon: Monitor }
  ];

  let theme: Theme = 'system';
  let active = 'Overview';
  let loading = true;
  let error = '';
  let readiness: Readiness = {
    api_version: '1',
    version: 'dev',
    daemon_running: false,
    session_secure: false,
    cache_connected: false,
    cache_reference: 'Waiting for daemon',
    schema_version: 0,
    checked_at: new Date(0).toISOString()
  };

  async function establishSession(): Promise<void> {
    const fragment = new URLSearchParams(location.hash.slice(1));
    const launchToken = fragment.get('launch');
    if (!launchToken) return;
    history.replaceState(null, '', location.pathname + location.search);
    const response = await fetch('/api/admin/v1/session', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ launch_token: launchToken })
    });
    if (!response.ok) throw new Error('The launch link is invalid or has expired. Run admin open again.');
  }

  async function refresh(): Promise<void> {
    loading = true;
    error = '';
    try {
      const response = await fetch('/api/admin/v1/readiness');
      if (!response.ok) throw new Error(response.status === 401 ? 'Admin session required. Run admin open again.' : 'Readiness is unavailable.');
      readiness = await response.json();
    } catch (value) {
      error = value instanceof Error ? value.message : 'Readiness is unavailable.';
    } finally {
      loading = false;
    }
  }

  function selectTheme(value: Theme): void {
    theme = value;
    applyTheme(value);
  }

  onMount(async () => {
    theme = normalizeTheme(localStorage.getItem(themeStorageKey));
    applyTheme(theme);
    try {
      await establishSession();
      await refresh();
    } catch (value) {
      error = value instanceof Error ? value.message : 'Admin session is unavailable.';
      loading = false;
    }
  });
</script>

<svelte:head><title>gitcode-mcp · Local operator console</title></svelte:head>

<div class="shell">
  <aside class="sidebar">
    <a class="brand" href="/" aria-label="gitcode-mcp overview">
      <span class="brand-mark"><Blocks size={18} strokeWidth={2.2} /></span>
      <span>gitcode-mcp</span>
    </a>

    <nav aria-label="Admin sections">
      {#each navigation as item}
        <button class:active={active === item.name} onclick={() => (active = item.name)}>
          <item.icon size={18} strokeWidth={1.8} />
          <span>{item.name}</span>
        </button>
      {/each}
    </nav>

    <div class="theme-control">
      <span class="theme-label">Theme</span>
      <div class="theme-options" role="radiogroup" aria-label="Color theme">
        {#each themes as item}
          <button
            class:selected={theme === item.value}
            role="radio"
            aria-checked={theme === item.value}
            onclick={() => selectTheme(item.value)}><item.icon size={14} />{item.label}</button
          >
        {/each}
      </div>
      <p>System is the default.</p>
    </div>
  </aside>

  <main>
    <header class="topbar">
      <div class="crumb"><span>Admin</span><ChevronRight size={14} /><strong>{active}</strong></div>
      <div class="version">v{readiness.version || 'dev'}</div>
    </header>

    <section class="content">
      {#if active === 'Overview'}
        <div class="intro">
          <p class="eyebrow">LOCAL ADMIN</p>
          <h1>Local operator console</h1>
          <p>Quick readiness check for your local daemon, cache connection, and admin session.</p>
        </div>

        <div class="status-block" aria-busy={loading}>
          <h2>Readiness</h2>
          <div class="status-row">
            <span class:ok={readiness.daemon_running} class="status-icon"><Activity size={19} /></span>
            <div><strong>Daemon</strong><span>{readiness.daemon_running ? 'Running' : 'Unavailable'}</span></div>
            <span class:healthy={readiness.daemon_running} class="pill">{readiness.daemon_running ? 'Healthy' : 'Check'}</span>
          </div>
          <div class="status-row">
            <span class:ok={readiness.session_secure} class="status-icon"><ShieldCheck size={19} /></span>
            <div><strong>Admin session</strong><span>{readiness.session_secure ? 'Secure local session' : 'Session required'}</span></div>
            <span class:healthy={readiness.session_secure} class="pill">{readiness.session_secure ? 'Secure' : 'Check'}</span>
          </div>
          <div class="status-row">
            <span class:ok={readiness.cache_connected} class="status-icon"><Database size={19} /></span>
            <div><strong>Cache</strong><span>{readiness.cache_connected ? 'Connected' : 'Not connected'}</span></div>
            <span class:healthy={readiness.cache_connected} class="pill">{readiness.cache_connected ? 'Ready' : 'Check'}</span>
          </div>
        </div>

        <div class="details">
          <div><span>Cache reference</span><strong>{readiness.cache_reference || 'Not configured'}</strong></div>
          <div><span>Schema</span><strong>{readiness.schema_version || 'Unknown'}</strong></div>
          <div><span>Last checked</span><strong>{readiness.checked_at ? new Date(readiness.checked_at).toLocaleTimeString() : 'Never'}</strong></div>
        </div>

        {#if readiness.api_version !== '1'}<p class="error" role="alert">UI/API version mismatch. Update the browser page and daemon binary together.</p>{/if}
        {#if error}<p class="error" role="alert">{error}</p>{/if}

        <div class="actions">
          <button class="primary" onclick={refresh} disabled={loading}><RefreshCw size={16} class={loading ? 'spin' : ''} />Refresh status</button>
          <button class="secondary" onclick={() => (active = 'Diagnostics')}><FolderCog size={16} />Open diagnostics</button>
        </div>

        <p class="privacy"><ShieldCheck size={15} />Bound to localhost. Credentials are never exposed to the browser.</p>
      {:else}
        <div class="intro section-intro">
          <p class="eyebrow">{active.toUpperCase()}</p>
          <h1>{active}</h1>
          <p>This foundation keeps the section visible and routable; operational controls arrive in the next UI slices.</p>
        </div>
        <button class="secondary" onclick={() => (active = 'Overview')}>Back to overview</button>
      {/if}
    </section>
  </main>
</div>
