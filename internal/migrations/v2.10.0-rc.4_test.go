package migrations

import (
	"slices"
	"testing"

	"github.com/abhinavxd/libredesk/internal/testutil"
)

func TestKnowledgeMigrationSchema(t *testing.T) {
	db := testutil.NewDB(t, "knowledge_migration")
	constraints := func() []string {
		t.Helper()
		var defs []string
		if err := db.Select(&defs, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid='ai_assistant_knowledge_items'::regclass AND contype IN ('p','f') ORDER BY 1`); err != nil {
			t.Fatal(err)
		}
		var reverse string
		if err := db.Get(&reverse, `SELECT pg_get_indexdef(indexrelid,1,true)||','||pg_get_indexdef(indexrelid,2,true) FROM pg_index WHERE indexrelid='index_ai_assistant_knowledge_items_on_item'::regclass`); err != nil || reverse != "knowledge_item_id,assistant_id" {
			t.Fatalf("bad reverse index: %q %v", reverse, err)
		}
		return defs
	}
	want := []string{"FOREIGN KEY (assistant_id) REFERENCES ai_assistants(id) ON DELETE CASCADE", "FOREIGN KEY (knowledge_item_id) REFERENCES ai_knowledge_base(id) ON DELETE CASCADE", "PRIMARY KEY (assistant_id, knowledge_item_id)"}
	if got := constraints(); !slices.Equal(got, want) {
		t.Fatalf("fresh constraints %v", got)
	}
	db.MustExec(`DROP TABLE ai_assistant_knowledge_items;
		WITH u AS (INSERT INTO users(type,first_name,last_name) VALUES ('ai_assistant','one',''),('ai_assistant','two','') RETURNING id)
		INSERT INTO ai_assistants(user_id) SELECT id FROM u;
		INSERT INTO ai_knowledge_base(content) VALUES ('legacy');
		INSERT INTO embeddings(source_type,source_id,chunk_text,embedding) SELECT 'snippet',id,content,decode('abcd','hex') FROM ai_knowledge_base`)
	for range 2 {
		if err := V2_10_0_RC4(db, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := constraints(); !slices.Equal(got, want) {
		t.Fatalf("upgrade constraints %v", got)
	}
	count := func(want int) {
		t.Helper()
		var n int
		if err := db.Get(&n, `SELECT count(*) FROM ai_assistant_knowledge_items`); err != nil || n != want {
			t.Fatalf("grants %d want %d: %v", n, want, err)
		}
	}
	count(0)
	var bytes string
	if err := db.Get(&bytes, `SELECT encode(embedding,'hex') FROM embeddings`); err != nil || bytes != "abcd" {
		t.Fatalf("legacy vector %s %v", bytes, err)
	}
	const share = `INSERT INTO ai_assistant_knowledge_items SELECT a.id,i.id FROM ai_assistants a CROSS JOIN ai_knowledge_base i`
	db.MustExec(share)
	count(2)
	if _, err := db.Exec(share); err == nil {
		t.Fatal("accepted duplicate pair")
	}
	db.MustExec(`DELETE FROM ai_assistants WHERE id=(SELECT min(id) FROM ai_assistants)`)
	count(1)
	db.MustExec(`DELETE FROM ai_knowledge_base`)
	count(0)
}
