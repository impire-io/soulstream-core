package topic

import (
	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/realm"
)

// Handle binds a client to one topic-path: the thing a persona posts through and
// follows. It tracks the leaf op-ids (the frontier) it has observed, so posts parent
// onto what the handle has actually seen.
type Handle struct {
	client    *realm.Client
	path      string
	frontier  []string
	lifecycle Lifecycle // last observed (from Materialise/Follow); "" until observed
	keyring   *identity.Keyring
}

// Open returns a handle to an existing topic by path. It publishes nothing.
func Open(c *realm.Client, path string) *Handle {
	return &Handle{client: c, path: path}
}

// Path returns the handle's topic-path.
func (h *Handle) Path() string { return h.path }

// UseKeyring supplies the keyring Materialise and Follow verify signatures against.
// Without one (or with nil), signed ops report unknown-key and reading is unaffected —
// verification is an annotation, never a gate.
func (h *Handle) UseKeyring(kr *identity.Keyring) { h.keyring = kr }

// Frontier returns the leaf op-ids the handle has observed. It is nil until the handle
// materialises the topic or posts to it.
func (h *Handle) Frontier() []string { return h.frontier }

// Lifecycle returns the topic's lifecycle as the handle last observed it ("" until it
// has materialised or followed the topic).
func (h *Handle) Lifecycle() Lifecycle { return h.lifecycle }

// adopt updates the handle's observed frontier and lifecycle from a materialised view.
func (h *Handle) adopt(mt *MaterializedTopic) {
	h.frontier = mt.Frontier
	h.lifecycle = mt.Lifecycle
}
