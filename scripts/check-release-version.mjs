import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';

const packageJson = JSON.parse(readFileSync('package.json', 'utf8'));
const packageLock = JSON.parse(readFileSync('package-lock.json', 'utf8'));
const tauriConfig = JSON.parse(readFileSync('src-tauri/tauri.conf.json', 'utf8'));
const cargoToml = readFileSync('src-tauri/Cargo.toml', 'utf8');
const cargoLock = readFileSync('src-tauri/Cargo.lock', 'utf8');

const cargoVersion = cargoToml.match(/^version\s*=\s*"([^"]+)"/m)?.[1];
const cargoLockVersion = cargoLock.match(
  /\[\[package\]\]\nname = "switch-codex"\nversion = "([^"]+)"/,
)?.[1];
const expectedVersion = packageJson.version;
const versions = {
  'package-lock.json': packageLock.version,
  'package-lock.json packages[""]': packageLock.packages?.['']?.version,
  'src-tauri/Cargo.toml': cargoVersion,
  'src-tauri/Cargo.lock': cargoLockVersion,
  'src-tauri/tauri.conf.json': tauriConfig.version,
};
const errors = [];

if (!/^\d+\.\d+\.\d+$/.test(expectedVersion)) {
  errors.push(`package.json version must be MAJOR.MINOR.PATCH, received: ${expectedVersion}`);
}

for (const [file, version] of Object.entries(versions)) {
  if (version !== expectedVersion) {
    errors.push(`${file} version must match package.json (${expectedVersion}), received: ${version}`);
  }
}

const tagName = `v${expectedVersion}`;
const existingTag = execFileSync('git', ['tag', '--list', tagName], {
  encoding: 'utf8',
}).trim();

if (existingTag === tagName) {
  errors.push(`Git tag ${tagName} already exists; bump the version before committing release-triggering code.`);
}

if (errors.length > 0) {
  console.error('Release version check failed:\n- ' + errors.join('\n- '));
  process.exit(1);
}

console.log(`Release version check passed: ${tagName} is new and all version files match.`);
