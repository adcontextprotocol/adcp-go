# skills/

SKILL.md files consumed verbatim by LLM coding agents. Two kinds live side
by side:

| Kind | Naming | Source of truth | Edit path |
|------|--------|-----------------|-----------|
| **SDK-local** — how to build a Go-flavored AdCP agent against this SDK. | `build-*` | This repo. | Edit in place; PRs reviewed via CODEOWNERS (`/skills/`). |
| **Protocol-managed** — canonical wire contracts published by `adcontextprotocol/adcp`. | `adcp-*`, `call-adcp-agent` | The protocol tarball at `/protocol/<version>.tgz`. | Do not hand-edit — `adcp/schemas/download.sh` overwrites these on sync. Upstream changes go through `adcontextprotocol/adcp`. |

Protocol-managed skills are enumerated under `manifest.contents.skills` in
the bundle. The sync filters out per-skill `schemas/` subdirs (verified
identical to the top-level schemas already in `adcp/schemas/`).

The pinned bundle version is `adcp/schemas/VERSION`. Skills land on the
next bump to a release that bundles them.
