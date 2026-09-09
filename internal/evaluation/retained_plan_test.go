package evaluation_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/auditstorage"
)

func TestPublicRetainedPagePlansUseExistingTimeIndexes(t *testing.T) {
	fixture := newRetainedFixture(t, 2, false)
	for _, test := range []struct{ name, index string }{
		{"audit", "audit_time_idx"}, {"intake", "intake_time_idx"}, {"evaluation", "evaluation_time_idx"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var parts []string
			for _, name := range []string{"query_summary.sql", "query_keyset.sql", "query_page.sql"} {
				body, err := os.ReadFile(filepath.Join("..", test.name, name))
				if err != nil {
					t.Fatal(err)
				}
				parts = append(parts, string(body))
			}
			query := parts[0] + " where " + parts[1] + parts[2]
			cursor := "row-001"
			if test.name == "intake" {
				cursor = "2"
			}
			rows, err := fixture.handles[0].Database.QueryContext(t.Context(), "explain query plan "+query, auditstorage.FormatTime(fixture.base), cursor)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			var plan []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				plan = append(plan, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(plan, "\n")
			t.Log(joined)
			if !strings.Contains(joined, test.index) || strings.Contains(joined, "TEMP B-TREE") {
				t.Fatalf("keyset query does not use its ordered index: %s", joined)
			}
		})
	}
}
