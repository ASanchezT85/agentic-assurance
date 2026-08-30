//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A-6-01: every statement the application issues has the privilege to run.
//
// `PostgresUsage.Release` is a DELETE on authority_usage. assurance_app was never granted
// DELETE on that table, so every release since the table existed failed with "permission
// denied" and capacity reserved for orders that were never sent stayed held for ever.
//
// Nothing caught it. The migrations grant privileges one table at a time and the code
// issues statements one file at a time, and the two lists were never compared. The unit
// tests use in-memory stores, and no integration test exercised Release at all.
//
// This reads the statements out of the source and asks the database whether the role may
// run them. It is deliberately mechanical: a written list of "tables we think we write to"
// is the same guess that produced the gap.
func TestTheApplicationRoleMayRunTheStatementsItIssues(t *testing.T) {
	ctx := context.Background()
	pool := idemPool(t)

	needed := statementsInSource(t)
	if len(needed) < 8 {
		t.Fatalf("found only %d table/privilege pairs in the source; the extraction is "+
			"wrong and this guard would pass by seeing nothing", len(needed))
	}

	var missing []string
	for _, pair := range sortedPairs(needed) {
		var allowed bool
		err := pool.QueryRow(ctx,
			`SELECT has_table_privilege('assurance_app', $1, $2)`,
			pair.table, pair.privilege).Scan(&allowed)
		if err != nil {
			// A table the application names and the database does not have is a
			// different fault, and one the migration tests would catch; skip rather than
			// fail this guard on it.
			t.Logf("skipping %s: %v", pair.table, err)
			continue
		}
		if !allowed {
			missing = append(missing, fmt.Sprintf("%s on %s (%s)",
				pair.privilege, pair.table, needed[pair]))
		}
	}

	if len(missing) > 0 {
		t.Fatalf("the application issues statements it may not run:\n  %s\n\n"+
			"A denied statement fails at the moment it matters and is invisible until "+
			"then: the release path had been failing since the table existed, holding "+
			"capacity for orders nobody sent, and the platform's own evidence said so 56 "+
			"times before anybody read it.", strings.Join(missing, "\n  "))
	}
}

type tablePrivilege struct{ table, privilege string }

// statementsInSource extracts (table, privilege) pairs from the DML in internal/.
func statementsInSource(t *testing.T) map[tablePrivilege]string {
	t.Helper()

	patterns := []struct {
		privilege string
		re        *regexp.Regexp
	}{
		{"INSERT", regexp.MustCompile(`(?i)INSERT\s+INTO\s+([a-z_][a-z0-9_]*)`)},
		{"UPDATE", regexp.MustCompile(`(?i)UPDATE\s+([a-z_][a-z0-9_]*)\s`)},
		{"DELETE", regexp.MustCompile(`(?i)DELETE\s+FROM\s+([a-z_][a-z0-9_]*)`)},
		{"SELECT", regexp.MustCompile(`(?i)\bFROM\s+([a-z_][a-z0-9_]*)`)},
	}

	// Words these patterns pick up that are not tables.
	notTables := map[string]bool{
		"select": true, "where": true, "set": true, "values": true, "only": true,
		"doomed": true, "ordered": true, "retired": true, "unnest": true,
	}

	found := map[tablePrivilege]string{}
	root := filepath.Join(repoRootFromTest(t), "internal")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return err
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		body := string(raw)
		rel, _ := filepath.Rel(repoRootFromTest(t), path)
		for _, p := range patterns {
			for _, m := range p.re.FindAllStringSubmatch(body, -1) {
				name := strings.ToLower(m[1])
				if notTables[name] || !strings.Contains(name, "_") {
					continue
				}
				key := tablePrivilege{table: name, privilege: p.privilege}
				if _, seen := found[key]; !seen {
					found[key] = rel
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return found
}

func sortedPairs(m map[tablePrivilege]string) []tablePrivilege {
	out := make([]tablePrivilege, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].table != out[j].table {
			return out[i].table < out[j].table
		}
		return out[i].privilege < out[j].privilege
	})
	return out
}
