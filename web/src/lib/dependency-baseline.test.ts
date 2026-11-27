import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const lock = JSON.parse(readFileSync(new URL('../../package-lock.json', import.meta.url), 'utf8')) as {
  packages: Record<string, { version?: string }>;
};
const copies = (name: string) => Object.entries(lock.packages).filter(([path]) =>
  path === `node_modules/${name}` || path.endsWith(`/node_modules/${name}`));

// Offline regression for #225, not a replacement for current advisory triage.
// A new major line needs a new compatibility/applicability assessment.
describe('reviewed Admin dependency baseline', () => {
  it.each([
    ['@sveltejs/adapter-static', '3.0.11'], // GHSA-jf9x-2fg2-hx4w
    ['@sveltejs/kit', '2.70.4'], // backported route-resolution fixes
    ['vite', '7.3.7'], // dev-server/editor boundary fixes
    ['vitest', '4.1.12'], // both mocker fixes; removes the old worker pool
    ['@vitest/mocker', '4.1.12'], // GHSA-82fw-gwwq-j7x9, GHSA-qxg6-89mg-rqv8
    ['devalue', '5.9.3'], // all seven reported 5.9.2-and-earlier advisories
    ['source-map-js', '1.2.2'] // GHSA-68fv-2mgg-jv7q
  ])('keeps every %s copy on its patched release line', (name, floor) => {
    const installed = copies(name);
    expect(installed.length).toBeGreaterThan(0);
    const [major, minor, patch] = floor.split('.').map(Number);
    for (const [path, metadata] of installed) {
      const version = metadata.version ?? '';
      expect(version, path).toMatch(/^\d+\.\d+\.\d+$/);
      const [actualMajor, actualMinor, actualPatch] = version.split('.').map(Number);
      expect(actualMajor, path).toBe(major);
      expect(actualMinor > minor || (actualMinor === minor && actualPatch >= patch), path).toBe(true);
    }
  });

  it('does not retain Vitest 3 worker copies through another parent', () => {
    expect(copies('tinypool')).toHaveLength(0);
    expect(copies('vite-node')).toHaveLength(0);
  });
});
