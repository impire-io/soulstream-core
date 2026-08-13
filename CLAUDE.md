<!-- SPECKIT START -->
Last landed feature (no cycle active): **018-remote-mcp-node** (v0.7.0,
2026-08-02) — the remote MCP node: a URL into the realm for no-install
clients. Two deliverables shipped. (1) `internal/mcpserver` PROMOTED to
public `mcpserver` (embeddable by any host bringing a connected,
signer-wired `realm.Client`) + `WithKeyring` option (decouples reader
verification from the per-realm pins file for multi-tenant hosts) + new
`soulstream_whoami` tool (24 total) — SoulNode's fourth upstream ask.
`cmd/soulstream-mcp` changed one import; stdio behavior byte-identical.
(2) Nested consumer module `node/` (own go.mod, committed `replace
soulstream => ../`, soulidentity by pseudo-version; cycle guard: neither
core repo imports the other or the node — tested): streamable HTTP,
credential-free bearer passthrough onto per-principal pooled
callout-admitted NATS connections, server-asserted principal via
`$SYS.REQ.USER.INFO`, delegated signing via `client.PersonaSigner` (017
seam), RFC 9728 + 401 OAuth resource edge. EXTERNAL OIDC AS ONLY (soulfold
the intended default; node AS-agnostic; the AS-facing contract IS the
interface, proven by an SC-005 stand-in built from the doc alone).
R4 TRUST MODEL (the one change over the 56c7a2e prototype): a bearer
influences a pool entry only AFTER admission for that principal (build /
byBearer / bound-session refresh / candidate probe) — closes the
prototype's forged-hint DoS. All 5 stories measured on an in-process rig
(soulidentity `embed.Run` + operator-mode callout + OIDC AS stand-in).
GOTCHA banked: node-authored content verifies from soulidentity
`keys.public`, NOT the soulstream profile registry (node publishes no
profile). CI/release AWAKE since 2026-08-03: the whole impire-io stack
went public, so the `NODE_CI_ENABLED` gate + credential step were
removed rather than provisioned; node pins soulidentity v0.1.0; the
node-release job proves itself on the next `v*` tag; core release
unaffected. `byon-setup`+`probe` carried best-effort
(spec Q2). For details: [specs/018-remote-mcp-node/plan.md](specs/018-remote-mcp-node/plan.md)
(spec+Clarifications, research R1–R11, contracts/{library,authorization-server,http}.md,
data-model, quickstart, episode `../soul-hq/04-JOURNEY/0047-soulstream-remote-mcp-node-built.md`).

Prior landed feature: **017-signer-seam** (v0.6.0,
2026-07-29 — includes same-day DX hardening, journeys 0006+0007) — the
Signer seam: `identity.Signer { PublicKey() string; Sign(canonical []byte)
(string, error) }` so signing can be delegated to an external custodian
(SoulIdentity's `sign.record` over NATS — its M2 "consumers wire in")
without soulstream depending on it. `(*SigningKey).Sign` is fallible now
(error always nil locally); `realm.Config.Signer`/`Client.Signer()` take
the interface — a TYPED-NIL signer is REFUSED by Connect/NewClient with a
teaching error, pre-server-contact (it SIGSEGV'd six of our own helpers
before the guard). Chokepoint `topic/wire.go:buildOpMsg`: signer error or
EMPTY signature = publish fails, no unsigned fallback (empty would travel
as "unsigned"); responders go silent with the reason in their callbacks —
`onServed(query, sent int, err error)` / `MemoryWitness.OnServed(kind, n,
err)`, the `-1` sentinel is GONE (no-match = (0,nil)). CYCLE GUARD:
neither soulstream nor soulidentity imports the other — the interface is
satisfied structurally, adapters live in consumer binaries (rule in
identity/signer.go doc + 017 contract + SoulIdentity M2).
`registry.NewAttestationToken` + `Rotate` take the interface (capability,
not custody); keystore/keygen stay concrete `*SigningKey` (FR-008 via type
system). Delegation transparent [measured]: byte-identical sigs, verified
on every read surface. No new deps; no config surface for delegation
(arrives with the remote node, 018-ish). NOTE: soulstream-archivist needs a
2-line OnServed change on its next dependency bump.

For details read: [specs/017-signer-seam/plan.md](specs/017-signer-seam/plan.md)
(spec: `specs/017-signer-seam/spec.md` incl. Clarifications 2026-07-29,
decisions: `research.md` R1–R7, contract: `contracts/library.md`, model:
`data-model.md`, consumer view: `quickstart.md`, episode:
`../soul-hq/04-JOURNEY/0026-soulstream-the-signer-seam.md`).
Done: `001`–`005` (MVP), `006-signing`, `007-rollup`, `008-discover`, `009-curator`,
`010-work`, `011-vocab`, `012-distribution` (v0.1.0), `013-config` (v0.2.0),
`014-persona-accountability` (v0.3.0/v0.3.1), `015-memory` (v0.4.0, archivist
live on NGS + dogfood running since 2026-07-27), `016-provision-limits`
(v0.5.0), `017-signer-seam` (v0.6.0, merged + released 2026-07-29 —
SoulIdentity M2 wiring point). Research: sealed-topics graduated to design
2026-07-28
(journey 0005 — speckit-ready, build priority gated on the dogfood chafe
log to 2026-08-10).

Project conventions:
- Go 1.26; module `github.com/impire-io/soulstream-core`.
- `record` and `identity` import NO NATS; `realm`, `topic`, `registry`, `curator` are NATS-touching.
- Keep pure logic (folds, chain validation, discovery match/merge, curator judgment) separate
  from NATS I/O so it unit-tests with no server.
- Connect via `github.com/synadia-io/orbit.go/natscontext`; modern `nats.go/jetstream` API.
- Quality gate before every commit: `make fmt && make test && make lint` — all green, none skipped.
- Push to origin after merging a completed feature to main.
<!-- SPECKIT END -->

## How this project is run (read this first)

The SPECKIT block above tracks the active feature; the durable way of working
lives in `../soul-hq/`. Before touching anything:

- **`../soul-hq/00-GENESIS/` first** — [`vision.md`](../soul-hq/00-GENESIS/vision.md),
  [`constitution.md`](../soul-hq/00-GENESIS/constitution.md) (articles + the anti-drift
  working agreement, wired into spec-kit via the
  `.specify/memory/constitution.md` symlink), and
  [`how-we-work.md`](../soul-hq/00-GENESIS/how-we-work.md). Decisions are held against
  these.
- **[`AGENTS.md`](AGENTS.md)** — the numbered reading order and the
  non-negotiables in brief.
- **The journey duty (required):** every landed feature, concluded research
  investigation, or load-bearing decision gets a numbered episode in
  `../soul-hq/04-JOURNEY/` in the same change — `/journey-log` does this (research topics
  get theirs via `/research-graduate`). The structure is enforced by soul-hq's internal/hqlint under its own make test.
