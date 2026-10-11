import { expect, test, type Page } from '@playwright/test';
import { execFileSync, spawn, type ChildProcessWithoutNullStreams } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { createInterface } from 'node:readline';

async function bounded<T>(promise: Promise<T>, milliseconds: number, message: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([promise, new Promise<never>((_, reject) => {
      timer = setTimeout(() => reject(new Error(message)), milliseconds);
    })]);
  } finally { clearTimeout(timer); }
}

async function ready(page: Page): Promise<void> {
  await expect(page.getByRole('heading', { name: 'Execution and observation', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Copy safe context', exact: true })).toBeEnabled();
}

type Authority = { url: string; cache: string; repo: string; job: string; registration: string; sources: Array<{ id: string; ref: string }> };

test.use({ actionTimeout: 5000 });

test('actual listed entities and two Git sources round trip through Service without sibling fallback', async ({ page, context }) => {
  test.setTimeout(120000);
  const directory = mkdtempSync(join(tmpdir(), 'gitcode-o5-nav-'));
  let child: ChildProcessWithoutNullStreams | undefined;
  let lines: ReturnType<typeof createInterface> | undefined;
  try {
    const binary = join(directory, 'navigation.test');
    try { execFileSync('go', ['test', '-c', '-o', binary, './internal/servicectl'], { cwd: resolve('..'), stdio: 'pipe', timeout: 60000 }); }
    catch { throw new Error('Offline navigation qualification fixture could not compile.'); }
    child = spawn(binary, ['-test.run=^TestO5ActualAdminEntityAndSourceNavigation$', '-test.timeout=100s'], {
      cwd: resolve('..'), env: { ...process.env, TMPDIR: directory, GITCODE_MCP_O5_NAVIGATION_BROWSER: '1' }, stdio: 'pipe'
    });
    child.stderr.resume();
    let authority: Authority | undefined;
    let removed = false;
    let failed = false;
    let exited = false;
    let exitCode: number | null = null;
    const exit = new Promise<void>(done => child!.once('exit', code => { exited = true; exitCode = code; done(); }));
    child.once('error', () => { failed = true; });
    lines = createInterface({ input: child.stdout });
    lines.on('line', line => {
      if (line === 'O5_NAVIGATION_REMOVED') { removed = true; return; }
      if (!line.startsWith('O5_NAVIGATION ')) return;
      try {
        const value = JSON.parse(line.slice('O5_NAVIGATION '.length));
        if (authority || !/^http:\/\/127\.0\.0\.1:\d+$/.test(value.url) || !/^cache-[a-z0-9]+$/.test(value.cache) || value.repo !== 'owner/repo' || !/^job-[a-z0-9]+$/.test(value.job) || !/^cache-reg-[a-f0-9]+$/.test(value.registration) || !Array.isArray(value.sources) || value.sources.length !== 2 || value.sources.some((s: any) => typeof s.id !== 'string' || !/^[a-z0-9-]+$/.test(s.id) || !/^source-[a-f0-9]{32}$/.test(s.ref)) || value.sources[0].id === value.sources[1].id || value.sources[0].ref === value.sources[1].ref) { failed = true; return; }
        authority = value;
      } catch { failed = true; }
    });
    await expect.poll(() => Boolean(authority) || failed || exited, { timeout: 30000 }).toBe(true);
    if (!authority || failed || exited) throw new Error('Offline navigation fixture protocol failed.');
    const a = authority;
    const session = await context.request.post(a.url + '/api/admin/v1/session', { headers: { Origin: a.url }, data: { launch_token: 'qualification-launch' } });
    expect(session.status()).toBe(201);
    await page.addInitScript(() => {
      (window as any).__navigationCopy = '';
      Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async (value: string) => { (window as any).__navigationCopy = value; } } });
    });
    const methods: string[] = [];
    page.on('request', request => { if (request.url().startsWith(a.url + '/api/')) methods.push(request.method()); });
    // Broad snapshots and SSE come from the real Manager/registry, not routes.
    const snapshotResponse = await context.request.get(a.url + '/api/admin/v1/snapshot');
    expect(snapshotResponse.status()).toBe(200);
    const snapshot = await snapshotResponse.json();
    const cache = snapshot.caches.find((c: any) => c.cache_ref === a.cache);
    const repo = cache.repositories.find((r: any) => r.repo_id === a.repo);
    const job = snapshot.jobs.find((j: any) => j.id === a.job);
    const registration = snapshot.maintenance.find((r: any) => r.registration_id === a.registration);
    expect(repo.documentation.sources).toHaveLength(2);
    const publicSnapshot = JSON.stringify(snapshot);
    expect(publicSnapshot).not.toContain(directory);
    expect(publicSnapshot).not.toContain('synthetic-private');

    await page.goto(a.url + '/?view=Caches&cache=' + a.cache);
    await page.getByRole('button', { name: 'Observe cache in Service', exact: true }).click();
    await ready(page);
    expect(new URL(page.url()).searchParams.get('obs_cache_ref')).toBe(cache.observation_refs.cache_ref);
    await page.goto(a.url + '/?view=Caches&cache=' + a.cache + '&repo=owner%2Frepo');
    await page.getByRole('button', { name: 'Observe repository in Service', exact: true }).click();
    await ready(page);
    expect(new URL(page.url()).searchParams.get('obs_repo_ref')).toBe(repo.observation_refs.repo_ref);
    await page.goto(a.url + '/?view=Jobs&job=' + a.job);
    await page.getByRole('button', { name: 'Observe job in Service', exact: true }).click();
    await ready(page);
    expect(new URL(page.url()).searchParams.get('obs_job_ref')).toBe(job.observation_refs.job_ref);
    await page.getByRole('list', { name: 'Retained catalog events' }).getByRole('button', { name: 'Job Terminal', exact: true }).click();
    await page.getByRole('button', { name: 'Open exact job', exact: true }).click();
    expect(new URL(page.url()).searchParams.get('job')).toBe(a.job);
    await expect(page.getByRole('button', { name: 'Observe job in Service', exact: true })).toBeVisible();

    await page.goto(a.url + '/?view=Maintenance&registration=' + a.registration);
    await page.getByRole('button', { name: 'Observe registration in Service', exact: true }).click();
    await ready(page);
    expect(new URL(page.url()).searchParams.get('obs_registration_ref')).toBe(registration.observation_refs.registration_ref);
    // The actual enrollment event has no stronger job/source identity.
    await page.getByRole('combobox', { name: 'Outcome', exact: true }).selectOption('enrolled');
    const enrollment = page.getByRole('list', { name: 'Retained catalog events' }).getByRole('button', { name: 'Maintenance State Changed', exact: true });
    await expect(enrollment).toHaveCount(1);
    await enrollment.click();
    await page.getByRole('button', { name: 'Open exact maintenance', exact: true }).click();
    expect(new URL(page.url()).searchParams.get('registration')).toBe(a.registration);

    // Visit B first, then A, which the fixture will remove after selection.
    for (const source of [a.sources[1], a.sources[0]]) {
      await page.goto(a.url + '/?view=Caches&cache=' + a.cache + '&repo=owner%2Frepo&tab=documentation&doc_source_ref=' + source.ref);
      const authoritySelect = page.getByRole('combobox', { name: 'Git authority', exact: true });
      await expect(authoritySelect).toHaveValue(source.id);
      await page.getByRole('button', { name: 'Observe exact documentation source in Service', exact: true }).click();
      await ready(page);
      expect(new URL(page.url()).searchParams.get('obs_source_ref')).toBe(source.ref);
      await page.getByRole('combobox', { name: 'Outcome', exact: true }).selectOption('degraded');
      const transition = page.getByRole('list', { name: 'Retained catalog events' }).getByRole('button', { name: 'Maintenance State Changed', exact: true });
      await expect(transition).toHaveCount(1);
      await transition.click();
      await ready(page);
      await page.getByRole('button', { name: 'Copy safe context', exact: true }).click();
      const copy = JSON.parse(await page.evaluate(() => (window as any).__navigationCopy));
      expect(copy.event.source_ref).toBe(source.ref);
      expect(copy.event.outcome).toBe('degraded');
      expect(copy.event.maintenance_scope).toBe('repository_docs');
      expect(copy.event.registration_ref).toBe(registration.observation_refs.registration_ref);
      expect(Buffer.byteLength(JSON.stringify(copy), 'utf8')).toBeLessThanOrEqual(16*1024);
      expect(JSON.stringify(copy)).not.toContain('synthetic-private');
      await page.getByRole('button', { name: 'Open exact documentation source', exact: true }).click();
      expect(new URL(page.url()).searchParams.get('doc_source_ref')).toBe(source.ref);
      await expect(authoritySelect).toHaveValue(source.id);
    }
    child.stdin.write('remove\n');
    await expect.poll(() => removed || failed || exited, { timeout: 5000 }).toBe(true);
    if (!removed || failed || exited) throw new Error('Actual source removal did not reach its durable boundary.');
    // Native snapshot SSE refreshes real listed authority while this exact
    // source remains selected; no navigation/Refresh or fallback selection.
    await expect(page.getByText('Selected documentation source is unavailable. No sibling fallback was selected.')).toBeVisible({ timeout: 5000 });
    await expect(page.getByRole('combobox', { name: 'Git authority', exact: true })).toHaveValue('');
    await page.getByRole('button', { name: 'Preview exact plan', exact: true }).click();
    await expect(page.getByText('Selected documentation source is unavailable. Select an authority explicitly; no sibling fallback is allowed.')).toBeVisible();
    expect(methods.every(method => method === 'GET')).toBe(true);
    expect(await page.locator('body').innerText()).not.toContain('synthetic-private');
    child.stdin.write('continue\n');
    await bounded(exit, 5000, 'Navigation qualification fixture did not join.');
    expect(exitCode).toBe(0);
  } finally {
    lines?.close();
    if (child && child.pid && child.exitCode === null && child.signalCode === null) {
      const stopped = new Promise<void>(done => child!.once('exit', () => done()));
      child.kill('SIGTERM');
      try { await bounded(stopped, 2000, 'Navigation qualification fixture did not stop.'); }
      catch { child.kill('SIGKILL'); await bounded(stopped, 2000, 'Navigation qualification fixture did not join.'); }
    }
    rmSync(directory, { recursive: true, force: true });
  }
});
