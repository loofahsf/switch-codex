import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname } from 'node:path';
import { pkg, versionFiles } from './version-files.mjs';
for (const [path, content] of versionFiles()) {
  mkdirSync(dirname(path), { recursive: true });
  if (!existsSync(path) || readFileSync(path, "utf8") !== content) writeFileSync(path, content);
}
const lock = JSON.parse(readFileSync('package-lock.json', 'utf8'));
lock.version = lock.packages[''].version = pkg.version;
writeFileSync('package-lock.json', JSON.stringify(lock, null, 2)+'\n');
console.log(`Version metadata generated: ${pkg.version}`);
