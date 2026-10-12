#!/usr/bin/env node
'use strict';

// Local-only artifact policy. Keep test SOURCE (including synthetic fixture helpers)
// and product artwork; never publish generated reviews, captures, or recordings.
// Usage: node scripts/check-local-artifacts.cjs [--index | --range base..tip | --pre-push]
// Pre-push reads Git's four-column ref updates from stdin. A new remote ref scans
// its entire history, deliberately preferring a false positive to an unseen leak.
const { spawnSync } = require('node:child_process');
const fs = require('node:fs');

const raster = /\.(?:png|jpe?g|gif|webp|avif|bmp|tiff?|ico|cur|icns|heic|heif|jxl)$/i;
const recording = /\.(?:har|mp4|m4v|mov|webm|mkv|avi|mpeg|mpg|wmv|flv|ogv|3gp)$/i;
const archive = /\.(?:zip|tar|tgz|gz|bz2|xz|7z|rar|zst|zstd)$/i;
const generatedDir = /(?:^|[-_.])(?:screenshots?|mockups?|mockup-data|captures?|outputs?|artifacts?|evidence|test-results|playwright-report|blob-report|graphify-out|backups)(?:$|[-_.])/i;
const imageAsset = p => /^web\/static\/(?:icons|img)\//.test(p);

function pathReason(p) {
  if (/^docs\/(?:anwendungsreview|screenshots)(?:\/|$)/i.test(p) ||
      /^\.impeccable\/(?:review|critique)(?:\/|$)/i.test(p) ||
      /^\.impeccable\/[^/]+\.html$/i.test(p) ||
      /^docs\/[^/]*20\d{2}[^/]*\.(?:html|css|js)$/i.test(p)) return 'local review';
  if (p.split('/').slice(0, -1).some(part => generatedDir.test(part) || /^(?:\.?tmp|temp|coverage)$/i.test(part))) return 'generated output';
  if (raster.test(p) && !imageAsset(p)) return 'raster capture outside product assets';
  if (recording.test(p) || archive.test(p)) return 'recording or archive';
  return null;
}

