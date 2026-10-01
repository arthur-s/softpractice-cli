# Technical agent instructions

Root `AGENTS.md` and `CLAUDE.md` guide the local assistant. They are tracked in
Git, but they are excluded from the submitted file list and tar.gz. Files with
those names in subdirectories remain ordinary solution files. Archive safety
and resource limits still apply before exclusion.

The server publishes a separate bundle through
`GET /v1/assignments/{assignment_id}/agent-guidance?version=N`. The assignment
version authorizes access; the files have their own SHA-256 identity. The
bundle contains either no files or exactly `AGENTS.md`, then `CLAUDE.md`.
Its digest is SHA-256 of the compact Go JSON `files` array, including Go JSON
escaping. Contents and paths are validated before writing.

`starter` installs one common practicum instruction in the project root after
verifying the archive. A separate transfer starter gets the same instruction.
Project restoration reinstalls it after preserving the accepted Git baseline.
Lesson transitions leave these root instructions unchanged, including when
legacy immutable archives contain old lesson-specific AGENTS.md. Current lesson
goals, editable paths and commands are read from the assignment and README.
There is no lesson-specific guidance registry or agent-guidance update kind.

Older accepted revisions keep their full content hash. For an update from
such a revision, the CLI finds the accepted full tree in its bounded local Git
history and compares its solution projection with the current tree. Guidance
changes alone are allowed; changed lesson files still refuse the update.
If the accepted tree is unavailable locally, restore the project instead.

Deploy the server exclusion support and rebuild/deploy the runner images
before releasing this CLI. Integrity runs inside the runner; an old image
can still require `AGENTS.md` and reject a filtered Go submission. A 404 from the new endpoint only skips guidance installation;
authentication, permission, digest and transport failures remain errors.
No immutable starter/update archives or stored revisions are rewritten.

After downloading a transition, the CLI rechecks both the clean worktree and
the HEAD captured when preparing the update. A concurrent commit requires
preparing the update again, even when the worktree remains clean.
