package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"slices"
	"testing"
	"time"

	"github.com/abhinavxd/libredesk/internal/aiagent"
	authsvc "github.com/abhinavxd/libredesk/internal/auth"
	authmodels "github.com/abhinavxd/libredesk/internal/auth/models"
	"github.com/abhinavxd/libredesk/internal/authz"
	"github.com/abhinavxd/libredesk/internal/testutil"
	"github.com/abhinavxd/libredesk/internal/user"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func TestAssistantKnowledgeAPI(t *testing.T) {
	db := testutil.NewDB(t, "knowledge_api")
	app := newValidatorTestApp(t)
	rd := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { rd.Close() })
	var err error
	app.auth, err = authsvc.New(authsvc.Config{SessionLifetime: time.Hour}, app.i18n, rd, app.lo, nil)
	if err != nil {
		t.Fatal(err)
	}
	app.authz, err = authz.NewEnforcer(app.lo, app.i18n)
	if err != nil {
		t.Fatal(err)
	}
	app.user, err = user.New(app.i18n, user.Opts{DB: db, Lo: app.lo})
	if err != nil {
		t.Fatal(err)
	}
	app.aiAgent, err = aiagent.New(aiagent.Opts{DB: db, Lo: app.lo, I18n: app.i18n}, nil, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	app.fc = initFastCache(rd)
	g := fastglue.NewGlue()
	g.SetContext(app)
	initHandlers(g, nil)
	req := func(method, path string) *fasthttp.RequestCtx {
		ctx := &fasthttp.RequestCtx{}
		ctx.Init(&fasthttp.Request{}, nil, nil)
		ctx.Request.SetRequestURI(testAppBaseURL + path)
		ctx.Request.Header.SetMethod(method)
		return ctx
	}
	session := func(email string, admin bool) string {
		t.Helper()
		var id int
		if err := db.Get(&id, `INSERT INTO users(type,first_name,last_name,email,enabled) VALUES ('agent','Agent','',$1,true) RETURNING id`, email); err != nil {
			t.Fatal(err)
		}
		if admin {
			db.MustExec(`INSERT INTO user_roles(user_id,role_id) SELECT $1,id FROM roles WHERE name='Admin'`, id)
		}
		ctx := req("GET", "/")
		if err := app.auth.SaveSession(authmodels.User{ID: id, Email: email}, &fastglue.Request{RequestCtx: ctx, Context: app}); err != nil {
			t.Fatal(err)
		}
		var cookie fasthttp.Cookie
		cookie.SetKey("libredesk_session")
		if !ctx.Response.Header.Cookie(&cookie) {
			t.Fatal("missing login cookie")
		}
		return string(cookie.Value())
	}
	admin, staff := session("admin@example.test", true), session("agent@example.test", false)
	db.MustExec(`WITH u AS (INSERT INTO users(type,first_name,last_name) VALUES ('ai_assistant','assistant','') RETURNING id)
		INSERT INTO ai_assistants(id,user_id) SELECT 1,id FROM u;
		INSERT INTO ai_knowledge_base(id,content) VALUES (3,'one'),(7,'two')`)
	const base = "/api/v1/ai/assistants"
	const path = base + "/1/knowledge-items"
	call := func(method, path, cookie, ct, body string, want int) []byte {
		t.Helper()
		ctx := req(method, path)
		ctx.Request.Header.SetCookie("csrf_token", "csrf")
		ctx.Request.Header.Set("X-CSRFTOKEN", "csrf")
		if cookie != "" {
			ctx.Request.Header.SetCookie("libredesk_session", cookie)
		}
		ctx.Request.Header.SetContentType(ct)
		ctx.Request.SetBodyString(body)
		g.Handler()(ctx)
		if ctx.Response.StatusCode() != want {
			t.Fatalf("%s %s: got %d want %d, body %s", method, path, ctx.Response.StatusCode(), want, ctx.Response.Body())
		}
		return ctx.Response.Body()
	}
	assertIDs := func(b []byte, want []int) {
		t.Helper()
		var resp struct {
			Data struct {
				IDs []int `json:"knowledge_item_ids"`
			}
		}
		if err := json.Unmarshal(b, &resp); err != nil || resp.Data.IDs == nil || !slices.Equal(resp.Data.IDs, want) {
			t.Fatalf("expected ids %v, got %s: %v", want, b, err)
		}
	}
	const ct = "application/json"
	get := func(want []int) { t.Helper(); assertIDs(call("GET", path, admin, ct, "", 200), want) }
	get([]int{})
	for range 2 {
		assertIDs(call("PUT", path, admin, ct, `{"knowledge_item_ids":[7,3]}`, 200), []int{3, 7})
	}
	for _, tc := range []struct {
		cookie string
		want   int
	}{{"", 401}, {staff, 403}} {
		call("GET", path, tc.cookie, ct, "", tc.want)
		call("PUT", path, tc.cookie, ct, `{"knowledge_item_ids":[]}`, tc.want)
		get([]int{3, 7})
	}
	for _, bad := range []string{`{}`, `{"knowledge_item_ids":null}`, `{"knowledge_item_ids":"3"}`, `{"knowledge_item_ids":[0]}`, `{"knowledge_item_ids":[3,3]}`, `{"knowledge_item_ids":[7,99]}`, `{`} {
		call("PUT", path, admin, ct, bad, 400)
		get([]int{3, 7})
	}
	for _, method := range []string{"GET", "PUT"} {
		for _, id := range []string{"bad", "0", "-1"} {
			call(method, base+"/"+id+"/knowledge-items", admin, ct, `{"knowledge_item_ids":[]}`, 400)
		}
		call(method, base+"/99/knowledge-items", admin, ct, `{"knowledge_item_ids":[]}`, 404)
	}
	profile := func(method, path, payload string) []byte {
		t.Helper()
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		if err := w.WriteField("data", payload); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return call(method, path, admin, w.FormDataContentType(), b.String(), 200)
	}
	for _, payload := range []string{`{"name":"renamed","enabled":true}`, `{"name":"renamed","enabled":true,"knowledge_item_ids":[]}`} {
		profile("PUT", base+"/1", payload)
		get([]int{3, 7})
	}
	var compact struct{ Data []map[string]any }
	if err := json.Unmarshal(call("GET", base+"/compact", staff, ct, "", 200), &compact); err != nil || len(compact.Data) != 1 || len(compact.Data[0]) != 2 || compact.Data[0]["name"] != "renamed" || compact.Data[0]["id"] != float64(1) {
		t.Fatalf("unexpected compact payload: %#v (%v)", compact, err)
	}
	db.MustExec(`SELECT setval(pg_get_serial_sequence('ai_assistants','id'),1)`)
	profile("POST", base, `{"name":"new","enabled":true,"knowledge_item_ids":[3]}`)
	assertIDs(call("GET", base+"/2/knowledge-items", admin, ct, "", 200), []int{})
	assertIDs(call("PUT", path, admin, ct, `{"knowledge_item_ids":[]}`, 200), []int{})
	get([]int{})
}
