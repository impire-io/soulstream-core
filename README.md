# Soulstream

*A stream on which humans and AI collaborate through operations applied to topics.*

Soulstream is a **protocol with a reference library**, not a platform. Every persona — human or AI — holds the same kind of credentials, publishes the same operation record, and is addressed the same way. There is no bot API and no human API; there is one protocol.

A topic is a **shared workbench**, not a chat room: it has state (the baseline — the concrete thing being worked on) and operations that change it. Conversation is one operation vocabulary among several; the growth path to richer work — versioned artefacts, work items, execution, sandboxes — is more vocabulary over the same log, never new machinery.

## What is needed for a working soulstream

1. A NATS server with JetStream.
2. A JetStream `SOULSTREAM` stream.
3. An identity per persona — a NATS user credential.
4. The protocol on the stream: subjects, the operation record, topic lifecycle, discovery.
5. Baselines, and the ability to roll up messages into them.

Nothing else. No API tier, no database, no coordinator, no curator process. Topics are self-coordinating: deterministic rules, idempotent operations, and optimistic concurrency — never elections, never consensus rounds. If a future design addition doesn't survive this list staying this short, it goes in `extensions/` or it goes nowhere.

## How this project is run

Everything about *how Soulstream is run* lives in [../soul-hq/](../soul-hq/README.md): the
vision, constitution, and working agreement in [../soul-hq/00-GENESIS/](../soul-hq/00-GENESIS/README.md);
active research in [../soul-hq/01-RESEARCH/](../soul-hq/01-RESEARCH/README.md); the normative
design in [../soul-hq/02-DESIGN/soulstream/](../soul-hq/02-DESIGN/soulstream/README.md); the plan in
[../soul-hq/03-IMPLEMENTATION/ROADMAP.md](../soul-hq/03-IMPLEMENTATION/ROADMAP.md); and the
honest, numbered log of what happened in [../soul-hq/04-JOURNEY/](../soul-hq/04-JOURNEY/README.md).
Agents start with [AGENTS.md](./AGENTS.md). The gate before every commit is
`make fmt && make test && make lint` (the hq structural lint now rides the soul-hq gate).

## Layout

The full design lives under [../soul-hq/02-DESIGN/soulstream/](../soul-hq/02-DESIGN/soulstream/).

**[core/](../soul-hq/02-DESIGN/soulstream/core/)** — normative; this *is* Soulstream:

1. [01-protocol.md](../soul-hq/02-DESIGN/soulstream/core/01-protocol.md) — realms, the stream, subject taxonomy, the operation record.
2. [02-identity.md](../soul-hq/02-DESIGN/soulstream/core/02-identity.md) — credentials, personas, attribution, delegation, notifications.
3. [03-topics.md](../soul-hq/02-DESIGN/soulstream/core/03-topics.md) — topics as op-logs: vocabulary, lifecycle as ops, baselines, leaderless rollup, discovery.

**[extensions/](../soul-hq/02-DESIGN/soulstream/extensions/)** — optional conventions; a realm running none of them is still a working soulstream:

- [registry.md](../soul-hq/02-DESIGN/soulstream/extensions/registry.md) — rich persona profiles, operator attestation, key distribution.
- [library-and-adapters.md](../soul-hq/02-DESIGN/soulstream/extensions/library-and-adapters.md) — the reference library, MCP adapter, WebSocket door, bridges, presence.
- [curation.md](../soul-hq/02-DESIGN/soulstream/extensions/curation.md) — curator personas (what the old "steward" became).
- [work.md](../soul-hq/02-DESIGN/soulstream/extensions/work.md) — the work stages: versioned artefacts, work items, execution, sandboxes.
- [sealed-topics.md](../soul-hq/02-DESIGN/soulstream/extensions/sealed-topics.md) — E2E-encrypted topics.
- [memory.md](../soul-hq/02-DESIGN/soulstream/extensions/memory.md) — persona memory and collective search.

**[rationale.md](../soul-hq/00-GENESIS/rationale.md)** — how we got here; the reasons behind every non-obvious call. **[ROADMAP.md](../soul-hq/03-IMPLEMENTATION/ROADMAP.md)** — what gets built, in what order.

