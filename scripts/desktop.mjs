import { spawn, spawnSync } from 'node:child_process';
import { chmodSync, copyFileSync, existsSync, mkdirSync, readFileSync, readdirSync, renameSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { pkg, wailsVersion } from './version-files.mjs';

const root = fileURLToPath(new URL('..', import.meta.url));
process.chdir(root);
const [action = 'package', platform = process.platform === 'win32' ? 'windows' : process.platform, arch = process.arch === 'x64' ? 'amd64' : process.arch] = process.argv.slice(2);
const dev = process.argv.includes('--dev');
const toolsDir = resolve('.cache/tools');
const cli = process.env.WAILS3 || join(toolsDir, process.platform === 'win32' ? 'wails3.exe' : 'wails3');
function run(command, args, env = {}, cwd = root) {
  const result = spawnSync(command, args, { cwd, stdio: 'inherit', env: { ...process.env, ...env } });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`${command} exited with ${result.status ?? result.signal}`);
}
function npm(script) {
  if (process.env.npm_execpath) run(process.execPath, [process.env.npm_execpath, 'run', script]);
  else {
    // Only fixed script names reach the Windows command shim.
    const result = spawnSync(process.platform === 'win32' ? 'npm.cmd' : 'npm', ['run', script], { stdio: 'inherit', shell: process.platform === 'win32' });
    if (result.status !== 0) throw new Error(`npm run ${script} failed`);
  }
}
function ensureCLI() {
  if (!existsSync(cli) && !process.env.WAILS3) {
    mkdirSync(toolsDir, { recursive: true });
    run('go', ['install', `github.com/wailsapp/wails/v3/cmd/wails3@${wailsVersion}`], { GOBIN: toolsDir });
  }
  const result = spawnSync(cli, ['version'], { encoding: 'utf8' });
  if (result.status !== 0 || !`${result.stdout}${result.stderr}`.includes(wailsVersion)) throw new Error(`Wails CLI must be ${wailsVersion}`);
}
function generateBindings() {
  // go:embed must match even on a clean checkout. This placeholder is never shipped.
  mkdirSync('dist', { recursive: true });
  if (!existsSync('dist/index.html')) writeFileSync('dist/index.html', '<!doctype html><title>Build pending</title>');
  run(cli, ['generate', 'bindings', '-ts', '-i', '-d', 'src/bindings']);
}
function bindingSnapshot(path = 'src/bindings') {
  const files = {};
  if (existsSync(path)) for (const item of readdirSync(path, { withFileTypes: true })) {
    const name = join(path, item.name);
    if (item.isDirectory()) Object.assign(files, bindingSnapshot(name));
    else files[name] = readFileSync(name, 'utf8');
  }
  return files;
}
function serializedBindings() { return JSON.stringify(Object.entries(bindingSnapshot()).sort()); }
function bundle(binary, output, isDev) {
  const app = join(output, 'Switch Codex.app');
  rmSync(app, { force: true, recursive: true });
  mkdirSync(join(app, 'Contents/MacOS'), { recursive: true });
  mkdirSync(join(app, 'Contents/Resources'), { recursive: true });
  copyFileSync(binary, join(app, 'Contents/MacOS/switch-codex'));
  chmodSync(join(app, 'Contents/MacOS/switch-codex'), 0o755);
  copyFileSync(`build/darwin/Info${isDev ? '.dev' : ''}.plist`, join(app, 'Contents/Info.plist'));
  copyFileSync('build/darwin/icons.icns', join(app, 'Contents/Resources/icons.icns'));
  run('codesign', ['--force', '--deep', '--sign', '-', app]);
  return app;
}
ensureCLI();
if (action === 'setup') process.exit(0);
if (action === 'bindings' || action === 'check-bindings') {
  const before = serializedBindings();
  generateBindings();
  if (action === 'check-bindings' && before !== serializedBindings()) throw new Error('Generated bindings are stale; run npm run bindings and commit src/bindings');
  process.exit(0);
}
if (action === 'dev') {
  const child = spawn(cli, ['dev', '-config', 'build/config.yml'], { stdio: 'inherit', env: { ...process.env, PATH: `${dirname(cli)}${process.platform === 'win32' ? ';' : ':'}${process.env.PATH}` } });
  for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill(signal));
  child.on('exit', code => process.exit(code || 0));
} else if (action === 'run') {
  const binary = process.platform === 'darwin'
    ? 'bin/dev/Switch Codex.app/Contents/MacOS/switch-codex'
    : `bin/dev/switch-codex${process.platform === 'win32' ? '.exe' : ''}`;
  run(resolve(binary), []);
} else {
  if (!['build', 'package'].includes(action)) throw new Error(`Unknown task: ${action}`);
  if (!((platform === 'darwin' && ['arm64','amd64'].includes(arch)) || (['windows','linux'].includes(platform) && arch === 'amd64'))) throw new Error('Supported targets: darwin arm64/amd64, windows amd64, linux amd64');
  if (platform === 'darwin' && process.platform !== 'darwin') throw new Error('macOS builds require macOS and Xcode Command Line Tools');
  if (platform === 'linux' && process.platform !== 'linux') throw new Error('Linux builds require a Linux host with GTK4 and WebKitGTK 6.0 development packages');
  run(process.execPath, ['scripts/sync-version.mjs']);
  run(process.execPath, ['scripts/check-release-version.mjs']);
  generateBindings();
  npm('build:web');
  const output = resolve(dev ? 'bin/dev' : `bin/${platform}-${arch}`);
  mkdirSync(output, { recursive: true });
  const binary = join(output, `switch-codex${platform === 'windows' ? '.exe' : ''}`);
  const env = { GOOS: platform, GOARCH: arch, CGO_ENABLED: platform === 'windows' ? '0' : '1' };
  if (platform === 'darwin') {
    const nativeArch = arch === 'amd64' ? 'x86_64' : 'arm64';
    Object.assign(env, { CGO_CFLAGS: `-arch ${nativeArch} -mmacosx-version-min=12.0`, CGO_LDFLAGS: `-arch ${nativeArch} -mmacosx-version-min=12.0`, MACOSX_DEPLOYMENT_TARGET: '12.0' });
  }
  const syso = `wails_windows_${arch}.syso`;
  try {
    if (platform === 'windows') run(cli, ['generate','syso','-arch',arch,'-icon','build/windows/icon.ico','-manifest','build/windows/wails.exe.manifest','-info','build/windows/info.json','-out',syso]);
    run('go', ['build', ...(dev ? ['-gcflags=all=-l'] : ['-tags','production','-trimpath','-ldflags',`-s -w${platform === 'windows' ? ' -H windowsgui' : ''}`]), '-buildvcs=false','-o',binary,'.'], env);
  } finally { if (platform === 'windows') rmSync(syso, { force: true }); }
  const app = platform === 'darwin' ? bundle(binary, output, dev) : null;
  if (action === 'package') {
    if (dev) throw new Error('Cannot package a development build');
    mkdirSync('release', { recursive: true });
    const extension = platform === 'darwin' ? '.dmg' : platform === 'windows' ? '-setup.exe' : '.deb';
    const target = resolve(`release/Switch-Codex-${pkg.version}-${platform}-${arch}${extension}`);
    if (app) {
      const stage = join(output, 'dmg');
      rmSync(stage, { force: true, recursive: true }); mkdirSync(stage);
      run('ditto', [app, join(stage, 'Switch Codex.app')]);
      symlinkSync('/Applications', join(stage, 'Applications'));
      run('hdiutil', ['create','-volname','Switch Codex','-srcfolder',stage,'-ov','-format','UDZO',target]);
      rmSync(stage, { force: true, recursive: true });
    } else if (platform === 'windows') {
      run(cli, ['generate','webview2bootstrapper','-dir','build/windows']);
      const makensis = process.env.MAKENSIS || (process.platform === 'win32' && existsSync('C:/Program Files (x86)/NSIS/makensis.exe') ? 'C:/Program Files (x86)/NSIS/makensis.exe' : 'makensis');
      run(makensis, ['-V2',`-DAPP_BINARY=${binary}`,`-DOUTPUT_FILE=${target}`,'installer.nsi'], {}, resolve('build/windows'));
    } else {
      const generated = resolve('release/switch-codex.deb');
      rmSync(generated, { force: true });
      run(cli, ['tool','package','-name','switch-codex','-format','deb','-config','build/linux/nfpm.yaml','-out','release'], { APP_VERSION: pkg.version, GOARCH: arch, LINUX_BINARY: binary });
      rmSync(target, { force: true });
      renameSync(generated, target);
    }
    console.log(`Package: ${target}`);
  }
}
