package store

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every SQL literal in this package must at least parse against SQLite.
// A plain grep for Postgres-only syntax already missed FOR UPDATE SKIP LOCKED
// once; preparing each statement asks the engine instead of guessing.
func TestAllSQLLiteralsPrepare(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	files, _ := filepath.Glob("*.go")
	lit := regexp.MustCompile("(?s)`([^`]*)`")
	sqlStart := regexp.MustCompile(`(?is)^\s*(SELECT|INSERT|UPDATE|DELETE|WITH)\b`)
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range lit.FindAllStringSubmatch(string(src), -1) {
			q := strings.TrimSpace(m[1])
			if !sqlStart.MatchString(q) {
				continue
			}
			// sqlx.In queries carry an IN (?) placeholder expanded at runtime.
			if strings.Contains(q, "IN (?)") {
				q = strings.Replace(q, "IN (?)", "IN (?, ?)", 1)
			}
			checked++
			stmt, err := st.db.PrepareContext(ctx, q)
			if err != nil {
				t.Errorf("%s: %v\n  query: %s", f, err, strings.Join(strings.Fields(q), " "))
				continue
			}
			stmt.Close()
		}
	}
	t.Logf("已验证 %d 条 SQL", checked)
	if checked < 30 {
		t.Errorf("只抽到 %d 条 SQL,提取逻辑可能失效", checked)
	}
}
