package identity

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Workloads maps a verified SPIFFE ID to the tenant it acts for.
//
// A2 has been buildable and unusable: a workload certificate proves which workload is
// calling, the platform had no way to say which customer that workload belongs to, and
// RequireTenant refused rather than trusting the request. That was the right refusal
// and it left the level unreachable.
//
// A mapping rather than a convention. The SPIFFE IDs SPIRE issues here look like
// spiffe://acme.example/ns/agents/sa/momentum — a namespace and a service account, and
// nothing in that names a tenant. Deriving one would mean inventing a convention and
// then assigning customers by it silently, which is the same mistake as reading the
// tenant off the request: a value that decides scope, chosen by whoever writes the
// path.
type Workloads struct {
	// exact maps a full SPIFFE ID to a tenant.
	exact map[string]string

	// prefixes are entries ending in "/", longest first.
	prefixes []workloadPrefix
}

type workloadPrefix struct {
	path   string
	tenant string
}

// ParseWorkloads reads "spiffe://domain/path=tenant,spiffe://domain/path/=tenant".
//
// An entry whose path ends in "/" matches every workload beneath it. The trailing
// slash is required rather than implied, because "spiffe://td/ns/prod" as a prefix
// would also match "spiffe://td/ns/production" — a different namespace, a different
// customer, and a bug that looks like nothing until the day someone registers the
// longer name.
//
// Ambiguity is refused at startup. Two entries for the same path, or a prefix that is
// also an exact entry, are a configuration error rather than something to resolve by
// map iteration order at the moment an order is being placed.
func ParseWorkloads(raw string) (*Workloads, error) {
	w := &Workloads{exact: map[string]string{}}
	seen := map[string]bool{}

	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		id, tenant, ok := strings.Cut(entry, "=")
		id, tenant = strings.TrimSpace(id), strings.TrimSpace(tenant)
		if !ok || id == "" || tenant == "" {
			return nil, errors.New("malformed workload entry; expected spiffe://domain/path=tenant")
		}
		if !identifierShaped(tenant) {
			return nil, fmt.Errorf("workload entry %q: tenant must be identifier-shaped", entry)
		}
		if !strings.HasPrefix(id, "spiffe://") {
			return nil, fmt.Errorf("workload entry %q: %q is not a SPIFFE ID", entry, id)
		}
		if _, err := ParseSPIFFEID(strings.TrimSuffix(id, "/")); err != nil {
			// This is also what refuses a whole-trust-domain entry like
			// "spiffe://acme.example/", and that refusal is deliberate. A trust domain
			// is not a tenant: a catch-all assigns every workload SPIRE ever issues in
			// it to one customer, including the ones added later by someone who never
			// read this file. A deployment with one customer writes its namespace,
			// which is one entry and says what it covers.
			return nil, fmt.Errorf("workload entry %q: %w. A prefix needs at least one "+
				"path segment; a whole trust domain is not a tenant", entry, err)
		}
		if seen[id] {
			return nil, fmt.Errorf("workload %q is mapped twice; which tenant it belongs "+
				"to would depend on iteration order", id)
		}
		seen[id] = true

		if strings.HasSuffix(id, "/") {
			w.prefixes = append(w.prefixes, workloadPrefix{path: id, tenant: tenant})
			continue
		}
		w.exact[id] = tenant
	}

	if len(w.exact) == 0 && len(w.prefixes) == 0 {
		return nil, errors.New("no workloads configured")
	}

	// Longest first, so the most specific prefix decides. Sorted once here rather than
	// searched every request: this is on the hot path.
	sort.Slice(w.prefixes, func(i, j int) bool {
		return len(w.prefixes[i].path) > len(w.prefixes[j].path)
	})

	return w, nil
}

// TenantFor returns the tenant a verified workload acts for.
//
// Exact entries win over prefixes, and a longer prefix wins over a shorter one. A
// workload with no entry gets nothing: it is a registered workload nobody has assigned
// to a customer, and guessing would assign one.
func (w *Workloads) TenantFor(id SPIFFEID) (string, bool) {
	if w == nil || id.IsZero() {
		return "", false
	}

	full := id.String()
	if tenant, ok := w.exact[full]; ok {
		return tenant, true
	}
	for _, p := range w.prefixes {
		if strings.HasPrefix(full+"/", p.path) {
			return p.tenant, true
		}
	}
	return "", false
}
