package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/topic"
)

// sigGlyph renders a verification status as a one-character marker: ✓ verified,
// ✗ failed, ? unknown-key, nothing for unsigned (a realm that never signed should
// look exactly as it always did). The leading space keeps columns tidy.
func sigGlyph(s topic.SigStatus) string {
	switch s {
	case topic.SigVerified:
		return " ✓"
	case topic.SigFailed:
		return " ✗"
	case topic.SigUnknownKey:
		return " ?"
	default:
		return ""
	}
}

// warnDistrusted prints the substitution-attack banner for every distrusted persona,
// on stdout (first, unmissable) and mirrored to stderr (machine-distinguishable).
func warnDistrusted(stdout, stderr io.Writer, kr *identity.Keyring) {
	if kr == nil {
		return
	}
	var names []string
	for name, distrusted := range kr.Distrusted {
		if distrusted {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		line := fmt.Sprintf("!! possible key substitution for %s — signatures from this persona are not trusted", name)
		fmt.Fprintln(stdout, line)
		fmt.Fprintln(stderr, line)
	}
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func renderReport(w io.Writer, r *realm.ProvisionReport) {
	for _, res := range r.Results {
		fmt.Fprintf(w, "%-13s %-13s %s", res.Artefact, res.Outcome, formatSize(res.MaxBytes))
		if len(res.Nonconformities) > 0 {
			fmt.Fprintf(w, " %v", res.Nonconformities)
		}
		fmt.Fprintln(w)
	}
}

func renderBoard(w io.Writer, entries []topic.BoardEntry) {
	for _, e := range entries {
		lc := string(e.Lifecycle)
		if lc == "" {
			lc = "-"
		}
		fmt.Fprintf(w, "%-9s %-40s %s\n", lc, e.Path, e.Announcement.Name)
	}
}

func renderView(w io.Writer, v *topic.MaterializedTopic) {
	fmt.Fprintf(w, "topic:     %s\n", v.Path)
	if v.Announcement != nil {
		fmt.Fprintf(w, "name:      %s\n", v.Announcement.Name)
		if v.Announcement.SubjectMatter != "" {
			fmt.Fprintf(w, "about:     %s\n", v.Announcement.SubjectMatter)
		}
	}
	fmt.Fprintf(w, "lifecycle: %s\n", v.Lifecycle)
	if v.Malformed != "" {
		fmt.Fprintf(w, "malformed: %s\n", v.Malformed)
	}
	if len(v.Contributions) > 0 {
		fmt.Fprintln(w, "contributions:")
		for _, c := range v.Contributions {
			renderContribution(w, c)
		}
	}
	if len(v.Attachments) > 0 {
		fmt.Fprintln(w, "attachments:")
		for _, a := range v.Attachments {
			fmt.Fprintf(w, "  [%s]%s %s (%s, %d bytes) object=%s%s\n",
				shortID(a.OpID), sigGlyph(a.Sig), a.Name, a.ContentType, a.Size, a.Object, attachmentMark(a))
		}
	}
	if len(v.WorkItems) > 0 {
		fmt.Fprintln(w, "work items:")
		for _, item := range v.WorkItems {
			owner := ""
			if item.Owner != "" {
				owner = " owner=" + item.Owner
			}
			fmt.Fprintf(w, "  [%s]%s %s (%s%s): %s\n",
				shortID(item.ID), sigGlyph(item.Sig), item.Author, item.Status, owner, item.Title)
		}
	}
}

// renderWorkItem prints one item in full: status, timeline (void included), and
// the evidence anchored to it.
func renderWorkItem(w io.Writer, v *topic.MaterializedTopic, item topic.WorkItem) {
	fmt.Fprintf(w, "item:   %s%s\n", item.ID, sigGlyph(item.Sig))
	fmt.Fprintf(w, "title:  %s\n", item.Title)
	if item.Body != "" {
		fmt.Fprintf(w, "body:   %s\n", item.Body)
	}
	fmt.Fprintf(w, "opened: %s by %s\n", item.Timestamp.Format("2006-01-02 15:04"), item.Author)
	fmt.Fprintf(w, "status: %s", item.Status)
	if item.Owner != "" {
		fmt.Fprintf(w, " (owner %s)", item.Owner)
	}
	fmt.Fprintln(w)
	if len(item.Timeline) > 0 {
		fmt.Fprintln(w, "timeline:")
		for _, ev := range item.Timeline {
			void := ""
			if ev.Void {
				void = "  VOID"
			}
			fmt.Fprintf(w, "  [%s]%s %s %s %s%s\n",
				shortID(ev.OpID), sigGlyph(ev.Sig), ev.Timestamp.Format("2006-01-02 15:04"), ev.Author, ev.Kind, void)
		}
	}
	var evidence bool
	for _, c := range v.Contributions {
		if c.Anchor == item.ID {
			if !evidence {
				fmt.Fprintln(w, "evidence:")
				evidence = true
			}
			renderContribution(w, c)
		}
	}
	for _, a := range v.Attachments {
		if a.Anchor == item.ID {
			if !evidence {
				fmt.Fprintln(w, "evidence:")
				evidence = true
			}
			fmt.Fprintf(w, "  [%s]%s %s (%s, %d bytes) object=%s\n",
				shortID(a.OpID), sigGlyph(a.Sig), a.Name, a.ContentType, a.Size, a.Object)
		}
	}
}

func renderContribution(w io.Writer, c topic.Contribution) {
	kind := "turn"
	switch c.Type {
	case topic.TypeCommentAdd:
		kind = "comment"
	case topic.TypeCommentReply:
		kind = "reply"
	}
	fmt.Fprintf(w, "  [%s]%s %s (%s", shortID(c.OpID), sigGlyph(c.Sig), c.Author, kind)
	if c.Anchor != "" {
		fmt.Fprintf(w, " -> %s", shortID(c.Anchor))
		if c.Dangling {
			fmt.Fprint(w, " dangling")
		}
	}
	if len(c.Edits) > 0 {
		fmt.Fprint(w, ", edited")
	}
	if c.Resolved {
		fmt.Fprintf(w, ", resolved by %s", c.ResolvedBy)
	}
	fmt.Fprintf(w, "): %s", c.Body)
	if len(c.Mentions) > 0 {
		fmt.Fprintf(w, "  mentions=%v", c.Mentions)
	}
	fmt.Fprintln(w)
}

// attachmentMark renders the withdrawn marker for attachment listings.
func attachmentMark(a topic.Attachment) string {
	if a.Removed {
		return " removed by " + a.RemovedBy
	}
	return ""
}

func shortID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}
