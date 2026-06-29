# CLAUDE.md — repo guidance

This repo hosts unrelated CLI tools and utility scripts. Read this before
making changes.

## Layout invariant

Every tool gets its own top-level directory and is **independently
buildable and shippable**. No shared root module, no cross-tool imports,
no shared library directory. If you find yourself wanting to share code
between two tools, copy the snippet — coupling them defeats the point of
this repo.

When adding a new tool:

- Create a new top-level directory; name it after the tool.
- Put the build manifest *inside* that directory (`go.mod`, `pyproject.toml`,
  `Cargo.toml`, etc.). Do not create one at the repo root.
- Add a per-tool `README.md` (user-facing) and `CLAUDE.md` (agent-facing).
- Update the tool index table in the root `README.md`.

When modifying an existing tool, stay inside its directory. Touching files
in a sibling tool is almost always a mistake.

## Per-tool `CLAUDE.md`

Each tool's `CLAUDE.md` is the authoritative source for that tool's
invariants. Read it before editing. Examples of what belongs there:

- File-format contracts that other parts of the tool rely on (e.g. chunk
  filename patterns that the merge path parses).
- Performance assumptions and their defaults (buffer sizes, concurrency).
- Things that look like cleanup opportunities but are load-bearing.

## Commits and branches

- Develop on the feature branch the task specifies; do not push elsewhere
  without explicit instruction.
- Keep commits scoped to a single tool when possible. A commit that
  touches two tools should be rare and well justified in the message.
- Commit messages: imperative mood, scope prefix matches the tool
  directory (e.g. `splitmerge: …`).

## What not to do

- Do not promote any tool's code into a shared root package.
- Do not add a root-level `go.work`, `Makefile`, or build script that
  reaches into multiple tools — each tool's directory must remain
  buildable in isolation.
- Do not create documentation files (`*.md`) beyond the per-tool
  `README.md` and `CLAUDE.md` unless the user explicitly asks for them.