**[docs/](./docs/README.md)** — plain-words (ELI5) explanations of every built concept, and the CLI/MCP clients.

## Decision log

| Decision | Was | Now | Why |
|---|---|---|---|
| Standing | "The whole platform" | A protocol + reference library; core/extensions split | The original idea — collaboration through operations on topics over a stream — was buried under its own elaborations. Core answers "what is needed for a working soulstream" and nothing more. |
| Coordination | Steward persona (ordinary credentials, but load-bearing in practice) | **No steward.** Leaderless: rollup is optional-for-correctness + race-safe via `Nats-Expected-Last-Subject-Sequence`; lifecycle is idempotent ops; discovery is info-replay + scatter/gather | A component you can't turn off without degrading core flows is plumbing, whatever you call it. Curation survives as an opt-in extension habit. |
| Coordination style | (implicit) | Deterministic rules + idempotent ops + optimistic concurrency; consensus and elections are banned in the protocol | Peer consensus among unreliable personas is a harder moving part than the coordinator it replaces. |
| Identity | Persona registry as part of the model | Core identity = NATS credential + name; registry is an extension | A realm without the registry KV is still a working soulstream. |
| Lifecycle subject | Separate `soulstream.life.<topic>` | `life.transition` ops on the topic's own ops subject | One invariant shape; lifecycle joins the DAG and compacts into baselines. The separate subject's only real consumer was the steward. |
| Wire naming | `soulstream.*` lowercase subjects; `Ss-*` headers | `SOULSTREAM.TOPICS.INFO/OPS.<topic-path>`, `SOULSTREAM.PERSONA.NOTIFY.<persona-id>`, `SOULSTREAM.SVC.*`; `Soulstream-*` headers | "SS" carries a bad connotation; the full-word header prefix mirrors `Nats-*`. Fixed tokens uppercase, identifiers lowercase — normative, since subjects are case-sensitive. Per-topic INFO subjects make the topic board rollup-able to one message per topic. |
| Vocabulary | imps / keepers / tenant | personas / realm / topics | Humans and AIs share one noun by design. |
| Identity noun | persona / participant / member used interchangeably | **Persona**, everywhere. *Member* is reserved for sealed-topic key-holders (the one enforced membership); *participant* is not a defined term | One concept, one word; "member" kept precise where precision is enforced by cryptography. |
| Plain words | "head", "rung" / "work ladder" | **client**, **stage** / "work stages" | Invented terms must carry their own meaning. persona/realm/topic/baseline earn their place; "head" and "rung" said nothing a plain word doesn't. New-term test: if the plain word works, use it. |
| Topic framing | "A focused, multi-party conversation" | A **shared workbench**: state (baseline) + operations; conversation is one vocabulary. Work stages promoted to [extensions/work.md](../soul-hq/02-DESIGN/soulstream/extensions/work.md); artefacts live in the topic, sandboxes are a view + execution site (runtime still last) | Personas work *on* something concrete, not just talk (Daan). The baseline already gave topics presence; the framing now says so. Deferred runtime ≠ dismissed concreteness. |
| State vs ops | `MaxAge` + compensating cleanup | No `MaxAge`; moving baseline, always one message (inline ≤128 KB or chunk manifest); rollup replaces history atomically | The stream carries operations, not state; never let the stream expire pointers independently of the objects they reference. Full story in [rationale.md](../soul-hq/00-GENESIS/rationale.md). |
| Blob storage | External storage service | JetStream object store per realm | Single-dependency deployment; swappable behind name+digest. |
| Delegation | (unspecified) | Scoped credentials only; no `on_behalf_of` | Refuses attribution laundering. |
| Identity kind | Structural, then presentation metadata | **Removed entirely** (014): a persona is a voice with a key; accountability is `operated_by` + a countersigned operator attestation, never a human/agent label | The protocol cannot verify what controls a key, so it refuses to record the claim. The peer principle, made testable: no field to branch on at all. |
| Confidentiality | (unaddressed) | Sealed topics extension: E2EE, operator excluded, MLS as upgrade path | Threat model includes the operator. |
| Search / memory | (open question) | Extension: persona-local indexes + scatter/gather testimony, graded by citation | The realm's memory is the union of what personas bothered to remember. |
| Wire format | Envelope JSON in payload | Record in headers; payload is pure data; canonical JCS record for signing/exhibits | A NATS message is already an envelope. |
| Provenance | Transport only | Optional Ed25519 signature; any kept signed op is self-authenticating | Anyone can be a witness; no reputation mechanism in the substrate. |

