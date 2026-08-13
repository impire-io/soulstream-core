package cli

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/impire-io/soulstream-core/internal/config"
)

// cmdConfig prints each identity field's effective value and its true source. It
// never connects — misconfiguration has to be inspectable offline.
func cmdConfig(resolved config.Resolved, stdout io.Writer) int {
	w := tabwriter.NewWriter(stdout, 2, 0, 2, ' ', 0)
	for _, f := range resolved.Fields() {
		value := f.V
		if value == "" {
			value = "(unset)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", f.Name, value, sourceDescription(f.Name, f.Source))
	}
	_ = w.Flush()
	return 0
}

// sourceDescription renders provenance the way a user would look it up.
func sourceDescription(field string, s config.Source) string {
	switch s.Kind {
	case config.SourceFlag:
		return "flag"
	case config.SourceEnv:
		return "env " + s.Detail
	case config.SourceProject:
		return "project " + s.Detail
	case config.SourceUser:
		return "user " + s.Detail
	default:
		if field == "key_file" || field == "pins_file" {
			return "— keystore default applies"
		}
		return "—"
	}
}
