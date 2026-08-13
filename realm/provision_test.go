package realm

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/impire-io/soulstream-core/internal/natstest"
)

// newJS starts an in-process JetStream server and returns a JetStream handle plus a
// cleanup func. Shared by the provisioning tests.
func newJS(t *testing.T) (jetstream.JetStream, func()) {
	t.Helper()
	url, shutdown := natstest.StartJetStream(t)
	nc, err := nats.Connect(url)
	if err != nil {
		shutdown()
		t.Fatalf("connect: %v", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		shutdown()
		t.Fatalf("jetstream: %v", err)
	}
	return js, func() {
		nc.Close()
		shutdown()
	}
}

func outcomeByArtefact(r *ProvisionReport) map[Artefact]ArtefactResult {
	m := map[Artefact]ArtefactResult{}
	for _, res := range r.Results {
		m[res.Artefact] = res
	}
	return m
}

// US1: provisioning a clean server creates both artefacts with the mandated settings.
func TestProvisionFresh(t *testing.T) {
	ctx := context.Background()
	js, cleanup := newJS(t)
	defer cleanup()

	report, err := ProvisionOn(ctx, js)
	if err != nil {
		t.Fatalf("ProvisionOn: %v", err)
	}
	if !report.Conformant() {
		t.Errorf("report not conformant: %+v", report.Results)
	}

	got := outcomeByArtefact(report)
	if got[ArtefactStream].Outcome != OutcomeCreated {
		t.Errorf("stream outcome = %q, want created", got[ArtefactStream].Outcome)
	}
	if got[ArtefactObjectStore].Outcome != OutcomeCreated {
		t.Errorf("object store outcome = %q, want created", got[ArtefactObjectStore].Outcome)
	}
	if got[ArtefactPersonas].Outcome != OutcomeCreated {
		t.Errorf("personas outcome = %q, want created", got[ArtefactPersonas].Outcome)
	}

	// Read back the stream config and verify every mandated setting.
	stream, err := js.Stream(ctx, StreamName)
	if err != nil {
		t.Fatalf("look up stream: %v", err)
	}
	cfg := stream.CachedInfo().Config
	if len(cfg.Subjects) != 1 || cfg.Subjects[0] != StreamSubject {
		t.Errorf("subjects = %v, want [%s]", cfg.Subjects, StreamSubject)
	}
	if cfg.Retention != jetstream.LimitsPolicy {
		t.Errorf("retention = %v, want Limits", cfg.Retention)
	}
	if cfg.MaxAge != 0 {
		t.Errorf("max age = %v, want 0", cfg.MaxAge)
	}
	if !cfg.AllowRollup {
		t.Error("allow rollup = false, want true")
	}
	if cfg.Duplicates < MinDuplicateWindow {
		t.Errorf("duplicate window = %v, want >= %v", cfg.Duplicates, MinDuplicateWindow)
	}
	if cfg.Storage != jetstream.FileStorage {
		t.Errorf("storage = %v, want File", cfg.Storage)
	}

	// Object store exists.
	if _, err := js.ObjectStore(ctx, ObjectBucket); err != nil {
		t.Errorf("object store lookup: %v", err)
	}

	// Persona directory exists with the mandated history depth.
	kv, err := js.KeyValue(ctx, PersonasBucket)
	if err != nil {
		t.Fatalf("personas lookup: %v", err)
	}
	if st, err := kv.Status(ctx); err != nil {
		t.Errorf("personas status: %v", err)
	} else if st.History() != PersonasHistory {
		t.Errorf("personas history = %d, want %d", st.History(), PersonasHistory)
	}
}

// 014/US3+US4: a fresh provision creates the inbox stream with its bounded window,
// and neither stream captures service subjects.
func TestProvisionFreshNotifyStream(t *testing.T) {
	ctx := context.Background()
	js, cleanup := newJS(t)
	defer cleanup()

	report, err := ProvisionOn(ctx, js)
	if err != nil {
		t.Fatalf("ProvisionOn: %v", err)
	}
	got := outcomeByArtefact(report)
	if got[ArtefactNotify].Outcome != OutcomeCreated {
		t.Fatalf("notify stream outcome = %q, want created", got[ArtefactNotify].Outcome)
	}

	stream, err := js.Stream(ctx, NotifyStreamName)
	if err != nil {
		t.Fatalf("look up inbox stream: %v", err)
	}
	cfg := stream.CachedInfo().Config
	if issues := notifyNonconformities(cfg); len(issues) != 0 {
		t.Errorf("fresh inbox stream nonconformant: %v", issues)
	}
	if cfg.MaxMsgsPerSubject != InboxWindow {
		t.Errorf("MaxMsgsPerSubject = %d, want %d", cfg.MaxMsgsPerSubject, InboxWindow)
	}

	// Service traffic is captured by neither stream: a message published on a SVC
	// subject must land in no store.
	nc := js.Conn()
	if err := nc.Publish("SOULSTREAM.SVC.DISCOVER", []byte("transient ask")); err != nil {
		t.Fatalf("publish svc: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	for _, name := range []string{StreamName, NotifyStreamName} {
		s, err := js.Stream(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		info, err := s.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if info.State.Msgs != 0 {
			t.Errorf("stream %s retained %d messages; service traffic must leave no residue", name, info.State.Msgs)
		}
	}
}

// 014/US3: the recognised legacy shape (subjects ["SOULSTREAM.>"]) is converged —
// subjects narrowed with every other setting preserved, the inbox stream created,
// the newest notifications migrated verbatim, and persona/service residue purged —
// while topic history stays untouched.
func TestProvisionConvergesLegacyRealm(t *testing.T) {
	ctx := context.Background()
	js, cleanup := newJS(t)
	defer cleanup()

	// A pre-014 realm: one stream capturing everything, with an operator-tuned
	// MaxBytes that must survive convergence.
	legacy := streamConfig(0)
	legacy.Subjects = []string{LegacyStreamSubject}
	legacy.MaxBytes = 1 << 30
	if _, err := js.CreateStream(ctx, legacy); err != nil {
		t.Fatalf("create legacy stream: %v", err)
	}

	// Seed it the way life would have: topic history, inbox traffic beyond the
	// window for one persona, a little for another, and service residue.
	publish := func(subject, body string) {
		t.Helper()
		if _, err := js.Publish(ctx, subject, []byte(body)); err != nil {
			t.Fatalf("seed %s: %v", subject, err)
		}
	}
	publish("SOULSTREAM.TOPICS.OPS.demo-aaaa", "turn one")
	publish("SOULSTREAM.TOPICS.OPS.demo-aaaa", "turn two")
	publish("SOULSTREAM.TOPICS.INFO.demo-aaaa", "announce")
	for i := 0; i < InboxWindow+20; i++ {
		publish("SOULSTREAM.PERSONA.NOTIFY.busy", fmt.Sprintf("ping %d", i))
	}
	publish("SOULSTREAM.PERSONA.NOTIFY.quiet", "one ping")
	publish("SOULSTREAM.SVC.DISCOVER", "stale ask")

	report, err := ProvisionOn(ctx, js)
	if err != nil {
		t.Fatalf("ProvisionOn: %v", err)
	}
	got := outcomeByArtefact(report)
	if got[ArtefactStream].Outcome != OutcomeUpdated {
		t.Fatalf("legacy stream outcome = %q, want updated (results: %+v)", got[ArtefactStream].Outcome, report.Results)
	}
	if got[ArtefactNotify].Outcome != OutcomeCreated {
		t.Fatalf("notify stream outcome = %q, want created", got[ArtefactNotify].Outcome)
	}
	if !report.Conformant() {
		t.Errorf("converged report not conformant: %+v", report.Results)
	}

	// The op-log: narrowed subjects, preserved MaxBytes, topic history intact,
	// persona + service residue gone.
	stream, err := js.Stream(ctx, StreamName)
	if err != nil {
		t.Fatal(err)
	}
	cfg := stream.CachedInfo().Config
	if len(cfg.Subjects) != 1 || cfg.Subjects[0] != StreamSubject {
		t.Errorf("subjects after convergence = %v, want [%s]", cfg.Subjects, StreamSubject)
	}
	if cfg.MaxBytes != 1<<30 {
		t.Errorf("operator-tuned MaxBytes not preserved: %d", cfg.MaxBytes)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != 3 {
		t.Errorf("op-log holds %d messages after purge, want 3 (topic history only)", info.State.Msgs)
	}

	// The inbox stream: busy capped at the window (the NEWEST window), quiet intact.
	notify, err := js.Stream(ctx, NotifyStreamName)
	if err != nil {
		t.Fatal(err)
	}
	ninfo, err := notify.Info(ctx, jetstream.WithSubjectFilter(NotifyStreamSubject))
	if err != nil {
		t.Fatal(err)
	}
	if n := ninfo.State.Subjects["SOULSTREAM.PERSONA.NOTIFY.busy"]; n != InboxWindow {
		t.Errorf("busy inbox migrated %d notifications, want %d", n, InboxWindow)
	}
	if n := ninfo.State.Subjects["SOULSTREAM.PERSONA.NOTIFY.quiet"]; n != 1 {
		t.Errorf("quiet inbox migrated %d notifications, want 1", n)
	}
	last, err := notify.GetLastMsgForSubject(ctx, "SOULSTREAM.PERSONA.NOTIFY.busy")
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("ping %d", InboxWindow+19); string(last.Data) != want {
		t.Errorf("newest migrated notification = %q, want %q", last.Data, want)
	}

	// Re-provision: everything is now conformant, nothing converges twice.
	again, err := ProvisionOn(ctx, js)
	if err != nil {
		t.Fatalf("second ProvisionOn: %v", err)
	}
	if !again.Conformant() {
		t.Errorf("re-provision after convergence not conformant: %+v", again.Results)
	}
	if o := outcomeByArtefact(again)[ArtefactStream].Outcome; o != OutcomeConformant {
		t.Errorf("second run stream outcome = %q, want conformant", o)
	}
}

// US2: a second run on a conformant realm makes zero changes and reports conformant.
func TestProvisionIdempotent(t *testing.T) {
	ctx := context.Background()
	js, cleanup := newJS(t)
	defer cleanup()

	if _, err := ProvisionOn(ctx, js); err != nil {
		t.Fatalf("first ProvisionOn: %v", err)
	}
	s1, err := js.Stream(ctx, StreamName)
	if err != nil {
		t.Fatalf("look up stream after first run: %v", err)
	}
	createdBefore := s1.CachedInfo().Created

	report, err := ProvisionOn(ctx, js)
	if err != nil {
		t.Fatalf("second ProvisionOn: %v", err)
	}
	got := outcomeByArtefact(report)
	if got[ArtefactStream].Outcome != OutcomeConformant {
		t.Errorf("stream outcome on re-run = %q, want conformant", got[ArtefactStream].Outcome)
	}
	if got[ArtefactObjectStore].Outcome != OutcomeConformant {
		t.Errorf("object store outcome on re-run = %q, want conformant", got[ArtefactObjectStore].Outcome)
	}
	if got[ArtefactPersonas].Outcome != OutcomeConformant {
		t.Errorf("personas outcome on re-run = %q, want conformant", got[ArtefactPersonas].Outcome)
	}

	// Zero changes: the stream was not recreated (its creation time is unchanged).
	s2, err := js.Stream(ctx, StreamName)
	if err != nil {
		t.Fatalf("look up stream after second run: %v", err)
	}
	if !s2.CachedInfo().Created.Equal(createdBefore) {
		t.Errorf("stream Created changed %v -> %v: the stream was recreated", createdBefore, s2.CachedInfo().Created)
	}
}

// US2: provisioning completes a partial realm — creates only the missing part.
func TestProvisionPartialCreatesOnlyMissing(t *testing.T) {
	ctx := context.Background()
	js, cleanup := newJS(t)
	defer cleanup()

	// Pre-create only the stream (mandated config); leave the object store missing.
	if _, err := js.CreateStream(ctx, streamConfig(0)); err != nil {
		t.Fatalf("pre-create stream: %v", err)
	}
	s1, err := js.Stream(ctx, StreamName)
	if err != nil {
		t.Fatalf("look up pre-created stream: %v", err)
	}
	createdBefore := s1.CachedInfo().Created

	report, err := ProvisionOn(ctx, js)
	if err != nil {
		t.Fatalf("ProvisionOn: %v", err)
	}
	got := outcomeByArtefact(report)
	if got[ArtefactStream].Outcome != OutcomeConformant {
		t.Errorf("stream outcome = %q, want conformant (already present)", got[ArtefactStream].Outcome)
	}
	if got[ArtefactObjectStore].Outcome != OutcomeCreated {
		t.Errorf("object store outcome = %q, want created (was missing)", got[ArtefactObjectStore].Outcome)
	}

	// The pre-existing stream must be untouched, and the bucket must now exist.
	s2, err := js.Stream(ctx, StreamName)
	if err != nil {
		t.Fatalf("look up stream after run: %v", err)
	}
	if !s2.CachedInfo().Created.Equal(createdBefore) {
		t.Error("pre-existing stream was recreated")
	}
	if _, err := js.ObjectStore(ctx, ObjectBucket); err != nil {
		t.Errorf("object store should now exist: %v", err)
	}
}

// 006: a pre-existing persona directory is reported, never modified — even when its
// history depth differs from the mandate (existence is the mandate).
func TestProvisionPersonasNeverModified(t *testing.T) {
	ctx := context.Background()
	js, cleanup := newJS(t)
	defer cleanup()

	if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: PersonasBucket, History: 3}); err != nil {
		t.Fatalf("pre-create personas bucket: %v", err)
	}

	report, err := ProvisionOn(ctx, js)
	if err != nil {
		t.Fatalf("ProvisionOn: %v", err)
	}
	if got := outcomeByArtefact(report); got[ArtefactPersonas].Outcome != OutcomeConformant {
		t.Errorf("personas outcome = %q, want conformant (already present)", got[ArtefactPersonas].Outcome)
	}

	kv, err := js.KeyValue(ctx, PersonasBucket)
	if err != nil {
		t.Fatalf("personas lookup: %v", err)
	}
	if st, err := kv.Status(ctx); err != nil {
		t.Fatalf("personas status: %v", err)
	} else if st.History() != 3 {
		t.Errorf("pre-existing bucket history changed to %d — provisioning mutated it", st.History())
	}
}

// US2: drift is reported with the specific nonconformity and never mutated in place.
func TestProvisionReportsDriftWithoutMutating(t *testing.T) {
	ctx := context.Background()
	js, cleanup := newJS(t)
	defer cleanup()

	// Pre-create a nonconformant stream: age-based expiry present, rollup disabled.
	drifted := streamConfig(0)
	drifted.MaxAge = 720 * time.Hour
	drifted.AllowRollup = false
	if _, err := js.CreateStream(ctx, drifted); err != nil {
		t.Fatalf("pre-create drifted stream: %v", err)
	}

	report, err := ProvisionOn(ctx, js)
	if err != nil {
		t.Fatalf("ProvisionOn (must succeed even when nonconformant): %v", err)
	}

	sr := outcomeByArtefact(report)[ArtefactStream]
	if sr.Outcome != OutcomeNonconformant {
		t.Fatalf("stream outcome = %q, want nonconformant", sr.Outcome)
	}
	joined := strings.Join(sr.Nonconformities, "; ")
	if !strings.Contains(joined, "MaxAge") {
		t.Errorf("nonconformities %v missing MaxAge drift", sr.Nonconformities)
	}
	if !strings.Contains(joined, "AllowRollup") {
		t.Errorf("nonconformities %v missing AllowRollup drift", sr.Nonconformities)
	}
	if report.Conformant() {
		t.Error("report.Conformant() = true, want false")
	}

	// The drifted stream must be unchanged — provisioning must not mutate in place.
	cfg := mustStreamConfig(ctx, t, js)
	if cfg.MaxAge != 720*time.Hour {
		t.Errorf("MaxAge was mutated to %v; provisioning must not modify in place", cfg.MaxAge)
	}
	if cfg.AllowRollup {
		t.Error("AllowRollup was mutated to true; provisioning must not modify in place")
	}
}

func mustStreamConfig(ctx context.Context, t *testing.T, js jetstream.JetStream) jetstream.StreamConfig {
	t.Helper()
	s, err := js.Stream(ctx, StreamName)
	if err != nil {
		t.Fatalf("look up stream: %v", err)
	}
	return s.CachedInfo().Config
}

// newLimitedJS is newJS against a server whose account requires MaxBytes on every
// stream — the NGS R1 shape (016).
func newLimitedJS(t *testing.T) (jetstream.JetStream, func()) {
	t.Helper()
	url, shutdown := natstest.StartJetStreamMaxBytesRequired(t)
	nc, err := nats.Connect(url)
	if err != nil {
		shutdown()
		t.Fatalf("connect: %v", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		shutdown()
		t.Fatalf("jetstream: %v", err)
	}
	return js, func() {
		nc.Close()
		shutdown()
	}
}

// US1 scenario 2 (016): without budgets, a limit-enforced account refuses
// provisioning with an error naming the refused artefact — today's failure, kept.
func TestProvisionLimitEnforcedWithoutBudgetsFails(t *testing.T) {
	ctx := context.Background()
	js, cleanup := newLimitedJS(t)
	defer cleanup()

	_, err := ProvisionOn(ctx, js)
	if err == nil {
		t.Fatal("ProvisionOn without budgets on a MaxBytesRequired account succeeded, want error")
	}
	if !strings.Contains(err.Error(), StreamName) {
		t.Errorf("error %q does not name the refused artefact %q", err, StreamName)
	}
}

// US1 scenario 1 (016): with the default budgets, the same account provisions
// everything out of the box, each artefact carrying its documented roof.
func TestProvisionLimitEnforcedWithDefaultBudgets(t *testing.T) {
	ctx := context.Background()
	js, cleanup := newLimitedJS(t)
	defer cleanup()

	report, err := ProvisionOn(ctx, js, DefaultBudgets())
	if err != nil {
		t.Fatalf("ProvisionOn with DefaultBudgets: %v", err)
	}
	byArtefact := outcomeByArtefact(report)
	want := map[Artefact]int64{
		ArtefactStream:      1 << 30,
		ArtefactNotify:      NotifyMaxBytes,
		ArtefactPersonas:    64 << 20,
		ArtefactObjectStore: 512 << 20,
	}
	for artefact, roof := range want {
		res, ok := byArtefact[artefact]
		if !ok {
			t.Fatalf("no result for %s", artefact)
		}
		if res.Outcome != OutcomeCreated {
			t.Errorf("%s outcome = %s, want created", artefact, res.Outcome)
		}
		if res.MaxBytes != roof {
			t.Errorf("%s reported roof = %d, want %d", artefact, res.MaxBytes, roof)
		}
	}

	// The roofs must be real on the server, not just in the report.
	if got := mustStreamConfig(ctx, t, js).MaxBytes; got != 1<<30 {
		t.Errorf("op-log MaxBytes on server = %d, want %d", got, int64(1<<30))
	}
	for stream, roof := range map[string]int64{
		NotifyStreamName:       NotifyMaxBytes,
		"KV_" + PersonasBucket: 64 << 20,
		"OBJ_" + ObjectBucket:  512 << 20,
	} {
		s, err := js.Stream(ctx, stream)
		if err != nil {
			t.Fatalf("look up %s: %v", stream, err)
		}
		if got := s.CachedInfo().Config.MaxBytes; got != roof {
			t.Errorf("%s MaxBytes on server = %d, want %d", stream, got, roof)
		}
	}
}

// FR-005 (016): impossible budgets are rejected before any server contact — proven
// by passing a nil JetStream handle, which would panic if touched.
func TestProvisionBudgetsValidatedBeforeServerContact(t *testing.T) {
	ctx := context.Background()

	if _, err := ProvisionOn(ctx, nil, Budgets{Personas: -1}); err == nil {
		t.Error("negative budget accepted, want error")
	} else if !strings.Contains(err.Error(), "persona directory") {
		t.Errorf("error %q does not name the artefact", err)
	}

	if _, err := ProvisionOn(ctx, nil, Budgets{}, Budgets{}); err == nil {
		t.Error("two Budgets values accepted, want error")
	}
}

// US2 (016): a partial Budgets value budgets only what it names — the inbox keeps
// its mandated roof (never unlimited), everything else stays unlimited.
func TestProvisionPartialBudgets(t *testing.T) {
	ctx := context.Background()
	js, cleanup := newJS(t)
	defer cleanup()

	report, err := ProvisionOn(ctx, js, Budgets{Objects: 512 << 20})
	if err != nil {
		t.Fatalf("ProvisionOn with partial budgets: %v", err)
	}
	byArtefact := outcomeByArtefact(report)
	for artefact, roof := range map[Artefact]int64{
		ArtefactStream:      0,
		ArtefactNotify:      NotifyMaxBytes,
		ArtefactPersonas:    0,
		ArtefactObjectStore: 512 << 20,
	} {
		if got := byArtefact[artefact].MaxBytes; got != roof {
			t.Errorf("%s roof = %d, want %d", artefact, got, roof)
		}
	}
	// The server stores "no roof" as -1; anything positive would be a real roof.
	if got := mustStreamConfig(ctx, t, js).MaxBytes; got > 0 {
		t.Errorf("op-log MaxBytes = %d, want unlimited", got)
	}
}

// US3 (016): re-provisioning with different budgets mutates nothing and reports
// the roofs as found on the server.
func TestProvisionReportsRoofsAsFound(t *testing.T) {
	ctx := context.Background()
	js, cleanup := newJS(t)
	defer cleanup()

	if _, err := ProvisionOn(ctx, js, DefaultBudgets()); err != nil {
		t.Fatalf("first provision: %v", err)
	}

	// Different budgets on the second run: applied nowhere, first run's roofs reported.
	report, err := ProvisionOn(ctx, js, Budgets{OpLog: 2 << 30, Objects: 1 << 30})
	if err != nil {
		t.Fatalf("second provision: %v", err)
	}
	byArtefact := outcomeByArtefact(report)
	for artefact, roof := range map[Artefact]int64{
		ArtefactStream:      1 << 30,
		ArtefactNotify:      NotifyMaxBytes,
		ArtefactPersonas:    64 << 20,
		ArtefactObjectStore: 512 << 20,
	} {
		res := byArtefact[artefact]
		if res.Outcome == OutcomeCreated {
			t.Errorf("%s outcome = created on a re-run, want existing", artefact)
		}
		if res.MaxBytes != roof {
			t.Errorf("%s reported roof = %d, want as-found %d", artefact, res.MaxBytes, roof)
		}
	}
	if got := mustStreamConfig(ctx, t, js).MaxBytes; got != 1<<30 {
		t.Errorf("op-log MaxBytes = %d after re-provision, want untouched %d", got, int64(1<<30))
	}
}