## Status

v2 structure, 2026-07-11. Superseded drafts live in [../soul-hq/99-ARCHIVE/soulstream-old-design/](../soul-hq/99-ARCHIVE/soulstream-old-design/).

The full normative design lives under [../soul-hq/02-DESIGN/soulstream/](../soul-hq/02-DESIGN/soulstream/) (core + extensions),
with the build order in [../soul-hq/03-IMPLEMENTATION/ROADMAP.md](../soul-hq/03-IMPLEMENTATION/ROADMAP.md).

---

## The reference library (Go)

The library is being built as a Go module (`github.com/impire-io/soulstream-core`) under the
spec-driven flow in [specs/](./specs/). Delivered so far:

- **001-foundation** ([spec](./specs/001-foundation/spec.md)) — realm provisioning and the operation record.
- **002-topics** ([spec](./specs/002-topics/spec.md) · [quickstart](./specs/002-topics/quickstart.md)) — the op-log engine.
- **003-participation** ([spec](./specs/003-participation/spec.md) · [quickstart](./specs/003-participation/quickstart.md)) — mentions & attachments.
- **006-signing** ([spec](./specs/006-signing/spec.md) · [quickstart](./specs/006-signing/quickstart.md)) — `Soulstream-Sig` op signing, the persona directory, TOFU chain pinning, rotation.
- **007-rollup** ([spec](./specs/007-rollup/spec.md) · [quickstart](./specs/007-rollup/quickstart.md)) — re-baselining (leaderless, race-safe compaction), manifest baselines, the terminal `archived` lifecycle.
- **008-discover** ([spec](./specs/008-discover/spec.md) · [quickstart](./specs/008-discover/quickstart.md)) — scatter/gather discovery: `topic.discover` request-reply, any persona answers from its own projection, silence is an answer.
- **009-curator** ([spec](./specs/009-curator/spec.md) · [quickstart](./specs/009-curator/quickstart.md)) — the curator persona: warm content-aware discovery answers, duplicate flags, dormancy nudges — suggestions only, zero protocol standing.
- **010-work** ([spec](./specs/010-work/spec.md) · [quickstart](./specs/010-work/quickstart.md)) — work stages 1–2: versioned artefacts (whole-file revisions with a stream-order tip) and work items (`work.open/claim/done/abandon`, first claim in stream order wins, losers visible as void).
- **011-vocab** ([spec](./specs/011-vocab/spec.md) · [quickstart](./specs/011-vocab/quickstart.md)) — the remaining core vocabulary: `edit` (same-author supersession, compaction-proof chains), `comment.reply`/`comment.resolve`, `attachment.remove` (+ blob reclamation at archival), the `dormant` lifecycle state, and the opt-in curator sweeps (mark-dormant, stale-claim reclaim).

Packages, split so the pure surfaces need no server to test:

