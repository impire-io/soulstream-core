# Feature Specification: External subcommands — the CLI grows verbs from PATH

**Feature Branch**: `019-external-subcommands` (landed directly on main, the
repo's way for single-seam features)
**Created**: 2026-08-15
**Status**: Shipped
**Input**: soul-hq design
[`0004-wrap.md`](../../../soul-hq/02-DESIGN/soulstream-workloads/0004-wrap.md)
§8 — `soulstream wrap` must reach the `soulstream-wrap` binary the workload
plane ships, without the core CLI importing it (the dependency points the
other way).

The core CLI adopts the git/kubectl convention: a verb it does not know is
looked up as `soulstream-<verb>` on PATH and executed with the CLI's
**resolved identity** projected into the child's environment. Resolution
happens once, by the precedence rules people already configure (flag > env >
nearest file > config dir); the child reads the same `SOULSTREAM_*` names
every other door already reads. Any `soulstream-*` binary is a verb; the
seam's named first occupant is `soulstream-wrap` (constitution S2: the seam
exists because its concrete occupant does).

## User Scenarios & Testing

### User Story 1 — A verb the CLI doesn't know runs the tool that provides it (P1)

A person with `soulstream-wrap` installed types `soulstream --persona clerk
wrap --harness claude`. The CLI resolves identity exactly as for any
built-in verb, finds `soulstream-wrap` on PATH, and runs it with the
remaining arguments; the child sees `SOULSTREAM_PERSONA=clerk` (and the
rest of the resolved identity) in its environment and the parent's exit
code is the child's.

**Acceptance Scenarios**:

1. **Given** an executable `soulstream-<verb>` on PATH, **When**
   `soulstream [identity flags] <verb> [args...]` runs, **Then** the binary
   runs with those args, inherits stdin/stdout/stderr, sees the resolved
   identity as `SOULSTREAM_CONTEXT/REALM/PERSONA/KEY_FILE/PINS_FILE`
   (non-empty values only; everything else passes through), and its exit
   code is returned.
2. **Given** no such binary, **When** an unknown verb runs, **Then** the
   CLI fails with its own usage error exactly as before — the fallback
   never turns a typo into a silent search.

## Requirements

- **FR-001**: Dispatch MUST happen only after built-in verbs; a built-in
  name always wins (no shadowing of the CLI's own surface).
- **FR-002**: The child MUST receive the resolved identity as environment,
  non-empty fields only, layered over the parent environment (never
  unsetting what the parent carried).
- **FR-003**: The child's stdin/stdout/stderr are the CLI's own; the exit
  code is propagated; context cancellation terminates the child.
- **FR-004**: Lookup is exactly `soulstream-<verb>` via PATH; no other
  search, no registry, no configuration.

## Success Criteria

- **SC-001**: A stub external verb receives args, env, and returns its
  exit code through the CLI (hermetic test with a temp PATH).
- **SC-002**: Unknown verbs without a binary keep the existing usage
  failure (regression-pinned).