function contentReason(b) {
  const hex = b.subarray(0, 12).toString('hex');
  const head = b.subarray(0, 40).toString('latin1');
  if (/^(?:89504e470d0a1a0a|ffd8ff|474946383[79]61|424d|49492a00|4d4d002a|00000100|00000200|ff0a|0000000c4a584c20)/.test(hex) ||
      /^RIFF[\s\S]{4}WEBP/.test(head) || /^.{4}ftyp(?:avif|avis|heic|heix|hevc|hevx|mif1|msf1)/s.test(head)) return 'raster';
  if (/^(?:504b0304|504b0506|504b0708|1f8b|52617221|377abcaf271c|425a68|fd377a585a00|28b52ffd|1a45dfa3)/.test(hex) ||
      /^.{4}ftyp/s.test(head) || /^RIFF[\s\S]{4}AVI /.test(head) ||
      b.subarray(257, 262).toString() === 'ustar') return 'recording or archive';
  // Require payload: source-code URL constructors and documented prefixes are not
  // captures. NUL removal also detects ASCII data URLs in UTF-16 text.
  if (/data:image\/(?:png|apng|jpe?g|gif|webp|avif|bmp|tiff?|x-icon|vnd\.microsoft\.icon|heic|heif|jxl)[^\s"'<>]*,[a-z0-9+/%=]+/i.test(b.toString('utf8').replace(/\0/g, ''))) return 'embedded raster image';
  return null;
}

function git(args, input) {
  const r = spawnSync('git', args, {
    input, maxBuffer: 256 * 1024 * 1024, windowsHide: true,
    env: { ...process.env, GIT_NO_REPLACE_OBJECTS: '1', GIT_NO_LAZY_FETCH: '1' },
  });
  if (r.error || r.status !== 0) throw new Error('Git inspection failed; refusing unchecked content.');
  return r.stdout;
}

function commit(ref) {
  if (!ref || ref.startsWith('-')) throw new Error('Invalid revision.');
  return git(['rev-parse', '--verify', '--end-of-options', ref + '^{commit}']).toString().trim();
}

function entries(raw, index) {
  return raw.toString('utf8').split('\0').filter(Boolean).map(line => {
    const tab = line.indexOf('\t');
    if (tab < 0) throw new Error('Malformed Git listing.');
    const [mode, second, third] = line.slice(0, tab).split(' ');
    if ((index && third !== '0') || mode === '160000') throw new Error('Unmerged index or submodule cannot be inspected safely.');
    if (!['100644', '100755', '120000'].includes(mode) || (!index && second !== 'blob')) throw new Error('Unsupported Git object.');
    return { oid: index ? second : third, path: line.slice(tab + 1) };
  });
}

function scanner() {
  const blobs = new Map();
  const violations = new Map();
  function scan(files) {
    const pending = new Set();
    for (const f of files) {
      const reason = pathReason(f.path);
      if (reason) violations.set(f.path, reason);
      else if (!blobs.has(f.oid)) pending.add(f.oid);
    }
    if (pending.size) {
      const result = git(['cat-file', '--batch'], [...pending].join('\n') + '\n');
      let offset = 0;
      for (const oid of pending) {
        const end = result.indexOf(10, offset);
        const header = result.subarray(offset, end).toString();
        const match = /^([a-f0-9]+) blob (\d+)$/.exec(header);
        if (!match || match[1] !== oid) throw new Error('Missing or unreadable staged/history blob.');
        const size = Number(match[2]);
        offset = end + 1;
        if (!Number.isSafeInteger(size) || offset + size >= result.length || result[offset + size] !== 10) throw new Error('Incomplete blob inspection.');
        blobs.set(oid, contentReason(result.subarray(offset, offset + size)));
        offset += size + 1;
      }
      if (offset !== result.length) throw new Error('Unexpected blob inspection output.');
    }
    for (const f of files) {
      const reason = blobs.get(f.oid);
      if (reason && !(reason === 'raster' && imageAsset(f.path))) violations.set(f.path, reason);
    }
  }
  function history(tip, base) {
    if (git(['rev-parse', '--is-shallow-repository']).toString().trim() !== 'false') throw new Error('Shallow history cannot be checked safely. Fetch complete history first.');
    const args = ['rev-list', tip];
    if (base) args.push('^' + base);
    args.push('--');
    for (const oid of git(args).toString().trim().split('\n').filter(Boolean)) {
      scan(entries(git(['ls-tree', '-rz', '--full-tree', oid]), false));
    }
  }
  return { scan, history, violations };
}

function run(args, stdin = '') {
  const check = scanner();
  if (!args.length || (args.length === 1 && args[0] === '--index')) {
    check.scan(entries(git(['ls-files', '--stage', '-z']), true));
  } else if (args.length === 2 && args[0] === '--range') {
    const range = /^(.+?)\.\.([^.].*)$/.exec(args[1]);
    if (!range || range[1].includes('..') || range[2].includes('..')) throw new Error('Use exactly --range base..tip.');
    check.history(commit(range[2]), commit(range[1]));
  } else if (args.length === 1 && args[0] === '--pre-push') {
    for (const line of stdin.split(/\r?\n/).filter(Boolean)) {
      const parts = line.trim().split(/\s+/);
      if (parts.length !== 4 || !parts[0] || !parts[2] ||
          !parts.slice(1).filter((_, i) => i % 2 === 0).every(s => /^(?:[a-f0-9]{40}|[a-f0-9]{64})$/i.test(s))) throw new Error('Malformed pre-push ref input.');
      const [, local, , remote] = parts;
      if (/^0+$/.test(local)) continue; // Deleting a remote ref transmits no new tree.
      check.history(commit(local), /^0+$/.test(remote) ? null : commit(remote));
    }
  } else throw new Error('Usage: check-local-artifacts.cjs [--index | --range base..tip | --pre-push]');
  if (check.violations.size) {
    // JSON escaping makes filenames with tabs/newlines safe; never print blob contents.
    process.stderr.write('Local-only artifacts blocked (including intermediate outgoing commits):\n');
    for (const [p, reason] of [...check.violations].sort()) process.stderr.write('  ' + JSON.stringify(p) + ' (' + reason + ')\n');
    return 1;
  }
  process.stdout.write('Local-artifact guard passed.\n');
  return 0;
}

if (require.main === module) {
  try { process.exitCode = run(process.argv.slice(2), process.argv[2] === '--pre-push' ? fs.readFileSync(0, 'utf8') : ''); }
  catch (error) { process.stderr.write('Local-artifact guard failed closed: ' + error.message + '\n'); process.exitCode = 1; }
}
module.exports = { pathReason, contentReason };
