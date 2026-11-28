package aiagent

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/abhinavxd/libredesk/internal/envelope"
	"github.com/abhinavxd/libredesk/internal/testutil"
	"github.com/zerodha/logf"
)

func TestKnowledgeItemsReplace(t *testing.T) {
	db := testutil.NewDB(t, "knowledge_replace")
	lo := logf.New(logf.Opts{})
	m, err := New(Opts{DB: db, I18n: testutil.NewI18n(t), Lo: &lo}, nil, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	db.MustExec(`WITH u AS (INSERT INTO users(type,first_name,last_name) VALUES ('ai_assistant','one',''),('ai_assistant','two','') RETURNING id)
		INSERT INTO ai_assistants(id,user_id) SELECT row_number() OVER (ORDER BY id),id FROM u;
		INSERT INTO ai_knowledge_base(id,content,enabled) VALUES (3,'first',true),(7,'second',false),(8,'third',true)`)
	ctx := t.Context()
	replace := func(ids ...int) error { return m.ReplaceKnowledgeItemIDs(ctx, 1, ids, 42) }
	assertSet := func(a int, want []int) {
		t.Helper()
		got, err := m.GetKnowledgeItemIDs(ctx, a)
		if err != nil || got == nil || !slices.Equal(got, want) {
			t.Fatalf("assistant %d got %v want %v: %v", a, got, want, err)
		}
	}
	checkType := func(err error, want string) {
		t.Helper()
		if e, ok := err.(envelope.Error); !ok || e.ErrorType != want {
			t.Fatalf("error %v want %s", err, want)
		}
	}
	if err := replace(7, 3); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]int{nil, {0}, {-1}, {3, 3}, {3, 99}, {1 << 32}} {
		checkType(replace(bad...), envelope.InputError)
		assertSet(1, []int{3, 7})
	}
	assertSet(2, []int{})
	_, err = m.GetKnowledgeItemIDs(ctx, 99)
	checkType(err, envelope.NotFoundError)
	checkType(m.ReplaceKnowledgeItemIDs(ctx, 99, []int{}, 42), envelope.NotFoundError)

	db.MustExec(`CREATE FUNCTION reject_8() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'simulated failure'; END $$;
		CREATE TRIGGER reject_8 BEFORE INSERT ON ai_assistant_knowledge_items FOR EACH ROW WHEN (NEW.knowledge_item_id=8) EXECUTE FUNCTION reject_8()`)
	if err := replace(3, 8); err == nil {
		t.Fatal("expected trigger failure")
	}
	assertSet(1, []int{3, 7})
	db.MustExec(`DROP TRIGGER reject_8 ON ai_assistant_knowledge_items`)

	// Hold the same parent lock as a replacing writer. An independent GET must
	// see the prior committed grants even after the uncommitted delete.
	tx, err := db.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	tx.MustExec(`SELECT id FROM ai_assistants WHERE id=1 FOR UPDATE`)
	tx.MustExec(`DELETE FROM ai_assistant_knowledge_items WHERE assistant_id=1`)
	assertSet(1, []int{3, 7})
	lockCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if err := m.ReplaceKnowledgeItemIDs(lockCtx, 1, []int{3}, 42); err == nil || lockCtx.Err() == nil {
		t.Fatalf("expected blocked writer, got %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertSet(1, []int{3, 7})
	done := make(chan error, 2)
	for _, ids := range [][]int{{3, 7}, {7, 8}} {
		go func() { done <- replace(ids...) }()
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.GetKnowledgeItemIDs(ctx, 1)
	if err != nil || !(slices.Equal(got, []int{3, 7}) || slices.Equal(got, []int{7, 8})) {
		t.Fatalf("partial replacement: %v %v", got, err)
	}
	if err := replace([]int{}...); err != nil {
		t.Fatal(err)
	}
	assertSet(1, []int{})
}
