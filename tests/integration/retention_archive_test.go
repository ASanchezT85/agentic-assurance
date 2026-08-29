//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"agentic-assurance/adapters/objectstore"
	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/retention"
)

// The archive path, end to end: a month of evidence out of PostgreSQL, into an
// S3-compatible object store, and back with its integrity proved.
//
// The package had the policy rules, the manifest shape and the hash chain, each with a
// passing test, and nothing that moved a month of evidence anywhere. The acceptance gate
// read "archive export, restore, tamper verification, legal hold" against primitives
// that had never met a bucket.
//
// What is proved here is not that a file arrived. It is that an archive nobody can trust
// is refused: an edited payload, a truncated file, a manifest belonging to a different
// archive, and an upload that failed leaving the source untouched. And that a legal hold
// outranks the lot.

func objectStore(t *testing.T) *objectstore.S3 {
	t.Helper()
	endpoint := os.Getenv("OBJECT_STORE_ENDPOINT")
	if endpoint == "" {
		t.Skip("OBJECT_STORE_ENDPOINT is not set; the archive path is not being exercised")
	}
	bucket := os.Getenv("OBJECT_STORE_BUCKET")
	if bucket == "" {
		bucket = "assurance-evidence"
	}
	store, err := objectstore.New(context.Background(), objectstore.Config{
		Endpoint:  endpoint,
		AccessKey: os.Getenv("OBJECT_STORE_ACCESS_KEY"),
		SecretKey: os.Getenv("OBJECT_STORE_SECRET_KEY"),
		Bucket:    bucket,
	})
	if err != nil {
		t.Skipf("no object store at %s: %v", endpoint, err)
	}
	return store
}

// writeMonth commits a period of evidence the way the pipeline commits it.
func writeMonth(t *testing.T, store *evidence.Store, tenant string, start time.Time,
	count int) {

	t.Helper()
	ctx := context.Background()
	batch := make([]evidence.Event, 0, count)
	for i := range count {
		at := start.Add(time.Duration(i) * time.Minute)
		batch = append(batch, evidence.Event{
			SchemaVersion: evidence.SchemaVersion,
			EventID:       fmt.Sprintf("arch_%s_%d", tenant, i),
			EventName:     evidence.AuthorityEvaluated,
			TenantID:      tenant,
			AggregateID:   fmt.Sprintf("env_%d", i),
			CorrelationID: fmt.Sprintf("corr_%d", i),
			OccurredAt:    at,
			ProducedAt:    at,
			Producer:      "assurance-gateway",
			Sequence:      int64(i + 1),
			// A decision with a value in it, because the payload is what an archive
			// tamper has to be able to change.
			Payload: map[string]any{"allowed": true, "grant_id": "grant_arch"},
		})
	}
	if err := store.AppendBatch(ctx, batch); err != nil {
		t.Fatalf("commit the month: %v", err)
	}
}

// refusingStore is an object store whose upload fails, which is the case that decides
// whether a failed export can authorize a deletion.
type refusingStore struct {
	retention.ObjectStore
	err error
}

func (r refusingStore) Put(context.Context, string, []byte) error { return r.err }

// tamperingStore hands back a body different from the one that was written.
type tamperingStore struct {
	retention.ObjectStore
	rewrite func([]byte) []byte
}

func (m tamperingStore) Get(ctx context.Context, key string) ([]byte, error) {
	body, err := m.ObjectStore.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return m.rewrite(body), nil
}

