package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/impire-io/soulstream-core/internal/keystore"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/registry"
)

// cmdProfile manages directory profiles: publish (create-or-metadata-update for the
// session persona), attest (countersign operating another persona), and show (any
// persona's profile, operator claim, chain, and pin state).
func cmdProfile(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "soulstream: usage: profile publish|attest|show")
		return 2
	}
	switch args[0] {
	case "publish":
		return profilePublish(ctx, connect, cfg, args[1:], stdout, stderr)
	case "attest":
		return profileAttest(ctx, connect, cfg, args[1:], stdout, stderr)
	case "show":
		return profileShow(ctx, connect, cfg, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "soulstream: unknown profile subcommand %q (want publish|attest|show)\n", args[0])
		return 2
	}
}

// profileAttest countersigns "I operate <persona>": it signs the attestation
// statement with this persona's key and prints a portable token the operated persona
// includes in its own `profile publish --attestation`. The secret key never moves;
// profiles stay self-published.
func profileAttest(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: soulstream profile attest <operated-persona>")
		return 2
	}
	operated := args[0]

	signer, err := loadSigner(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "soulstream: %v\n", err)
		return 2
	}
	if signer == nil {
		fmt.Fprintln(stderr, "soulstream: attesting requires a signing key (run `soulstream key init` first)")
		return 2
	}

	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		// Bind the operated persona's current key when it has one published;
		// an absent profile or keyless persona binds on the name alone.
		operatedKey := ""
		p, ok, err := registry.Lookup(ctx, c, operated)
		if err != nil {
			return err
		}
		if ok && p.SigningKey != nil {
			operatedKey = p.SigningKey.Ed25519
		}

		token, err := registry.NewAttestationToken(signer, cfg.Persona, operated, operatedKey)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, token)
		fmt.Fprintf(stderr, "hand this token to %q; it publishes with:\n  soulstream profile publish --operated-by %s --attestation <token>\n", operated, cfg.Persona)
		if operatedKey == "" {
			fmt.Fprintf(stderr, "note: %q has no published signing key yet — the vouch binds to the name alone; consider re-attesting once it has a key\n", operated)
		}
		return nil
	})
}

func profilePublish(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("profile publish", flag.ContinueOnError)
	fs.SetOutput(stderr)
	displayName := fs.String("display-name", "", "presentation name")
	description := fs.String("description", "", "one-line description")
	operatedBy := fs.String("operated-by", "", "persona that operates (answers for) this one")
	attestation := fs.String("attestation", "", "attestation token from the operator (see profile attest)")
	if err := parseInterspersed(fs, args); err != nil {
		return 2
	}

	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		p := registry.Profile{
			Name:        cfg.Persona,
			DisplayName: *displayName,
			Description: *description,
			OperatedBy:  *operatedBy,
			CreatedAt:   time.Now().UTC(),
		}
		if *attestation != "" {
			tok, err := registry.ParseAttestationToken(*attestation)
			if err != nil {
				return err
			}
			if *operatedBy == "" {
				return fmt.Errorf("--attestation requires --operated-by %s", tok.Operator)
			}
			if tok.Operator != *operatedBy {
				return fmt.Errorf("attestation token is from %q but --operated-by names %q", tok.Operator, *operatedBy)
			}
			if tok.Operated != cfg.Persona {
				return fmt.Errorf("attestation token vouches for %q, not this persona (%q)", tok.Operated, cfg.Persona)
			}
			p.OperatorAttestation = &registry.OperatorAttestation{OperatedKey: tok.OperatedKey, Sig: tok.Sig}
		}
		// Include the public key when this persona has one; the stored key material
		// stays authoritative on metadata updates.
		signer, err := loadSigner(cfg)
		if err != nil {
			return err
		}
		if signer != nil {
			p.SigningKey = &registry.SigningKeyInfo{Ed25519: signer.PublicKey(), Since: time.Now().UTC()}
		}
		if err := registry.Publish(ctx, c, p); err != nil {
			return err
		}
		if signer != nil {
			fmt.Fprintf(stdout, "published profile for %q (key %s)\n", cfg.Persona, signer.PublicKey())
		} else {
			fmt.Fprintf(stdout, "published profile for %q (no signing key)\n", cfg.Persona)
		}
		return nil
	})
}

