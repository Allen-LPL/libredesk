package ai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/abhinavxd/libredesk/internal/ai/models"
	"github.com/abhinavxd/libredesk/internal/testutil"
)

func TestStaffKnowledgeUnavailable(t *testing.T) {
	db := testutil.NewDB(t, "staff_knowledge")
	var embeds atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			embeds.Add(1)
			fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}]}`)
			return
		}
		var req models.ChatCompletionPayload
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) == 0 {
			t.Error("bad completion request", err)
			w.WriteHeader(400)
			return
		}
		names := []string{}
		for _, tool := range req.Tools {
			names = append(names, tool.Function.Name)
		}
		if !slices.Equal(names, []string{"update_balance"}) || strings.Contains(req.Messages[0].Content, "search_articles") {
			t.Errorf("unexpected staff tool set %v or stale prompt", names)
		}
		last := req.Messages[len(req.Messages)-1]
		switch {
		case last.Role != "tool":
			fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"c","type":"function","function":{"name":"update_balance","arguments":"{}"}}]}}]}`)
		case last.ToolCallID == "c":
			fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"a","type":"function","function":{"name":"search_articles","arguments":"{\"query\":\"neutral\"}"}},{"id":"b","type":"function","function":{"name":"search_knowledge_base","arguments":"{\"query\":\"neutral\"}"}}]}}]}`)
		default:
			if last.ToolCallID != "b" || last.Content != "error: unknown tool search_knowledge_base" ||
				req.Messages[len(req.Messages)-2].Content != "error: unknown tool search_articles" {
				t.Errorf("search calls not denied: %s", last.Content)
			}
			fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
		}
	}))
	defer srv.Close()
	db.MustExec(`UPDATE ai_providers SET config=jsonb_build_object('base_url',$1::text,'api_key','test')`, srv.URL)
	db.MustExec(`INSERT INTO ai_tools(id,name,url) VALUES (1,'update_balance','https://unused.invalid'),
		(2,'search_articles','https://unused.invalid'),(3,'search_knowledge_base','https://unused.invalid')`)
	m := knowledgeTestManager(t, db)
	for _, tc := range []struct{ surface, persona, transcript string }{
		{"copilot", "", "current conversation"}, {"copilot", "Allen voice", "current conversation"},
		{"reply", "", ""}, {"reply", "", "current conversation"},
	} {
		scope := AgentRunScope{AgentID: 23, Surface: tc.surface}
		var res models.AgentRunResult
		var err error
		if tc.surface == "copilot" {
			res, err = m.Copilot(t.Context(), tc.transcript, []models.ChatMessage{{Role: "user", Content: "help"}}, ToolContext{}, nil, tc.persona, []int{1, 2, 3}, scope)
		} else {
			res, err = m.GenerateReply(t.Context(), tc.transcript, "help", ToolContext{}, nil, []int{1, 2, 3}, scope)
		}
		if err != nil || res.Approval == nil {
			t.Fatalf("expected custom tool approval: %v %v", res, err)
		}
		res, _, err = m.ResumeAgentRun(t.Context(), res.Approval.RunID, scope.AgentID, false)
		if err != nil || res.Content != "done" {
			t.Fatalf("resume: %v %v", res, err)
		}
	}
	if embeds.Load() != 0 {
		t.Errorf("staff invoked embeddings %d times", embeds.Load())
	}
}

func TestFAQDedup(t *testing.T) {
	db := testutil.NewDB(t, "faq_dedup")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"embedding":[1,0],"index":0}]}`)
	}))
	defer srv.Close()
	db.MustExec(`UPDATE ai_providers SET config=jsonb_build_object('base_url',$1::text,'api_key','test') WHERE type='embedding'`, srv.URL)
	db.MustExec(`INSERT INTO embeddings(source_type,source_id,chunk_text,embedding,dimensions)
		VALUES ('tag',1,'tag',$1,2),('help_article',1,'article',$2,2)`, serializeEmbedding([]float32{1, 0}), serializeEmbedding([]float32{0.8, 0.6}))
	m := knowledgeTestManager(t, db)
	for _, tc := range []struct {
		threshold float64
		want      bool
	}{{0.7, true}, {0.9, false}} {
		got, err := m.HasSimilarFAQ(t.Context(), "neutral", tc.threshold)
		if err != nil || got != tc.want {
			t.Fatalf("threshold %v: %v %v", tc.threshold, got, err)
		}
	}
}