func TestEvidenceArchivesAndRestores(t *testing.T) {
	ctx := context.Background()
	pool := idemPool(t)
	objects := objectStore(t)

	now := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_arch_%d", now.UnixNano())
	// Inside the current partition, and far enough back that the events are ordered
	// by a time this test controls rather than by when it happened to run.
	start := now.Truncate(time.Hour).Add(-12 * time.Hour)
	const events = 25

	evidenceStore := evidence.NewStore(pool)
	writeMonth(t, evidenceStore, tenant, start, events)

	manifests := retention.NewPostgresStore(pool)
	exporter := &retention.Exporter{
		Events:    evidenceStore,
		Objects:   objects,
		Manifests: manifests,
	}
	partition := fmt.Sprintf("p_%d", now.UnixNano())
	from, to := start.Add(-time.Minute), start.Add(24*time.Hour)

	// 1. Successful archive.
	manifest, err := exporter.Export(ctx, tenant, partition, from, to, "ops@example.test")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if manifest.EventCount != events {
		t.Fatalf("the manifest counted %d events, the period holds %d",
			manifest.EventCount, events)
	}
	if manifest.ChainHead == "" {
		t.Fatal("the manifest has no chain head; there is nothing to verify it against")
	}

	// Recorded where an operator looks for it, not only in the return value.
	stored, err := manifests.Manifest(ctx, tenant, partition)
	if err != nil {
		t.Fatalf("read the manifest back: %v", err)
	}
	if stored.ChainHead != manifest.ChainHead || stored.EventCount != manifest.EventCount {
		t.Errorf("the recorded manifest describes a different archive: %+v", stored)
	}

	// 2. Successful restore.
	restored, err := exporter.Restore(ctx, *stored)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(restored) != events {
		t.Fatalf("restored %d events of %d", len(restored), events)
	}
	if restored[0].Payload["grant_id"] != "grant_arch" {
		t.Errorf("the restored payload is not the one archived: %v", restored[0].Payload)
	}

	// 3. A tampered payload is refused.
	//
	// The edit that matters: an authority decision flipped from allowed to refused,
	// leaving every identity field untouched. An earlier version of the hash covered
	// only identity, and this exact change verified clean.
	t.Run("a tampered payload is refused", func(t *testing.T) {
		tampering := &retention.Exporter{
			Events: evidenceStore,
			Objects: tamperingStore{ObjectStore: objects, rewrite: func(b []byte) []byte {
				return []byte(strings.Replace(string(b),
					`"allowed":true`, `"allowed":false`, 1))
			}},
		}
		// Refused by the chain rather than by the count: the edit leaves the archive
		// exactly as long as the manifest says, and the recomputed head differs.
		if _, err := tampering.Restore(ctx, *stored); !errors.Is(err, retention.ErrTampered) {
			t.Errorf("a rewritten decision restored with err=%v; an archive that can be "+
				"edited is not evidence", err)
		}
	})

	// 4. Truncation is refused.
	//
	// Every hash left in a truncated archive is still valid — that is why the count is
	// checked. Dropping the last events of a month is the cheapest way to make an
	// incident disappear.
	t.Run("a truncated archive is refused", func(t *testing.T) {
		truncating := &retention.Exporter{
			Events: evidenceStore,
			Objects: tamperingStore{ObjectStore: objects, rewrite: func(b []byte) []byte {
				lines := strings.SplitAfter(string(b), "\n")
				return []byte(strings.Join(lines[:len(lines)-6], ""))
			}},
		}
		if _, err := truncating.Restore(ctx, *stored); !errors.Is(err, retention.ErrTruncated) {
			t.Errorf("a truncated archive restored with err=%v; the missing events are "+
				"the ones somebody wanted gone", err)
		}
	})

	// 5. The wrong manifest is refused.
	t.Run("the wrong manifest is refused", func(t *testing.T) {
		wrong := *stored
		wrong.ChainHead = strings.Repeat("0", len(stored.ChainHead))
		if _, err := exporter.Restore(ctx, wrong); !errors.Is(err, retention.ErrTampered) {
			t.Errorf("an archive verified against a manifest that does not describe it: "+
				"err=%v", err)
		}

		short := *stored
		short.EventCount = stored.EventCount + 10
		if _, err := exporter.Restore(ctx, short); !errors.Is(err, retention.ErrTruncated) {
			t.Errorf("an archive accepted against a manifest counting more events: err=%v", err)
		}
	})

	// 6. A legal hold prevents destruction.
	t.Run("a legal hold prevents destruction", func(t *testing.T) {
		// With no hold the gate opens, so the refusal below is the hold and not a
		// gate that refuses everything.
		if err := exporter.Destroy(ctx, manifests, *stored); err != nil {
			t.Fatalf("destruction refused with no hold and a verified archive: %v", err)
		}

		hold := retention.Hold{
			TenantID: tenant, HoldID: "hold_" + partition,
			Reason: "regulatory inquiry", PlacedBy: "counsel@example.test",
			PlacedAt: time.Now().UTC(),
		}
		if err := manifests.PlaceHold(ctx, hold); err != nil {
			t.Fatalf("place the hold: %v", err)
		}
		if err := exporter.Destroy(ctx, manifests, *stored); !errors.Is(err, retention.ErrHeld) {
			t.Errorf("destruction was permitted under a legal hold: err=%v", err)
		}

		// And releasing it is an act with an author, after which the gate opens again.
		if err := manifests.ReleaseHold(ctx, tenant, hold.HoldID,
			"counsel@example.test", time.Now().UTC()); err != nil {
			t.Fatalf("release the hold: %v", err)
		}
		if err := exporter.Destroy(ctx, manifests, *stored); err != nil {
			t.Errorf("destruction still refused after the hold was released: %v", err)
		}
	})

	// 7. A failed upload leaves the source intact and records nothing.
	t.Run("a failed upload records nothing", func(t *testing.T) {
		failing := &retention.Exporter{
			Events:    evidenceStore,
			Objects:   refusingStore{err: fmt.Errorf("the bucket is unreachable")},
			Manifests: manifests,
		}
		lost := partition + "_failed"
		if _, err := failing.Export(ctx, tenant, lost, from, to, "ops@example.test"); err == nil {
			t.Fatal("a failed upload reported success")
		}
		if _, err := manifests.Manifest(ctx, tenant, lost); !errors.Is(err, retention.ErrNoManifest) {
			t.Errorf("a manifest exists for an archive that was never written (err=%v); "+
				"a deletion authorized against it would destroy the only copy", err)
		}

		// The evidence is exactly where it was.
		after, err := evidenceStore.ByPeriod(ctx, tenant, from, to)
		if err != nil {
			t.Fatalf("read the period after the failure: %v", err)
		}
		if len(after) != events {
			t.Errorf("the period holds %d events after a failed export, not %d",
				len(after), events)
		}
	})
}
