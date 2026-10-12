'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const guard = path.resolve(__dirname, '../../scripts/check-local-artifacts.cjs');
const { pathReason, contentReason } = require(guard);
const png = Buffer.from('89504e470d0a1a0a00000000', 'hex');
// Assemble forbidden fixture contents at runtime: no embedded capture in this source.
const embedded = ['<img src="data:image', '/png;base64,PRIVATE_PAYLOAD">'].join('');
const zero = '0'.repeat(40);

function repository(t) {
  const dir = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'parkrr-artifact-guard-'));
  const env = { ...process.env, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: path.join(dir, 'no-global-config') };
  for (const key of ['GIT_DIR', 'GIT_WORK_TREE', 'GIT_INDEX_FILE', 'GIT_COMMON_DIR', 'GIT_OBJECT_DIRECTORY', 'GIT_ALTERNATE_OBJECT_DIRECTORIES']) delete env[key];
  const hooks = path.join(dir, 'empty-hooks');
  fs.mkdirSync(hooks);
  function git(...args) {
    const result = spawnSync('git', ['-c', 'core.hooksPath=' + hooks, '-c', 'user.name=Guard Test', '-c', 'user.email=guard@example.invalid', ...args], { cwd: dir, env, encoding: 'utf8', windowsHide: true });
    assert.equal(result.status, 0, 'isolated Git setup: ' + result.stderr);
    return result.stdout.trim();
  }
  git('init', '--quiet');
  function write(name, content = 'source\n') {
    const file = path.join(dir, name);
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.writeFileSync(file, content);
  }
  function stage(name, content) { write(name, content); git('add', '-f', '--', name); }
  function commit() { git('commit', '--quiet', '--allow-empty', '-m', 'isolated guard fixture'); return git('rev-parse', 'HEAD'); }
  function check(args = ['--index'], input = '') {
    return spawnSync(process.execPath, [guard, ...args], { cwd: dir, env, input, encoding: 'utf8', windowsHide: true });
  }
  t.after(() => {
    // Only our uniquely created temp repository is removed; never any user directory.
    assert.equal(path.dirname(dir), fs.realpathSync(os.tmpdir()));
    assert.ok(path.basename(dir).startsWith('parkrr-artifact-guard-'));
    fs.rmSync(dir, { recursive: true, force: true });
  });
  return { dir, env, git, write, stage, commit, check };
}
function blocked(result, expectedPath) {
  assert.equal(result.status, 1, result.stdout + result.stderr);
  if (expectedPath) assert.ok(result.stderr.includes(JSON.stringify(expectedPath)), result.stderr);
  assert.ok(!result.stderr.includes('PRIVATE_PAYLOAD'), 'must not dump blob content');
}
function passed(result) { assert.equal(result.status, 0, result.stdout + result.stderr); }

test('policy blocks generated paths but preserves source fixtures and product assets', () => {
  for (const p of [
    'docs/anwendungsreview/index.html', 'docs/screenshots/readme.txt',
    '.impeccable/review/check.json', '.impeccable/critique/report.md', '.impeccable/booking.html',
    'docs/ui-review-20261012.html', 'docs/ui-2026-10-12.css', 'docs/report-2026.js',
    'nested/qa-screenshots/a.txt', 'foo/mockups/report.html', 'foo/mockup-data/records.json',
    'any/output/data.json', 'any/local-artifacts/results.txt', 'tests/a11y/test-results-foo/out.txt',
    'playwright-report/index.html', '.tmp/check.json', 'tmp/go.mod',
    'elsewhere/PREVIEW.PNG', 'elsewhere/image.avif', 'record.har', 'record.webm', 'backup.tar.gz',
  ]) assert.ok(pathReason(p), p);
  // These exceptions are deliberate: source fixtures aren't generated mock datasets.
  for (const p of [
    'tests/a11y/helpers/ui-fixture.js', 'tests/a11y/helpers/overhaul-fixture.js',
    'tests/frontend/local-artifacts.test.js', 'tests/a11y/helpers/before-after-gallery.html',
    'web/static/icons/favicon.png', 'web/static/img/vehicles/car.webp',
    'web/static/css/style.css', 'docs/guide.md', '.impeccable/config.json',
    'internal/backup/backup.go',
  ]) assert.equal(pathReason(p), null, p);
  assert.ok(pathReason('web/static/img/screenshots/capture.png'), 'asset exception does not override generated directories');
  assert.ok(pathReason('web/static/img/recording.har'), 'asset exception applies only to raster images');
});

