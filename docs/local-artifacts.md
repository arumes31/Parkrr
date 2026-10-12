# Local-only previews and review evidence

Screenshots, mockups, generated demo outputs, recordings, and interactive review
reports are local working material. They must not be committed or pushed.

## What stays local

- `docs/anwendungsreview/`, including its report sources, answers and evidence.
- `docs/screenshots/`, dated HTML/CSS/JS workbenches in `docs/`, and generated
  `.impeccable` previews, critiques and reviews.
- Capture, mockup, test-result and other generated output directories; raster
  images outside the product asset directories; recordings and archives.
- Raster images embedded in HTML, CSS or other text as data URLs.

Product artwork in `web/static/icons/` and `web/static/img/` remains versioned.
Synthetic test fixture **source code** and capture/test scripts remain versioned:
they test the application without distributing generated customer/demo records.
Do not put screenshots in product asset directories to bypass this policy.

The local review is still available at `docs/anwendungsreview/index.html`. It is
not included in a fresh clone. These local-only files need a separate private
backup if they must be preserved; Git is no longer their backup.

## Protection layers

1. `.gitignore` keeps local outputs out of normal staging.
2. `pre-commit` checks the complete staged index, including force-added files.
3. `pre-push` checks every outgoing commit, including intermediate commits that
   added an artifact and later deleted it. Unknown or incomplete history blocks
   the push rather than transmitting unchecked content.
4. CI repeats the index check as a secondary regression check. CI runs after
   upload and therefore does **not** replace the local pre-push hook.
5. `.dockerignore` excludes review material from build contexts. The Docker
   publishing workflow also disables build-record artifact uploads.

The guard prints only file paths and classifications, never captured contents.
It is a targeted artifact check, not a general secret/customer-data scanner.
Avoid real customer records in tests and do not bypass hooks with `--no-verify`.
Hooks are local and can be deliberately bypassed; these protections are not a
server-enforced GitHub restriction.

## Checks and installation

Requires Node.js and Git. Run from the repository root:

```sh
node scripts/check-local-artifacts.cjs --index
node scripts/check-local-artifacts.cjs --range origin/dev..HEAD
node --test tests/frontend/local-artifacts.test.js
```

The versioned hook templates are `.githooks/pre-commit` and `.githooks/pre-push`.
Install them in the hook directory reported by `git rev-parse --git-path hooks`.
If either destination already exists, integrate the checks manually instead of
overwriting it. Preserve unrelated hooks, especially Graphify's `post-commit`
and `post-checkout`; do not change `core.hooksPath` merely to install these two
checks. On POSIX systems, make the installed hooks executable. A clone does not
automatically install hooks; repeat installation for each checkout.

## Existing commits are a separate concern

Removing files from Git's index preserves their local copies but does not erase
them from old commits. An ordinary deletion commit is **not sufficient** to make
an outgoing history clean. Consequently, an artifact-containing outgoing history
is intentionally blocked until that history is cleaned with approval.

Already-published files and history are not removed from GitHub by local cleanup.
Published-history rewriting, force pushes and deletion of remote artifacts need
separate approval and coordination. Never bypass the guard to push a cleanup
commit that would also publish earlier local captures.