func profileShow(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: soulstream profile show <persona>")
		return 2
	}
	persona := args[0]

	return withClient(ctx, connect, cfg, false, stderr, func(c *realm.Client) error {
		p, ok, err := registry.Lookup(ctx, c, persona)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no directory profile for %q", persona)
		}

		fmt.Fprintf(stdout, "name:         %s\n", p.Name)
		if p.DisplayName != "" {
			fmt.Fprintf(stdout, "display name: %s\n", p.DisplayName)
		}
		if p.Description != "" {
			fmt.Fprintf(stdout, "description:  %s\n", p.Description)
		}
		if p.OperatedBy != "" {
			kr := realmKeyring(ctx, c, cfg, stderr)
			operatorChain, operatorDistrusted := kr.ChainFor(p.OperatedBy)
			operatedChain, _ := kr.ChainFor(p.Name)
			status := registry.AttestationStatus(p, operatorChain, operatorDistrusted, operatedChain)
			if status == registry.ClaimFailed {
				fmt.Fprintf(stdout, "operated by:  %s  [FAILED]\n", p.OperatedBy)
				fmt.Fprintln(stdout, "!! operator claim FAILED — the countersignature does not vouch; treat the claim as unbacked")
				fmt.Fprintln(stderr, "soulstream: WARNING: operator claim FAILED for", p.Name)
			} else {
				fmt.Fprintf(stdout, "operated by:  %s  [%s]\n", p.OperatedBy, status)
			}
			printPrincipal(ctx, stdout, c, p.Name)
		}

		chain, chainErr := registry.Chain(p)
		switch {
		case chainErr != nil:
			fmt.Fprintf(stdout, "key chain:    INVALID — %v\n", chainErr)
		case len(chain) == 0:
			fmt.Fprintln(stdout, "key chain:    (no signing key published)")
		default:
			fmt.Fprintln(stdout, "key chain:")
			for i, k := range chain {
				marker := ""
				if i == len(chain)-1 {
					marker = "  (current)"
				}
				fmt.Fprintf(stdout, "  %d. %s%s\n", i+1, k, marker)
			}
		}

		fmt.Fprintf(stdout, "pin state:    %s\n", pinState(cfg, persona, chain, chainErr))
		return nil
	})
}

// printPrincipal walks operated_by links from persona and prints where they end:
// the principal who answers for it, a dangling operator, or an invalid loop.
// Bulk-read warnings are deliberately dropped here — the chain simply treats an
// invalid profile as absent (dangling), keeping the show readable.
func printPrincipal(ctx context.Context, stdout io.Writer, c *realm.Client, persona string) {
	profiles, _, err := registry.All(ctx, c)
	if err != nil {
		fmt.Fprintf(stdout, "principal:    unknown (%v)\n", err)
		return
	}
	byName := make(map[string]registry.Profile, len(profiles))
	for _, p := range profiles {
		byName[p.Name] = p
	}
	chain, terminal := registry.OperatorChain(byName, persona)
	trail := strings.Join(chain, " → ")
	switch terminal {
	case registry.TerminalPrincipal:
		fmt.Fprintf(stdout, "principal:    %s  (%s)\n", chain[len(chain)-1], trail)
	case registry.TerminalDangling:
		fmt.Fprintf(stdout, "principal:    unknown — operator %q has no directory entry  (%s)\n", chain[len(chain)-1], trail)
	case registry.TerminalCycle:
		fmt.Fprintf(stdout, "principal:    INVALID — operator links loop  (%s)\n", trail)
	}
}

// pinState compares a persona's published chain with this client's pin.
func pinState(cfg Config, persona string, chain []string, chainErr error) string {
	pinsPath, err := keystore.ResolvePinsFile(cfg.PinsFile, cfg.Realm)
	if err != nil {
		return fmt.Sprintf("unknown (%v)", err)
	}
	pins, err := keystore.LoadPins(pinsPath, cfg.Realm)
	if err != nil {
		return fmt.Sprintf("unknown (%v)", err)
	}
	pinned := pins.Personas[persona]

	switch {
	case chainErr != nil:
		return "DISTRUSTED — published chain is invalid"
	case len(pinned) == 0:
		return "not pinned yet (will pin on next read)"
	case isChainPrefix(pinned, chain):
		if len(pinned) == len(chain) {
			return "pinned (matches)"
		}
		return "pinned (published chain extends the pin; will re-pin on next read)"
	default:
		return "DISTRUSTED — possible key substitution (published chain does not extend the pin)"
	}
}

// isChainPrefix mirrors the registry's pin rule for display.
func isChainPrefix(pin, chain []string) bool {
	if len(pin) > len(chain) {
		return false
	}
	for i := range pin {
		if pin[i] != chain[i] {
			return false
		}
	}
	return true
}
