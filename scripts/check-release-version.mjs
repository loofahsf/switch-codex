import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { pkg, versionFiles, wailsVersion } from './version-files.mjs';
const errors = [];
const lock = JSON.parse(readFileSync('package-lock.json', 'utf8'));
if (lock.version !== pkg.version || lock.packages[''].version !== pkg.version) errors.push('package-lock.json version is stale');
for (const [path, expected] of versionFiles()) {
  try { if (readFileSync(path, 'utf8') !== expected) errors.push(`${path} is stale; run npm run version:sync`); }
  catch { errors.push(`${path} is missing; run npm run version:sync`); }
}
if (pkg.dependencies['@wailsio/runtime'] !== wailsVersion.slice(1) || !readFileSync('go.mod', 'utf8').includes(`github.com/wailsapp/wails/v3 ${wailsVersion}\n`)) errors.push('Wails Go, CLI and frontend runtime versions must be pinned together');
const git = (...args) => execFileSync('git', args, { encoding: 'utf8' }).trim();
const tag = `v${pkg.version}`;
const tagRef = (process.env.GITHUB_REF || '').startsWith('refs/tags/') ? process.env.GITHUB_REF.slice(10) : null;
const taggedBuild = process.argv.includes('--tagged') || process.env.SWITCH_CODEX_TAGGED_BUILD === '1' || tagRef !== null;
if (tagRef && tagRef !== tag) errors.push(`Tag ${tagRef} does not match ${tag}`);
if (git('tag', '--list', tag) === tag) {
  if (!taggedBuild || git('rev-parse', `${tag}^{commit}`) !== git('rev-parse', 'HEAD')) errors.push(`Tag ${tag} already exists; use a new version (only a build of that exact tag may reuse it)`);
} else if (taggedBuild) errors.push(`Tagged build requested but ${tag} is missing`);
if (errors.length) { console.error(errors.join('\n')); process.exit(1); }
console.log(`Version check passed: ${tag}${taggedBuild ? ' (tagged build)' : ' (unpublished)'}`);