test('renamed binary captures, archives, recordings and embedded raster URLs are detected', () => {
  assert.equal(contentReason(png), 'raster');
  assert.equal(contentReason(Buffer.from('504b030400000000', 'hex')), 'recording or archive');
  assert.equal(contentReason(Buffer.from('00000018667479706d703432', 'hex')), 'recording or archive');
  assert.equal(contentReason(Buffer.from(embedded)), 'embedded raster image');
  assert.equal(contentReason(Buffer.from(embedded, 'utf16le')), 'embedded raster image');
  assert.equal(contentReason(Buffer.from('const fixture = { name: "Demo", amount: 20 };')), null);
  assert.equal(contentReason(Buffer.from('data:image/svg+xml,<svg/>')), null, 'code-native vectors are allowed');
  assert.equal(contentReason(Buffer.from(['"data:image', '/png;base64," + encode(bytes)'].join(''))), null, 'runtime image constructors contain no captured payload');
  assert.equal(contentReason(Buffer.from(['optional data:image', '/png;base64,… URL'].join(''))), null, 'documentation prefix is not an embedded capture');
});

test('index scan reads staged blobs rather than modified working-tree contents', t => {
  const r = repository(t);
  r.stage('report.html', embedded);
  r.write('report.html', 'clean working copy');
  blocked(r.check(), 'report.html');
  r.git('add', '--', 'report.html');
  r.write('report.html', embedded);
  passed(r.check());
});

test('index scan checks all tracked entries, filename escaping and renamed raster content', t => {
  const r = repository(t);
  r.stage('docs/screenshots/space and ü.png', png);
  r.commit();
  blocked(r.check(), 'docs/screenshots/space and ü.png');
  r.git('rm', '--', 'docs/screenshots/space and ü.png');
  r.stage('innocent.txt', png);
  blocked(r.check(), 'innocent.txt');
  r.git('rm', '-f', '--', 'innocent.txt');
  r.stage('web/static/icons/icon.png', png);
  r.stage('tests/a11y/helpers/ui-fixture.js', 'module.exports = { name: "Demo" };');
  passed(r.check());
});

test('range detects artifacts added and then deleted before the tip', t => {
  const r = repository(t);
  r.stage('app.js'); const base = r.commit();
  r.stage('docs/screenshots/transient.png', png); r.commit();
  r.git('rm', '--', 'docs/screenshots/transient.png'); const tip = r.commit();
  passed(r.check());
  blocked(r.check(['--range', base + '..' + tip]), 'docs/screenshots/transient.png');
  blocked(r.check(['--pre-push'], `refs/heads/main ${tip} refs/heads/main ${base}\n`), 'docs/screenshots/transient.png');
});

test('pre-push checks every ref and all history on a new branch', t => {
  const r = repository(t);
  r.stage('app.js'); const clean = r.commit();
  r.stage('output/data.json', '{}'); const dirty = r.commit();
  r.git('rm', '--', 'output/data.json'); const tip = r.commit();
  blocked(r.check(['--pre-push'], `refs/heads/clean ${clean} refs/heads/clean ${zero}\nrefs/heads/new ${tip} refs/heads/new ${zero}\n`), 'output/data.json');
  // A removal-only commit is safe when the artifact was already on the remote.
  passed(r.check(['--pre-push'], `refs/heads/main ${tip} refs/heads/main ${dirty}\n`));
  passed(r.check(['--pre-push'], `(delete) ${zero} refs/heads/old ${tip}\n`));
  passed(r.check(['--pre-push'], ''));
});

