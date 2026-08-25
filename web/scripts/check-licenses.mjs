import { readFileSync } from 'node:fs';

const lock = JSON.parse(readFileSync(new URL('../package-lock.json', import.meta.url), 'utf8'));
const allowed = new Set(['Apache-2.0', 'BSD-3-Clause', 'ISC', 'MIT']);
const failures = [];
const counts = new Map();

for (const [name, metadata] of Object.entries(lock.packages || {})) {
  if (!name) continue;
  const license = metadata.license || 'UNKNOWN';
  counts.set(license, (counts.get(license) || 0) + 1);
  if (!allowed.has(license)) failures.push(`${name}: ${license}`);
}

if (failures.length) {
  console.error(`Admin UI dependency license gate failed:\n${failures.join('\n')}`);
  process.exit(1);
}

console.log(`Admin UI dependency licenses verified: ${[...counts].sort().map(([license, count]) => `${license}=${count}`).join(', ')}`);
