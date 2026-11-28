package aiagent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/abhinavxd/libredesk/internal/ai"
	aimodels "github.com/abhinavxd/libredesk/internal/ai/models"
	"github.com/abhinavxd/libredesk/internal/conversation"
	"github.com/abhinavxd/libredesk/internal/inbox"
	"github.com/abhinavxd/libredesk/internal/inbox/channel/livechat"
	"github.com/abhinavxd/libredesk/internal/media"
	"github.com/abhinavxd/libredesk/internal/migrations"
	"github.com/abhinavxd/libredesk/internal/setting"
	"github.com/abhinavxd/libredesk/internal/testutil"
	"github.com/abhinavxd/libredesk/internal/user"
	"github.com/abhinavxd/libredesk/internal/webhook"
	"github.com/abhinavxd/libredesk/internal/ws"
	"github.com/google/uuid"
	"github.com/zerodha/logf"
)

func TestKnowledgeUpgradeIsolation(t *testing.T) {
	for _, mode := range []string{"fresh", "upgrade"} {
		t.Run(mode, func(t *testing.T) {
			db := testutil.NewDB(t, "knowledge_install_"+mode)
			ctx := t.Context()
			lo := logf.New(logf.Opts{})
			lang := testutil.NewI18n(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/article" {
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprintf(w, `<html><head><title>Fixture</title></head><body><article><p>%s</p></article></body></html>`, strings.Repeat("Synthetic imported snippet content. ", 10))
					return
				}
				if r.URL.Path != "/embeddings" {
					t.Errorf("unexpected provider path: %s", r.URL.Path)
					w.WriteHeader(404)
					return
				}
				fmt.Fprint(w, `{"data":[{"embedding":[1,0],"index":0}]}`)
			}))
			defer srv.Close()
			db.MustExec(`UPDATE ai_providers SET config=jsonb_build_object('base_url',$1::text,'api_key','fixture') WHERE type='embedding'`, srv.URL)
			if mode == "upgrade" {
				db.MustExec(`DROP TABLE ai_assistant_knowledge_items`)
			}
			db.MustExec(`INSERT INTO users(id,type,first_name,last_name) VALUES (10,'ai_assistant','One',''),(11,'ai_assistant','Two','');
				INSERT INTO ai_assistants(id,user_id) VALUES(1,10),(2,11);
				INSERT INTO ai_knowledge_base(title,content,source) VALUES ('A','legacy manual','manual'),('B','legacy URL','url'),('C','legacy FAQ','conversation');
				INSERT INTO embeddings(source_type,source_id,chunk_text,embedding,dimensions) SELECT 'snippet',id,content,decode('0000803f00000000','hex'),2 FROM ai_knowledge_base`)
			snapshot := func() []string {
				t.Helper()
				var rows []string
				if err := db.Select(&rows, `SELECT concat_ws('|',i.id,i.source,i.title,i.content,e.id,e.chunk_text,encode(e.embedding,'hex')) FROM ai_knowledge_base i JOIN embeddings e ON e.source_type='snippet' AND e.source_id=i.id ORDER BY i.id,e.id`); err != nil {
					t.Fatal(err)
				}
				return rows
			}
			before := snapshot()
			if len(before) != 3 {
				t.Fatal("missing legacy fixture")
			}
			opts := ai.Opts{DB: db, Ctx: ctx, Lo: &lo, I18n: lang}
			if mode == "upgrade" {
				if _, err := ai.New(opts); err == nil {
					t.Fatal("AI manager started without migration")
				}
				if err := migrations.V2_10_0_RC4(db, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			if !slices.Equal(before, snapshot()) {
				t.Fatal("migration changed existing snippets or vectors")
			}
			engine, err := ai.New(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			store, err := New(Opts{DB: db, Lo: &lo, I18n: lang}, engine, nil, nil, nil, nil, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertUnbound := func() {
				t.Helper()
				var count int
				if err := db.Get(&count, `SELECT count(*) FROM ai_assistant_knowledge_items`); err != nil || count != 0 {
					t.Fatalf("implicit grants: %d (%v)", count, err)
				}
				for _, id := range []int{1, 2} {
					hits, err := engine.SearchAssistantKnowledge(ctx, id, "neutral", 5)
					if err != nil || len(hits) != 0 {
						t.Fatalf("unbound assistant %d retrieved %v (%v)", id, hits, err)
					}
				}
			}
			assertUnbound()
			if _, err := engine.CreateKnowledgeBaseItem("Manual", "Synthetic manual snippet", aimodels.KnowledgeSourceManual, "", true); err != nil {
				t.Fatal(err)
			}
			if _, err := engine.ImportKnowledgeBaseFromURL(ctx, srv.URL+"/article"); err != nil {
				t.Fatal(err)
			}
			db.MustExec(`INSERT INTO inboxes(id,name,channel) VALUES(1,'Fixture','livechat');
				INSERT INTO conversations(id,inbox_id,contact_id,status_id) SELECT 1,1,10,id FROM conversation_statuses WHERE category='open' LIMIT 1;
				INSERT INTO ai_faq_suggestions(id,conversation_id,question,answer) VALUES(1,1,'FAQ?','Synthetic FAQ snippet')`)
			if err := store.ApproveFAQSuggestion(1, "", "", 11); err != nil {
				t.Fatal(err)
			}
			engine.Close() // wait until the async create/import/approval jobs have finished
			assertUnbound()
			if err := store.ReplaceKnowledgeItemIDs(ctx, 1, []int{1}, 11); err != nil {
				t.Fatal(err)
			}
			for _, id := range []int{1, 2} {
				hits, err := engine.SearchAssistantKnowledge(ctx, id, "neutral", 5)
				if err != nil || id == 2 && len(hits) != 0 || id == 1 && (len(hits) != 1 || hits[0].ChunkText != "legacy manual") {
					t.Fatalf("scoped result: %v %v", hits, err)
				}
			}
			db.MustExec(`ALTER TABLE ai_assistant_knowledge_items DROP COLUMN knowledge_item_id CASCADE`)
			if hits, err := engine.SearchAssistantKnowledge(ctx, 1, "neutral", 5); err == nil || len(hits) != 0 {
				t.Fatal("broken grant lookup did not fail closed")
			}
		})
	}
}

func TestAssistantKnowledgeCanary(t *testing.T) {
	db := testutil.NewDB(t, "knowledge_canary")
	marker := "allen-test-" + uuid.NewString()
	toolMessages := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			fmt.Fprint(w, `{"data":[{"embedding":[1,0],"index":0}]}`)
			return
		}
		var req aimodels.ChatCompletionPayload
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) == 0 {
			t.Error("invalid completion request")
			w.WriteHeader(400)
			return
		}
		last := req.Messages[len(req.Messages)-1]
		if last.Role != "tool" {
			fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"s","type":"function","function":{"name":"search_knowledge_base","arguments":"{\"query\":\"neutral\",\"assistant_id\":1}"}}]}}]}`)
			return
		}
		toolMessages <- last.Content
		// Echo actual tool data; never manufacture a positive control in the provider.
		reply, _ := json.Marshal(last.Content + " [[cite:51]]")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}]}`, reply)
	}))
	defer srv.Close()
	db.MustExec(`UPDATE ai_providers SET config=jsonb_build_object('base_url',$1::text,'api_key','canary-test')`, srv.URL)
	db.MustExec(`INSERT INTO users(id,type,first_name,last_name) VALUES(10,'ai_assistant','Allen',''),(11,'ai_assistant','mydesign','');
		INSERT INTO ai_assistants(id,user_id,citations_enabled) VALUES (1,10,true),(2,11,true);
		INSERT INTO inboxes(id,name,channel,enabled,"from") VALUES (31,'Fixture','livechat',true,'Fixture');
		INSERT INTO ai_knowledge_base(id,title,content) VALUES (52,'Design title','design control');
		INSERT INTO help_centers(id,name,slug) VALUES (1,'Fixture','fixture');
		INSERT INTO article_collections(id,help_center_id,name,slug,is_published) VALUES(1,1,'Docs','docs',true);
		INSERT INTO help_articles(id,collection_id,title,slug,status,ai_enabled) VALUES(51,1,'Forbidden reference','ref','published',true);
		INSERT INTO embeddings(source_type,source_id,chunk_text,embedding,dimensions) VALUES
		('snippet',52,'design control',decode('cdcc4c3f9a99193f','hex'),2),('help_article',51,'Forbidden reference',decode('0000803f00000000','hex'),2)`)
	db.MustExec(`INSERT INTO ai_knowledge_base(id,title,content) VALUES (51,'Allen title',$1)`, marker)
	db.MustExec(`INSERT INTO embeddings(source_type,source_id,chunk_text,embedding,dimensions)
		SELECT 'snippet',51,content,decode('0000803f00000000','hex'),2 FROM ai_knowledge_base CROSS JOIN generate_series(1,$1) WHERE id=51`, searchResultLimit)
	db.MustExec(`INSERT INTO ai_assistant_knowledge_items VALUES (1,51),(2,52)`)
	lo := logf.New(logf.Opts{})
	lang := testutil.NewI18n(t)
	engine, err := ai.New(ai.Opts{DB: db, Lo: &lo, I18n: lang, Ctx: t.Context()})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	inboxes, err := inbox.New(&lo, db, lang, "")
	if err != nil {
		t.Fatal(err)
	}
	med, err := media.New(media.Opts{DB: db, Lo: &lo, I18n: lang})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := setting.New(setting.Opts{DB: db, Lo: &lo})
	if err != nil {
		t.Fatal(err)
	}
	hooks, err := webhook.New(webhook.Opts{DB: db, Lo: &lo, I18n: lang, QueueSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	users, err := user.New(lang, user.Opts{DB: db, Lo: &lo})
	if err != nil {
		t.Fatal(err)
	}
	convos, err := conversation.New(ws.NewHub(&lo, users), lang, nil, nil, nil, inboxes, users, nil, med, settings, nil, nil, nil, hooks, nil, conversation.Opts{DB: db, Lo: &lo})
	if err != nil {
		t.Fatal(err)
	}
	chat, err := livechat.New(convos, users, livechat.Opts{ID: 31, Name: "Fixture", Lo: &lo})
	if err != nil {
		t.Fatal(err)
	}
	inboxes.Register(chat)
	m, err := New(Opts{DB: db, Lo: &lo, I18n: lang, MaxSteps: 4, MaxHistoryMessages: 20}, engine, convos, inboxes, med, settings, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T, surface string, id int, want string, sourceID int) {
		t.Helper()
		var reply string
		if surface == "preview" {
			answer, sources, err := m.PreviewReply(t.Context(), id, "neutral")
			if err != nil {
				t.Fatal(err)
			}
			reply = answer
			title := "Design title"
			if id == 1 {
				title = "Allen title"
			}
			if sourceID == 0 && len(sources) != 0 || sourceID != 0 && (len(sources) != 1 || sources[0].Type != "snippet" || sources[0].ID != sourceID || sources[0].Title != title || sources[0].URL != "") {
				t.Errorf("unauthorized or missing preview metadata: %v", sources)
			}
		} else {
			var contact, conv int
			if err := db.Get(&contact, `INSERT INTO users(type,first_name,last_name) VALUES ('contact','Customer','') RETURNING id`); err != nil {
				t.Fatal(err)
			}
			if err := db.Get(&conv, `INSERT INTO conversations(inbox_id,contact_id,assigned_user_id,status_id) SELECT 31,$1,user_id,(SELECT id FROM conversation_statuses WHERE category='open' LIMIT 1) FROM ai_assistants WHERE id=$2 RETURNING id`, contact, id); err != nil {
				t.Fatal(err)
			}
			db.MustExec(`INSERT INTO conversation_messages(conversation_id,type,status,sender_id,sender_type,content,text_content,content_type) VALUES ($1,'incoming','received',$2,'contact','neutral','neutral','text')`, conv, contact)
			m.handle(t.Context(), conv)
			var actor, sender int
			var citations bool
			if err := db.QueryRow(`SELECT content,(meta->>'ai_assistant_id')::int,sender_id,meta ? 'ai_article_ids' FROM conversation_messages WHERE conversation_id=$1 AND type='outgoing' AND NOT private`, conv).Scan(&reply, &actor, &sender, &citations); err != nil {
				t.Fatal(err)
			}
			if actor != id || sender != id+9 || citations {
				t.Fatal("wrong live sender or citation metadata")
			}
		}
		select {
		case tool := <-toolMessages:
			for _, text := range []string{tool, reply} {
				if id != 1 && strings.Contains(text, marker) {
					t.Error("forbidden Allen marker")
				}
				if !strings.Contains(text, want) || strings.Contains(text, "Forbidden reference") || strings.Contains(text, "[[cite:") {
					t.Error("missing positive control or leaked reference")
				}
			}
		default:
			t.Fatal("provider never received tool output")
		}
	}
	for _, surface := range []string{"preview", "live"} {
		t.Run(surface+"/allen", func(t *testing.T) { check(t, surface, 1, marker, 51) })
		t.Run(surface+"/mydesign", func(t *testing.T) { check(t, surface, 2, "design control", 52) })
	}
	db.MustExec(`DELETE FROM ai_assistant_knowledge_items WHERE assistant_id=2`)
	for _, surface := range []string{"preview", "live"} {
		t.Run(surface+"/revoked", func(t *testing.T) { check(t, surface, 2, "No relevant information", 0) })
	}
}
