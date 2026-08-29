package retention

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"agentic-assurance/internal/evidence"
)

// Export and restore, which is where the retention policy stops being a document.
//
// The package had the policy rules, the manifest shape and the hash chain, and no path
// that moved a month of evidence anywhere or read it back. Every one of those pieces
// passed its own test — the project's recurring defect in its usual form, a component
// whose tests pass while nothing calls it.
//
// The properties that matter here are not "the file arrived":
//
//   - an archive that verifies is one nobody rewrote, and one that does not says so
//     rather than restoring quietly;
//   - a truncated archive is a tampered archive. Dropping the last hundred events
//     leaves every remaining hash valid, so the count is part of what is checked;
//   - a manifest belongs to one archive. Verifying against a different one is how an
//     archive of last month gets accepted as this month's;
//   - a failed upload changes nothing. No manifest, no deletion, and the source stays
//     exactly as it was — an archive nobody can read is worse than no archive when it
//     is what a deletion was authorized against.

// ObjectStore is the archive destination.
//
// Two methods, because that is all an archive needs: written once and read back whole.
// No delete: this package never removes an archive, and a port that cannot express the
// operation cannot perform it by accident.
type ObjectStore interface {
	Put(ctx context.Context, key string, body []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
}

// EventSource is the evidence a partition holds. Satisfied by *evidence.Store.
type EventSource interface {
	ByPeriod(ctx context.Context, tenantID string, from, to time.Time) ([]evidence.Event, error)
}

// ManifestStore records what was exported. Satisfied by the PostgreSQL store.
type ManifestStore interface {
	SaveManifest(ctx context.Context, m Manifest) error
	Manifest(ctx context.Context, tenantID, partition string) (*Manifest, error)
}

// HoldSource answers whether a tenant's evidence is under legal hold.
type HoldSource interface {
	ActiveHolds(ctx context.Context, tenantID string) ([]Hold, error)
}

// Errors an operator has to be able to tell apart.
var (
	// ErrEmptyPeriod is a period with no events. Refused rather than exported: an
	// empty archive verifies perfectly and would authorize the deletion of a partition
	// nobody read.
	ErrEmptyPeriod = errors.New("the period holds no evidence, so there is nothing to archive")

	// ErrTampered is an archive whose content no longer matches its manifest.
	ErrTampered = errors.New("the archive does not match its manifest")

	// ErrTruncated is an archive missing events the manifest counted. Named apart from
	// ErrTampered because the two have different causes — an edit versus a partial
	// write or a partial read — and an operator chasing one should not be told the
	// other.
	ErrTruncated = errors.New("the archive holds fewer events than its manifest counted")

	// ErrHeld is a destruction refused because a legal hold binds.
	ErrHeld = errors.New("a legal hold is active, so nothing may be destroyed")
)

// Exporter moves a period of evidence into an object store and records what it moved.
type Exporter struct {
	Events    EventSource
	Objects   ObjectStore
	Manifests ManifestStore
	Now       func() time.Time
}

func (e *Exporter) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

// ObjectKey is where a period's archive lives.
//
// Tenant first, so an object-store policy can be written per tenant, and the partition
// name after it, so the key says what it holds without opening it.
func ObjectKey(tenantID, partition string) string {
	return fmt.Sprintf("evidence/%s/%s.jsonl", tenantID, partition)
}

// Export writes one period's evidence to the object store and records a manifest.
//
// The order is deliberate: read, hash, upload, and only then record the manifest. A
// manifest is the claim that a readable archive exists, so it is written last — an
// upload that failed leaves no manifest, and a deletion authorized against a manifest
// is therefore authorized against something that was actually stored.
func (e *Exporter) Export(ctx context.Context, tenantID, partition string,
	from, to time.Time, by string) (*Manifest, error) {

	events, err := e.Events.ByPeriod(ctx, tenantID, from, to)
	if err != nil {
		return nil, fmt.Errorf("read the period: %w", err)
	}
	if len(events) == 0 {
		return nil, ErrEmptyPeriod
	}

	body, err := encodeArchive(events)
	if err != nil {
		return nil, err
	}
	head, err := ChainOver(events)
	if err != nil {
		return nil, fmt.Errorf("hash the period: %w", err)
	}

	key := ObjectKey(tenantID, partition)
	if err := e.Objects.Put(ctx, key, body); err != nil {
		// Nothing is recorded and nothing is deleted. The evidence is where it was.
		return nil, fmt.Errorf("upload the archive: %w", err)
	}

	at := e.now()
	m := Manifest{
		TenantID:    tenantID,
		ManifestID:  fmt.Sprintf("man_%s_%s", tenantID, partition),
		Partition:   partition,
		PeriodStart: from.UTC(),
		PeriodEnd:   to.UTC(),
		EventCount:  int64(len(events)),
		ChainHead:   head,
		Destination: key,
		ExportedBy:  by,
		ExportedAt:  at,
	}
	if e.Manifests != nil {
		if err := e.Manifests.SaveManifest(ctx, m); err != nil {
			return nil, fmt.Errorf("record the manifest: %w", err)
		}
	}
	return &m, nil
}

// Restore reads an archive back and proves it is the one the manifest describes.
//
// It returns events only when everything checks. A restore that returned what it could
// and reported the problem separately would put unverified evidence in front of whoever
// asked for it, and the whole point of the chain is that partial trust is not a thing
// an auditor can act on.
func (e *Exporter) Restore(ctx context.Context, m Manifest) ([]evidence.Event, error) {
	body, err := e.Objects.Get(ctx, m.Destination)
	if err != nil {
		return nil, fmt.Errorf("read the archive: %w", err)
	}

	events, err := decodeArchive(body)
	if err != nil {
		// A archive that will not parse is not a lesser archive. It is one nobody can
		// read, which is the state a manifest exists to rule out.
		return nil, fmt.Errorf("%w: %v", ErrTampered, err)
	}

	// Count first. Truncation leaves every remaining hash valid, so a chain check alone
	// would accept an archive with the last thousand events removed.
	if int64(len(events)) != m.EventCount {
		if int64(len(events)) < m.EventCount {
			return nil, fmt.Errorf("%w: %d of %d events", ErrTruncated,
				len(events), m.EventCount)
		}
		return nil, fmt.Errorf("%w: %d events where the manifest counted %d",
			ErrTampered, len(events), m.EventCount)
	}

	if _, err := Verify(events, m.ChainHead); err != nil {
		// Verify says where the chain stopped being true, which is what an operator
		// needs to know: an edit at event 4,000 of 5,000 is a different incident from
		// one at event 2.
		return nil, fmt.Errorf("%w: %v", ErrTampered, err)
	}
	return events, nil
}

// Destroy is the deletion decision, and V0 never performs one.
//
// It answers whether a period may be destroyed and returns the reason either way. The
// destructive half is deliberately absent (ADR: destructive purge stays disabled in V0),
// so this is the gate a future purge has to pass rather than a purge with a flag.
//
// A legal hold beats everything, including an approved authorization. That order is not
// a preference: a hold is a legal instruction about specific evidence and an
// authorization is an internal approval, and a system where the second can override the
// first has no legal hold.
func (e *Exporter) Destroy(ctx context.Context, holds HoldSource, m Manifest) error {
	active, err := holds.ActiveHolds(ctx, m.TenantID)
	if err != nil {
		// Unknown is not "none". A hold lookup that failed leaves the system unable to
		// say whether destruction is permitted, and the safe answer to that is no.
		return fmt.Errorf("%w: the holds on this tenant could not be read: %v", ErrHeld, err)
	}
	for _, h := range active {
		if h.Active() {
			return fmt.Errorf("%w: %s (%s)", ErrHeld, h.HoldID, h.Reason)
		}
	}

	// And the archive has to be readable and intact. Destroying a partition whose
	// archive does not verify destroys the evidence, not a copy of it.
	if _, err := e.Restore(ctx, m); err != nil {
		return fmt.Errorf("the archive does not stand in for the source: %w", err)
	}
	return nil
}

// encodeArchive writes one event per line.
//
// JSON Lines rather than one document: an archive is read back in order and streamed
// when it is large, and a single array makes the last event's integrity depend on
// parsing every earlier one.
func encodeArchive(events []evidence.Event) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			return nil, fmt.Errorf("encode %s: %w", e.EventID, err)
		}
	}
	return buf.Bytes(), nil
}

func decodeArchive(body []byte) ([]evidence.Event, error) {
	var out []evidence.Event
	dec := json.NewDecoder(bytes.NewReader(body))
	for dec.More() {
		var e evidence.Event
		if err := dec.Decode(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}
