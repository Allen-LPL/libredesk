package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/abhinavxd/libredesk/internal/testutil"
	"github.com/jmoiron/sqlx"
	"github.com/zerodha/logf"
)

func knowledgeTestManager(t *testing.T, db *sqlx.DB) *Manager {
	t.Helper()
	lo := logf.New(logf.Opts{})
	m, err := New(Opts{DB: db, Ctx: t.Context(), Lo: &lo, I18n: testutil.NewI18n(t)})
	if err != nil {
		t.Fatal(err)
	}
	<-m.indexReady
	t.Cleanup(m.Close)
	return m
}

func TestSearchAssistantKnowledge(t *testing.T) {
	db := testutil.NewDB(t, "assistant_knowledge")
	var calls atomic.Int32
	var reject atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if reject.Load() {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}]}`)
	}))
	defer srv.Close()
	db.MustExec(`UPDATE ai_providers SET config=jsonb_build_object('base_url',$1::text,'api_key','test-only') WHERE type='embedding'`, srv.URL)
	db.MustExec(`INSERT INTO users(id,type,first_name,last_name,enabled) VALUES (10,'ai_assistant','Allen','',true),(11,'ai_assistant','Design','',true);
		INSERT INTO ai_assistants(id,user_id) VALUES (1,10),(2,11);
		INSERT INTO ai_knowledge_base(id,content,enabled) VALUES (5,'private',true),(6,'design control',true),(7,'disabled',false);
		INSERT INTO ai_assistant_knowledge_items VALUES (1,5),(2,6),(2,7)`)
	insert := func(kind string, id int, text string, vec []byte, dim int) {
		db.MustExec(`INSERT INTO embeddings(source_type,source_id,chunk_text,embedding,dimensions) VALUES ($1,$2,$3,$4,$5)`, kind, id, text, vec, dim)
	}
	top := serializeEmbedding([]float32{1, 0})
	insert("snippet", 5, "private", top, 2)
	insert("snippet", 6, "design control", serializeEmbedding([]float32{0.8, 0.6}), 2)
	insert("snippet", 7, "disabled", top, 2)
	insert("snippet", 99, "orphan", top, 2)
	insert("help_article", 6, "article", top, 2)
	insert("tag", 6, "tag", top, 2)
	insert("snippet", 6, "trailing byte", append(append([]byte(nil), top...), 0), 2)
	insert("snippet", 6, "invalid dimensions", top, 3)
	insert("snippet", 6, "zero", make([]byte, 8), 2)
	m := knowledgeTestManager(t, db)
	check := func(m *Manager, id int, want string) {
		t.Helper()
		before := calls.Load()
		hits, err := m.SearchAssistantKnowledge(t.Context(), id, "neutral", 1)
		if err != nil {
			t.Fatal(err)
		}
		if want == "" {
			if len(hits) != 0 || calls.Load() != before {
				t.Fatalf("expected empty denied search: %v calls=%d", hits, calls.Load()-before)
			}
		} else if len(hits) != 1 || hits[0].SourceType != "snippet" || hits[0].ChunkText != want || calls.Load() != before+1 {
			t.Fatalf("unexpected hits for %d: %v", id, hits)
		}
	}
	check(m, 1, "private")
	check(m, 2, "design control")
	check(m, 11, "") // Paired user ID is not the assistant primary key.
	for _, pair := range [][2]int{{0, 1}, {2, 0}, {2, 1000}} {
		if hits, err := m.SearchAssistantKnowledge(t.Context(), pair[0], "neutral", pair[1]); err == nil || len(hits) != 0 {
			t.Fatal("invalid search accepted")
		}
	}
	db.MustExec(`UPDATE ai_assistants SET enabled=false WHERE id=2`)
	check(m, 2, "")
	db.MustExec(`UPDATE ai_assistants SET enabled=true WHERE id=2; UPDATE users SET deleted_at=now() WHERE id=11`)
	check(m, 2, "")
	db.MustExec(`UPDATE users SET deleted_at=NULL WHERE id=11; UPDATE ai_knowledge_base SET enabled=false WHERE id=6`)
	check(m, 2, "")
	db.MustExec(`INSERT INTO ai_assistant_knowledge_items VALUES (2,5)`)
	other := knowledgeTestManager(t, db)
	check(other, 2, "private")
	db.MustExec(`DELETE FROM ai_assistant_knowledge_items WHERE assistant_id=2`)
	check(other, 2, "")
	check(knowledgeTestManager(t, db), 2, "")
	db.MustExec(`DELETE FROM ai_knowledge_base WHERE id=6`)
	check(m, 2, "")
	badCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if hits, err := m.SearchAssistantKnowledge(badCtx, 1, "neutral", 1); err == nil || len(hits) != 0 {
		t.Fatal("canceled lookup returned content or no error")
	}
	reject.Store(true)
	if hits, err := m.SearchAssistantKnowledge(t.Context(), 1, "neutral", 1); err == nil || len(hits) != 0 {
		t.Fatal("provider failure returned content or no error")
	}
}
