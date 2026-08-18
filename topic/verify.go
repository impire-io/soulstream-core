package topic

import (
	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/record"
)

// SigStatus is the per-op outcome of signature verification. It is annotation only:
// no status ever drops, hides, or reorders an op — losing testimony is worse than
// reading it with a warning.
type SigStatus string

const (
	// SigUnsigned means the op carries no signature. Valid forever (testimony-grade).
	SigUnsigned SigStatus = "unsigned"
	// SigVerified means the signature verifies against a key in the author's
	// validated chain (exhibit-grade).
	SigVerified SigStatus = "verified"
	// SigFailed means a signature is present but wrong — malformed, matching no key
	// of the author's, or the author is distrusted after a suspected substitution.
	SigFailed SigStatus = "failed"
	// SigUnknownKey means a signature is present but the author has no known key (no
	// keyring, no profile, or a profile without one). Not a failure — the op may
	// verify retroactively once the key is learned.
	SigUnknownKey SigStatus = "unknown-key"
	// SigLegacy names the pre-v2 record shape (episode 0112): signed
	// before the acting field and the key-bound canonical existed, so
	// the signature cannot verify under v2 — re-found the realm, or
	// keep the history as legacy-shape testimony.
	SigLegacy SigStatus = "legacy-shape"
)

// VerifyRecord computes one record's verification status against the author's
// validated key chain. The binding is the canonical topic value derived from the
// subject the op was consumed on (the topic path for ops/info, the persona for
// notify). A nil keyring degrades signed ops to unknown-key — verification never
// blocks reading.
func VerifyRecord(rec record.Record, realmKey, binding string, kr *identity.Keyring) SigStatus {
	if rec.Signature == "" {
		return SigUnsigned
	}
	// A signed record without the acting field predates v2: its
	// signature covered the old canonical and can never verify here.
	// Named, never conflated with failure.
	if rec.Acting == "" {
		return SigLegacy
	}
	// No realm identity to bind: verification cannot proceed — the same
	// retroactively-resolvable class as a missing key.
	if realmKey == "" {
		return SigUnknownKey
	}
	chain, distrusted := kr.ChainFor(rec.Author)
	if distrusted {
		return SigFailed
	}
	if len(chain) == 0 {
		return SigUnknownKey
	}

	unsigned := rec
	unsigned.Signature = ""
	canonical, err := unsigned.Canonical(realmKey, binding)
	if err != nil {
		return SigFailed
	}
	for _, key := range chain {
		if identity.VerifySignature(key, canonical, rec.Signature) {
			return SigVerified
		}
	}
	return SigFailed
}

// annotate computes the status of every record in a replay, keyed by op-id.
func annotate(recs []SeqRecord, realmKey, binding string, kr *identity.Keyring) map[string]SigStatus {
	statuses := make(map[string]SigStatus, len(recs))
	for _, sr := range recs {
		statuses[sr.Record.ID] = VerifyRecord(sr.Record, realmKey, binding, kr)
	}
	return statuses
}

// annotateView attaches per-op statuses to a materialised view's elements. Elements
// with no status of their own were baked into the baseline by a rollup — their
// individual signatures were destroyed with the compacted tail, so they inherit the
// baseline op's status: the roll-upper's attestation is the state's provenance.
func annotateView(mt *MaterializedTopic, statuses map[string]SigStatus, baselineID string) {
	baked := statuses[baselineID]
	for i := range mt.Contributions {
		if s, ok := statuses[mt.Contributions[i].OpID]; ok {
			mt.Contributions[i].Sig = s
		} else {
			mt.Contributions[i].Sig = baked
		}
		for j := range mt.Contributions[i].Edits {
			e := &mt.Contributions[i].Edits[j]
			if s, ok := statuses[e.OpID]; ok {
				e.Sig = s
			} else {
				e.Sig = baked
			}
		}
	}
	for i := range mt.Attachments {
		if s, ok := statuses[mt.Attachments[i].OpID]; ok {
			mt.Attachments[i].Sig = s
		} else {
			mt.Attachments[i].Sig = baked
		}
	}
	for i := range mt.WorkItems {
		w := &mt.WorkItems[i]
		if s, ok := statuses[w.ID]; ok {
			w.Sig = s
		} else {
			w.Sig = baked
		}
		for j := range w.Timeline {
			if s, ok := statuses[w.Timeline[j].OpID]; ok {
				w.Timeline[j].Sig = s
			} else {
				w.Timeline[j].Sig = baked
			}
		}
	}
}
