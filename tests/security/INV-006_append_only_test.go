package security

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/evidence"
)

// INV-006: historical evidence cannot be silently mutated.
//
// The word is "silently". Corrections are permitted and necessary; what is forbidden
// is a correction that leaves no trace of what was believed before. ADR-009 makes a
// correction a new row referencing the old one, so the earlier record survives
// exactly as it was written.
//
// The database half of this invariant is in INV-006_append_only_db_test.go behind the
// integration tag: a privilege that is not exercised against a real PostgreSQL is a
// claim, not a guard.

var evAt = time.Date(2026, 8, 27, 14, 32, 4, 0, time.UTC)

func evidenceEvent(id string) evidence.Event {
	return evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       id,
		EventName:     evidence.PolicyEvaluated,
		TenantID:      "tenant_acme",
		AggregateID:   "env_1",
		CorrelationID: "corr_1",
		OccurredAt:    evAt,
		ProducedAt:    evAt,
		Producer:      "assurance-gateway",
	}
}

// The store exposes no way to change or remove a recorded event.
func TestEvidenceStoreExposesNoMutation(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, "../../internal/evidence", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	banned := map[string]string{
		"Update": "an evidence store with an Update method is not append-only",
		"Delete": "an evidence store with a Delete method is not append-only",
		"Remove": "an evidence store with a Remove method is not append-only",
		"Purge":  "purging evidence is deletion with a friendlier name",
	}

	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok || fn.Recv == nil || !fn.Name.IsExported() {
					return true
				}
				for prefix, reason := range banned {
					if strings.HasPrefix(fn.Name.Name, prefix) {
						t.Errorf("%s declares %s: %s (INV-006)", path, fn.Name.Name, reason)
					}
				}
				return true
			})
		}
	}
}

// No SQL in the package updates or deletes evidence.
func TestNoUpdateOrDeleteSQLAgainstEvidence(t *testing.T) {
	raw, err := os.ReadFile("../../internal/evidence/store.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := strings.ToUpper(string(raw))

	for _, statement := range []string{"UPDATE EVIDENCE_EVENTS", "DELETE FROM EVIDENCE_EVENTS", "TRUNCATE"} {
		if strings.Contains(body, statement) {
			t.Errorf("store.go contains %q; evidence is append-only (ADR-009, INV-006)", statement)
		}
	}
}

// A correction must name what it supersedes. A correction pointing at nothing is an
// unexplained change of the record.
func TestCorrectionMustNameItsTarget(t *testing.T) {
	e := evidenceEvent("ev_correction")
	e.EventName = evidence.EvidenceCorrected

	if err := e.Validate(); err == nil {
		t.Fatal("a correction with no corrects_event_id was accepted (ADR-009, INV-006)")
	}

	e.CorrectsEventID = "ev_original"
	if err := e.Validate(); err != nil {
		t.Fatalf("a well-formed correction was rejected: %v", err)
	}
}

func TestEventCannotCorrectItself(t *testing.T) {
	e := evidenceEvent("ev_1")
	e.EventName = evidence.EvidenceCorrected
	e.CorrectsEventID = "ev_1"

	if err := e.Validate(); err == nil {
		t.Fatal("an event that corrects itself was accepted; that is a rewrite (INV-006)")
	}
}

// Every event has to be attributable and placeable, or a timeline cannot be
// reconstructed from it and the record is not evidence.
func TestUnattributableEventsAreRefused(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(*evidence.Event)
	}{
		{"no producer", func(e *evidence.Event) { e.Producer = "" }},
		{"no correlation id", func(e *evidence.Event) { e.CorrelationID = "" }},
		{"no tenant", func(e *evidence.Event) { e.TenantID = "" }},
		{"no event id", func(e *evidence.Event) { e.EventID = "" }},
		{"no aggregate", func(e *evidence.Event) { e.AggregateID = "" }},
		{"no occurrence time", func(e *evidence.Event) { e.OccurredAt = time.Time{} }},
		{"unknown event name", func(e *evidence.Event) { e.EventName = "something.invented.v1" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := evidenceEvent("ev_1")
			tc.break_(&e)
			if err := e.Validate(); err == nil {
				t.Errorf("an event with %s was accepted into the record (INV-006)", tc.name)
			}
		})
	}
}

// The event catalog is closed. An event nobody wrote down cannot be recorded,
// because a timeline containing unnamed events cannot be replayed.
func TestEventCatalogIsClosed(t *testing.T) {
	if evidence.Known("agent.intent.invented.v1") {
		t.Error("an unlisted event name was accepted into the catalog")
	}
	if !evidence.Known(evidence.IntentReceived) {
		t.Error("a catalogued event name was rejected")
	}
	if n := len(evidence.CatalogNames()); n < 29 {
		t.Errorf("the catalog holds %d names; spec section 32 lists 29 plus the "+
			"correction event ADR-009 requires", n)
	}
}
