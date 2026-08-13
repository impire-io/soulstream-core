package realm

import (
	"errors"
	"testing"

	"github.com/impire-io/soulstream-core/identity"
)

func TestClientEnforceAuthor(t *testing.T) {
	// A persona-bound client may only author as itself.
	bound := &Client{cfg: Config{Realm: "acme", Persona: "daan"}}
	if err := bound.EnforceAuthor("daan"); err != nil {
		t.Errorf("EnforceAuthor(self) = %v, want nil", err)
	}
	if err := bound.EnforceAuthor("architect"); !errors.Is(err, identity.ErrForeignAuthor) {
		t.Errorf("EnforceAuthor(foreign) = %v, want ErrForeignAuthor", err)
	}

	// A read-only client (no persona) permits any author.
	readonly := &Client{cfg: Config{Realm: "acme"}}
	if err := readonly.EnforceAuthor("anyone"); err != nil {
		t.Errorf("EnforceAuthor on read-only client = %v, want nil", err)
	}
}
