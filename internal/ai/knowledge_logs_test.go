package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/abhinavxd/libredesk/internal/ai/models"
	"github.com/abhinavxd/libredesk/internal/testutil"
	"github.com/zerodha/logf"
)

func TestKnowledgeLogsProvider(t *testing.T) {
	db := testutil.NewDB(t, "knowledge_log_provider")
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(401)
			fmt.Fprint(w, "private-body private-key")
			return
		}
		if r.URL.Path == "/tool" {
			fmt.Fprint(w, "private-chunk private-title")
			return
		}
		var req models.ChatCompletionPayload
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Messages[len(req.Messages)-1].Role == "tool" {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"private-answer"}}]}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"s","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"private-query\"}"}}]}}]}`)
	}))
	defer srv.Close()
	db.MustExec(`UPDATE ai_providers SET config=jsonb_build_object('base_url',$1::text,'api_key','private-key')`, srv.URL)
	db.MustExec(`INSERT INTO ai_tools(id,name,url,method,requires_verification) VALUES(1,'lookup',$1,'GET',false)`, srv.URL+"/tool")
	var logs bytes.Buffer
	lo := logf.New(logf.Opts{Writer: &logs, Level: logf.DebugLevel})
	m, err := New(Opts{DB: db, Lo: &lo, I18n: testutil.NewI18n(t), Ctx: t.Context()})
	if err != nil {
		t.Fatal(err)
	}
	<-m.indexReady
	defer m.Close()
	for _, steps := range []int{1, 3} {
		got, err := m.RunAgentWithTools(t.Context(), "private-prompt", []models.ChatMessage{{Role: "user", Content: "hello"}}, steps, ToolContext{}, []int{1}, false, nil)
		if err != nil || got != "private-answer" {
			t.Fatal("expected completed run", err)
		}
	}
	fail.Store(true)
	if _, err := m.GetEmbeddings(t.Context(), "private-query"); err == nil {
		t.Fatal("expected provider error")
	}
	m.providerError(fmt.Errorf("private-body %w", ErrProviderUnavailable))
	for _, s := range []string{"private-prompt", "private-query", "private-chunk", "private-title", "private-answer", "private-key", "private-body", srv.URL} {
		if strings.Contains(logs.String(), s) {
			t.Errorf("unsafe log contains test sentinel %q", s)
		}
	}
}
