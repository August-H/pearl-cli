package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkOpen(b *testing.B) {
	for _, fresh := range []bool{true, false} {
		name := "existing"
		if fresh {
			name = "fresh"
		}
		b.Run(name, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "pearl.db")
			state, err := Open(path)
			if err != nil {
				b.Fatal(err)
			}
			if err := state.Close(); err != nil {
				b.Fatal(err)
			}
			for b.Loop() {
				if fresh {
					b.StopTimer()
					if err := os.Remove(path); err != nil {
						b.Fatal(err)
					}
					b.StartTimer()
				}
				state, err := Open(path)
				if err != nil {
					b.Fatal(err)
				}
				if err := state.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestFailedSchemaMigrationRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// A malformed existing table makes the jobs index fail after earlier schema
	// statements have run. Those statements must not leave a partial migration.
	if _, err := db.Exec("CREATE TABLE jobs (id TEXT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if state, err := Open(path); err == nil {
		state.Close()
		t.Fatal("opened a database with an incompatible jobs table")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = 'resource_limits'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed migration left a partially created schema")
	}
}