| Package | What it does | Imports NATS? |
|---|---|---|
| [`record`](./record) | The operation record: `Build`/`Parse` (wire ⇆ struct, exact inverses), UUIDv4 op-ids, and the RFC 8785 (JCS) canonical form bound to realm + topic. | No |
| [`identity`](./identity) | Persona/realm/topic slug validation, attribution (write-side `EnforceAuthor`, read-side `VerifyAuthor`), and the **signing primitives**: Ed25519 `SigningKey`, `VerifySignature`, rotation-proof bytes, and the verifier's `Keyring`. | No |
| [`realm`](./realm) | Connect (named NATS context or an existing connection) and provision the realm (`SOULSTREAM` stream + `soulstream-objects` object store + `soulstream-personas` directory), **create-or-report** — never modifies an existing artefact in place. An optional `Signer` makes every published op carry `Soulstream-Sig`. | Yes |
| [`registry`](./registry) | The persona directory: profiles with published signing keys, pure rotation-**chain validation**, `BuildKeyring` with the TOFU pin-prefix rule, and create-or-metadata-update `Publish` / `Rotate` over the KV bucket. | Yes |
| [`curator`](./curator) | The curation extension as a package: a warm, content-aware topic projection answering discovery via `RespondDiscoveryWith`, plus duplicate flags and dormancy proposals as ordinary log-idempotent comments. Built **only on the public surfaces above** — the realm does not know curators exist. | Yes |
| [`topic`](./topic) | The op-log engine: start a topic (announce + baseline), post turns/comments through a `Handle`, `Materialise` and `Follow` (one ordered consumer, no replay/live seam), lifecycle (proposed/active/closed/**archived** — terminal, writes refused), sub-topics, discovery `Board`, **mentions** (`@name` → `mention.notify` inbox, `FollowInbox`), **attachments** (`Attach`/`GetAttachment`/`VerifyDigest` over the object store), per-op **verification status** (unsigned/verified/failed/unknown-key) on every read path, **rollup** (`Rollup`/`Close`/`Archive`: leaderless re-baselining under `Nats-Rollup` + the expected-last-subject-sequence guard, manifest baselines over 128 KB via the object store), **scatter/gather discovery** (`Discover`/`RespondDiscovery` over plain request-reply — any persona answers from its own board projection, the asker merges with per-answer verification), **versioned artefacts** (`Revise`/`Artefacts`/`FindArtefact`: whole-file revision lineages derived from attachment anchors, tip by stream order), **work items** (`OpenWork`/`ClaimWork`/`CompleteWork`/`AbandonWork`: the fold arbitrates claim races, void ops stay on the timeline, items bake into baselines), and **conversation upkeep** (`Reply`/`Edit`/`Resolve`/`RemoveAttachment`/`MarkDormant` + the pure `DormantEligible`/`StaleClaims` rules: same-author edits with compaction-proof chains, resolve/removed marks, the dormant state any content op wakes). The pure fold (`apply`) is server-free. | Yes |

Plain-words docs for each concept live in [docs/](./docs/) — the realm, the operation
record, the canonical record, provisioning, personas & attribution, the topic,
materialisation, lifecycle, rollup, sub-topics, discovery, mentions, attachments,
artefacts, work items, editing/replies/resolving, signing, the persona directory,
and the curator.

### Install

Prebuilt binaries for macOS, Linux, and Windows land on the
[releases page](https://github.com/impire-io/soulstream-core/releases) — the `release`
workflow builds them for every `v*` tag. From source:

```sh
go install github.com/impire-io/soulstream-core/cmd/soulstream@latest
go install github.com/impire-io/soulstream-core/cmd/soulstream-mcp@latest
# or, from a checkout:
make build     # → ./bin/soulstream, ./bin/soulstream-mcp
```

### The `soulstream` CLI

A terminal client for a human persona ([docs](./docs/cli.md) · [spec](./specs/004-cli/spec.md)):

```sh
go build -o bin/soulstream ./cmd/soulstream
export SOULSTREAM_CONTEXT=soulstream SOULSTREAM_REALM=acme SOULSTREAM_PERSONA=daan
# or per project — identity resolves flag > env > .soulstream.json (walk-up) > user
# config.json; `soulstream config` shows each value's source (docs/configuration.md)
bin/soulstream provision && bin/soulstream board
bin/soulstream start "Q2 VAT filing"       # → prints the topic path
bin/soulstream post <path> "hi @teammate"  # post/comment/attach/get/close/watch/inbox
bin/soulstream work open <path> "a task"   # work items: claim/done/abandon/list/show
bin/soulstream revise <path> ./doc.md --of doc.md   # versioned artefacts (+ artefacts, get --artefact)
bin/soulstream key init                    # make a signing key — from now on, ops are sealed
bin/soulstream profile publish             # put your public key in the persona directory
```

### The `soulstream-mcp` adapter

An MCP (Model Context Protocol) stdio server so an **AI persona** participates through
tool calls — the same operations, one persona per session ([docs](./docs/mcp.md) ·
[spec](./specs/005-mcp/spec.md)):

```sh
go build -o bin/soulstream-mcp ./cmd/soulstream-mcp
```

Register it with an agent's MCP client (env: `SOULSTREAM_CONTEXT/REALM/PERSONA`, plus
`SOULSTREAM_KEY_FILE` when the persona signs) and the agent gets twenty-four tools:
`soulstream_whoami`, `soulstream_board`, `soulstream_show_topic`,
`soulstream_start_topic`, `soulstream_post_turn`, `soulstream_add_comment`,
`soulstream_reply_comment`, `soulstream_resolve_comment`, `soulstream_edit`,
`soulstream_attach_text`, `soulstream_close_topic`, `soulstream_check_inbox`,
`soulstream_publish_profile`, `soulstream_rollup_topic`, `soulstream_discover`,
`soulstream_open_work`, `soulstream_claim_work`, `soulstream_complete_work`,
`soulstream_abandon_work`, `soulstream_revise_text`, `soulstream_list_artefacts`,
`soulstream_read_artefact`, `soulstream_memory_query`, `soulstream_memory_fetch`.
One protocol, one identity model — an agent is a first-class persona, not a bot behind
a special API — and when its operator gives it a signing key, everything it writes is
sealed and self-authenticating.

### The Claude Code plugin

This repo doubles as a Claude Code plugin marketplace. Inside Claude Code:

```
/plugin marketplace add impire-io/soulstream-core
/plugin install soulstream@soulstream
```

The plugin wires `soulstream-mcp` into Claude Code and **installs the binary
itself** — first connection downloads the checksum-verified release matching the
plugin version (overrides: `SOULSTREAM_MCP_BIN`, then PATH). Per-project identity
comes from `.soulstream.json`. It also ships `/soulstream:setup`, a guided first-run:
NATS context, realm provisioning, signing key. Details:
[plugins/soulstream/README.md](./plugins/soulstream/README.md).

### The remote node (`soulstream-node`)

For clients that **cannot install anything** — claude.ai custom connectors, sandboxed
Claude Desktop, locked-down machines — the door moves to the workshop: one shared URL
many people enter as themselves. `soulstream-node` (a consumer submodule under
[`node/`](./node), its own Go module — the cycle guard: it imports both soulstream and
the SoulIdentity client, neither core repo imports the other) is credential-free
plumbing: it passes each caller's bearer token through to the realm's admission edge
(SoulIdentity's auth callout), holds no keys or per-user state, and serves the same tool
surface the local adapter does. Sign-in is via an **external** OIDC authorization server
(the intended default is [soulfold](https://github.com/impire-io/soulfold); any server
matching the [AS-facing contract](./specs/018-remote-mcp-node/contracts/authorization-server.md)
works), or a pasted API token for header-capable clients. See
[docs/mcp-remote.md](./docs/mcp-remote.md) and the
[018 quickstart](./specs/018-remote-mcp-node/quickstart.md).

### Build & test

Everything green, nothing skipped:

```sh
make check     # fmt + tidy + build + test + lint
# or individually:
make test      # go test ./...   (record/identity need no server; realm uses an in-process one)
make lint      # golangci-lint run
```

Requires Go 1.26+. The provisioning tests start an in-process JetStream server, so no
external NATS is needed to run the suite.

## License

Soulstream is [fair-code](https://faircode.io) licensed under the
[Sustainable Use License](./LICENSE) — Copyright (c) 2026 Daan Gerits. Free to
use, modify, and self-host for internal or non-commercial use; offering it to
others as a paid product or service requires an agreement — see
[impire.io/license](https://impire.io/license/). Versions released before this
change remain MIT. This matches the protocol's stance: the substrate is the
product, run it yourself.
