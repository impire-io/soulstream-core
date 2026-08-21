// Package toolcatalog is the tool catalog's discovery face — the optional
// convention answering "which tools does this soulstream have?" in one
// realm-readable place, uniform across the tools a deployment runs and the
// remote systems nobody here runs.
//
// It is display/discovery-grade, never authority: the same demotion the
// canonical form gave names. Authority lives where it always did —
// admission at the transport, outbound custody behind the identity plane,
// consent on the record's grant vocabulary. A remote entry deliberately
// carries no endpoints, client ids, or secrets: the half a ceremony needs
// lives with the custody half on the identity plane, keyed by the same
// name, so the record never holds a secret and never partially describes
// one.
//
// The store is a realm-readable KV bucket created on first write — this is
// an extension; nothing in provisioning mandates it. Entries evolve
// additively: unknown fields survive a round trip, and a reader treats an
// unfamiliar kind as present-but-unsupported rather than an error, the
// record's own rule for unknown vocabulary.
//
// Design: ../soul-hq/02-DESIGN/soulstream-core/extensions/tool-catalog.md;
// the custody half and the forwarding door are
// ../soul-hq/02-DESIGN/soulstream-identity/external-tools.md (D39–D41).
package toolcatalog
