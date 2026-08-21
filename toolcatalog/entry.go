package toolcatalog

import (
	"encoding/json"
	"fmt"

	"github.com/impire-io/soulstream-core/identity"
)

// Kind classifies a CATALOG ENTRY — never a persona. The persona taxonomy
// the protocol removed stays removed: a workload tool's persona is a
// persona like any other, and what answers for it is the registry's
// operator claim, not this field.
type Kind string

const (
	// KindRemote is a tool nobody here runs, reached through the identity
	// plane's resource of the same name.
	KindRemote Kind = "remote"
	// KindWorkload is a tool this deployment runs.
	KindWorkload Kind = "workload"
)

// Entry is one tool as the catalog describes it. Evolution is additive:
// fields this version does not know survive a round trip untouched, so a
// newer writer's entries pass through an older reader's hands whole.
type Entry struct {
	// Name is the tool's catalog name — the KV key, the identity-plane
	// resource name for a remote tool — in the foundation's slug grammar.
	Name string
	// Kind says which half of the catalog this entry is.
	Kind Kind
	// Persona, workload kind only: the tool workload's persona, resolvable
	// in the persona registry like any participant.
	Persona string
	// Endpoint, workload kind only: where a door reaches it. Empty is a
	// declared-but-not-serving tool, which readers report honestly rather
	// than refuse.
	Endpoint string
	// Description is one plain-language line for screens and agents.
	Description string

	// extras carries every field this version does not know, verbatim.
	extras map[string]json.RawMessage
}

// The catalog's own keys — everything else in a stored document is a
// future writer's and rides in extras.
var knownKeys = map[string]bool{
	"name": true, "kind": true, "persona": true, "endpoint": true, "description": true,
}

// UnmarshalJSON decodes leniently and keeps what it does not know.
func (e *Entry) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	take := func(key string, into *string) error {
		v, ok := raw[key]
		if !ok {
			return nil
		}
		return json.Unmarshal(v, into)
	}
	if err := take("name", &e.Name); err != nil {
		return fmt.Errorf("toolcatalog: name: %w", err)
	}
	var kind string
	if err := take("kind", &kind); err != nil {
		return fmt.Errorf("toolcatalog: kind: %w", err)
	}
	e.Kind = Kind(kind)
	if err := take("persona", &e.Persona); err != nil {
		return fmt.Errorf("toolcatalog: persona: %w", err)
	}
	if err := take("endpoint", &e.Endpoint); err != nil {
		return fmt.Errorf("toolcatalog: endpoint: %w", err)
	}
	if err := take("description", &e.Description); err != nil {
		return fmt.Errorf("toolcatalog: description: %w", err)
	}
	for k, v := range raw {
		if knownKeys[k] {
			continue
		}
		if e.extras == nil {
			e.extras = map[string]json.RawMessage{}
		}
		e.extras[k] = v
	}
	return nil
}

// MarshalJSON writes the known fields (empty ones omitted) and every
// carried unknown beside them.
func (e Entry) MarshalJSON() ([]byte, error) {
	doc := map[string]json.RawMessage{}
	for k, v := range e.extras {
		doc[k] = v
	}
	set := func(key, val string) error {
		if val == "" {
			return nil
		}
		raw, err := json.Marshal(val)
		if err != nil {
			return err
		}
		doc[key] = raw
		return nil
	}
	if err := set("name", e.Name); err != nil {
		return nil, err
	}
	if err := set("kind", string(e.Kind)); err != nil {
		return nil, err
	}
	if err := set("persona", e.Persona); err != nil {
		return nil, err
	}
	if err := set("endpoint", e.Endpoint); err != nil {
		return nil, err
	}
	if err := set("description", e.Description); err != nil {
		return nil, err
	}
	return json.Marshal(doc)
}

// Validate refuses an unwritable entry by name. It runs at WRITE time only:
// readers stay lenient (an unfamiliar kind from a newer writer is
// present-but-unsupported, never an error), which is what keeps the
// vocabulary additive.
func (e Entry) Validate() error {
	if err := identity.CheckName(e.Name); err != nil {
		return fmt.Errorf("toolcatalog: entry name: %w", err)
	}
	switch e.Kind {
	case KindRemote:
		// The public half a ceremony needs lives on the identity plane; an
		// entry that starts describing it here is the split-brain the
		// design refuses.
		if e.Persona != "" || e.Endpoint != "" {
			return fmt.Errorf("toolcatalog: remote entry %s carries workload fields — a remote tool's reachable half lives on the identity plane, by name", e.Name)
		}
	case KindWorkload:
		if e.Persona == "" {
			return fmt.Errorf("toolcatalog: workload entry %s needs the persona it runs as", e.Name)
		}
		if err := identity.CheckName(e.Persona); err != nil {
			return fmt.Errorf("toolcatalog: workload entry %s persona: %w", e.Name, err)
		}
	default:
		return fmt.Errorf("toolcatalog: entry %s has unknown kind %q (this writer speaks %q and %q)", e.Name, e.Kind, KindRemote, KindWorkload)
	}
	return nil
}
