# indtools

A collection of small, independent command-line tools and utility scripts.

Each tool lives in its own top-level directory and is fully self-contained:
its own source, build files, dependencies, tests, and docs. There is no
shared module or cross-tool import — pulling any single subdirectory into
another repo should be enough to ship it on its own.

## Tools

| Directory | Language | What it does |
|-----------|----------|--------------|
| [`splitmerge/`](splitmerge) | Go | Split a file into fixed-size chunks and merge them back. Streams through the kernel where possible; tuned for throughput on machines with ~100 MiB free RAM, with `-buf` to dial down for smaller hosts. |

## Repository conventions

- **One directory per tool.** Pick a short, descriptive directory name and
  keep everything for that tool inside it. Do not add shared top-level
  source files.
- **Self-contained builds.** A Go tool ships its own `go.mod`; a Python
  tool ships its own `pyproject.toml` / `requirements.txt`; a shell tool
  ships a single executable script. No tool should depend on artifacts
  produced elsewhere in this repo.
- **Each tool documents itself.** Every tool directory ships a
  `README.md` (user-facing: install, usage, examples) and a `CLAUDE.md`
  (agent-facing: architecture notes, invariants, gotchas).
- **Tests live next to the code.** Run them from inside the tool's
  directory; CI (when present) should likewise scope to that directory.

## Adding a new tool

1. Create a new top-level directory named after the tool.
2. Initialize whatever build system the language wants, inside that directory.
3. Add the tool's own `README.md` and `CLAUDE.md`.
4. Add a row to the table above so the tool is discoverable from the root.

## License

[MIT](LICENSE).