test('unknown remote SHA, missing refs, malformed input and non-commit tags fail closed', t => {
  const r = repository(t);
  r.stage('app.js'); const tip = r.commit();
  const blob = r.git('rev-parse', 'HEAD:app.js');
  for (const input of [
    `refs/heads/main ${tip} refs/heads/main ${'f'.repeat(40)}\n`,
    `refs/tags/blob ${blob} refs/tags/blob ${zero}\n`,
    `refs/heads/main ${tip} missing\n`,
  ]) blocked(r.check(['--pre-push'], input));
  blocked(r.check(['--range', 'missing..HEAD']));
  blocked(r.check(['--range', 'HEAD...HEAD']));
  r.git('tag', '-a', 'commit-tag', '-m', 'commit-backed tag');
  const tag = r.git('rev-parse', 'commit-tag');
  passed(r.check(['--pre-push'], `refs/tags/commit-tag ${tag} refs/tags/commit-tag ${zero}\n`));
  r.git('tag', '-a', 'blob-tag', blob, '-m', 'not a commit');
  blocked(r.check(['--pre-push'], `refs/tags/blob-tag ${r.git('rev-parse', 'blob-tag')} refs/tags/blob-tag ${zero}\n`));
});

test('shallow and unmerged repositories fail closed', t => {
  const r = repository(t);
  r.stage('app.js'); const tip = r.commit();
  fs.writeFileSync(path.join(r.dir, '.git/shallow'), tip + '\n');
  blocked(r.check(['--pre-push'], `refs/heads/new ${tip} refs/heads/new ${zero}\n`));
  fs.unlinkSync(path.join(r.dir, '.git/shallow'));
  const blob = r.git('rev-parse', 'HEAD:app.js');
  r.git('update-index', '--force-remove', 'app.js');
  const result = spawnSync('git', ['update-index', '--index-info'], { cwd: r.dir, input: `100644 ${blob} 1\tapp.js\n`, encoding: 'utf8', windowsHide: true });
  assert.equal(result.status, 0);
  blocked(r.check());
});

test('real pre-push hook allows a clean local push and rejects a removed embedded image', t => {
  const r = repository(t);
  const remote = path.join(r.dir, 'local-remote.git');
  r.git('init', '--bare', '--quiet', remote);
  r.git('remote', 'add', 'local-guard-test', remote);
  r.stage('scripts/check-local-artifacts.cjs', fs.readFileSync(guard));
  r.stage('app.js');
  const clean = r.commit();
  const hookDirectory = path.join(r.dir, '.git/hooks');
  fs.mkdirSync(hookDirectory, { recursive: true });
  const hook = path.join(hookDirectory, 'pre-push');
  fs.writeFileSync(hook, fs.readFileSync(path.resolve(__dirname, '../../.githooks/pre-push'), 'utf8').replace(/\r\n/g, '\n'));
  fs.chmodSync(hook, 0o755);
  function push() {
    // The remote is an explicit filesystem path inside our temp repo: no network.
    return spawnSync('git', ['-c', 'core.hooksPath=' + hookDirectory, 'push', 'local-guard-test', 'HEAD:refs/heads/main'], {
      cwd: r.dir, env: r.env, encoding: 'utf8', windowsHide: true,
    });
  }
  passed(push());
  assert.equal(r.git('--git-dir=' + remote, 'rev-parse', 'refs/heads/main'), clean);
  r.stage('report.html', embedded); r.commit();
  r.git('rm', '--', 'report.html'); r.commit();
  passed(r.check()); // A tip-only guard would incorrectly permit this push.
  blocked(push(), 'report.html');
  assert.equal(r.git('--git-dir=' + remote, 'rev-parse', 'refs/heads/main'), clean, 'blocked push must leave local remote unchanged');
});

test('hook templates preserve pre-push stdin and do not configure or replace other hooks', () => {
  for (const hook of ['pre-commit', 'pre-push']) {
    const body = fs.readFileSync(path.resolve(__dirname, '../../.githooks', hook), 'utf8');
    assert.ok(body.startsWith('#!/bin/sh\n') || body.startsWith('#!/bin/sh\r\n'));
    assert.ok(body.includes('set -eu'));
    assert.ok(body.includes('exec node scripts/check-local-artifacts.cjs --' + (hook === 'pre-push' ? 'pre-push' : 'index')));
    assert.ok(!/git config|post-commit\s*>|post-checkout\s*>/.test(body));
  }
});
