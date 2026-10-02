# Project agent instructions

Every starter project includes root `AGENTS.md` and `CLAUDE.md` for the local
assistant; `CLAUDE.md` imports `@AGENTS.md`. Learners do not download them
separately. Both files are part of the starter archive, covered by its
manifest and archive hash, tracked in the learner Git repository, and
preserved across lesson updates.

When both regular files are present after extraction, starter creation makes
no separate guidance request. The server repository keeps one common source
per practicum and copies it into its entry and independent transfer starter
sources. Published archive bytes and manifests are immutable; source changes
require new starter releases.

## Compatibility and restore

Old starter archives and restored revisions may lack root instructions.
Only in that case the CLI requests
`GET /v1/assignments/{assignment_id}/agent-guidance?version=N`. The response
identity and content digest are validated. Existing regular instruction files
are preserved; only missing files are added. Directories and symbolic links
at either destination are rejected before requesting the endpoint.

Restore first commits the accepted revision as its Git baseline, then adds
missing instructions in a separate technical commit. This preserves the
accepted solution hash and the current lesson pin. A 404 skips compatibility
installation; authentication and transport errors still fail staging.

Lesson updates never download or replace root instructions. Legacy immutable
update archives are verified fully, then root instruction operations are
excluded from application. The current lesson instructions and README define
the editable scope and any stricter limits on agent assistance.

## Submission boundary

Root instructions are excluded from new submissions, integrity checking,
review sources and mentor context. Same-named files in subdirectories remain
ordinary solution files. Path, file-type and resource checks still apply.
Historical revision hashes are unchanged. Accepted-base comparison supports
both the old full content hash and the instruction-free solution projection.
Server normalization and runner integrity support must precede deployment
of the CLI that filters these files.
