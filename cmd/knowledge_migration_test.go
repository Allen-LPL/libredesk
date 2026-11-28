package main

import (
	"testing"

	"github.com/abhinavxd/libredesk/internal/testutil"
)

func TestKnowledgeMigrationRegistered(t *testing.T) {
	db := testutil.NewDB(t, "knowledge_migration_registration")
	if err := recordMigrationVersion("v2.10.0-rc.3", db); err != nil {
		t.Fatal(err)
	}
	_, pending, err := getPendingMigrations(db)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range pending {
		if m.version == "v2.10.0-rc.4" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected one pending knowledge migration, got %d", n)
	}
}
