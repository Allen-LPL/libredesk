# Assistant Knowledge Binding Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Return a snippet to an autonomous assistant or its preview only when an administrator explicitly granted that assistant access; expose no snippet search to staff AI surfaces.

**Architecture:** Keep one embedding per existing source chunk and store grants in a PostgreSQL join table. Select authorized chunks on the primary DB for each tool invocation, then rank them in Go. Administer grants separately from assistant profile updates.

**Tech Stack:** Go (`go.mod`), PostgreSQL 17, sqlx/goyesql, fasthttp/fastglue, Vue 3, pnpm, Vitest/jsdom, Go `testing`, `httptest`, and `internal/testutil`.

---

**Scope:** Implement the [approved design](2026-11-28-assistant-knowledge-binding-design.md). No Help Center grants, collections, staff knowledge access, deployment, or CV ingestion. Use synthetic fixtures and local fake providers only. The guarantee concerns what retrieval returns, not text already present in a transcript.

**Run from repository root.** Start an isolated Postgres with `make test-db` or set `LIBREDESK_TEST_DB_DSN` to a disposable instance with database creation privileges. `testutil.NewDB` recreates a named test DB and **skips** if unreachable: give each test a unique DB name; require `--- PASS` for each named DB case, never count `SKIP` or “no tests to run” as green. Install frontend deps with `(cd frontend && npx pnpm install --frozen-lockfile)`. `frontend/package.json` selects pnpm 11.28.8. Do not run against a workspace/customer database.

For each task, add one failing case at a time, run the indicated command, implement the smallest change, and repeat it. Stage **only** the listed changes for each commit. Proposed test/helper names below are new unless an existing test file is cited.

### Task 1: Establish fresh-install and upgrade parity

**Files:** `schema.sql`, `cmd/upgrade.go`; create `internal/migrations/v2.10.0-rc.4.go`, `internal/migrations/v2.10.0-rc.4_test.go`, `cmd/knowledge_migration_test.go`. The current last entry in `migList` is rc.3; if upstream takes rc.4 before implementation, use the next unused semver throughout.

1. **Red:** Follow `internal/migrations/v2.9.0-rc.8_test.go`. Inspect the fresh schema's PK, cascade FKs and reverse index; seed an assistant, snippet and vector, drop **only the join table**, and execute the migration twice as below. In `cmd`, assert `getPendingMigrations` includes rc.4 once when the last recorded version is rc.3.

   ```go
   for range 2 {
       if err := V2_10_0_RC4(db, nil, nil); err != nil { t.Fatal(err) }
   }
   var count int
   if err := db.Get(&count, `SELECT count(*) FROM ai_assistant_knowledge_items`); err != nil || count != 0 {
       t.Fatalf("legacy data gained grants: count=%d err=%v", count, err)
   }
   ```

2. Run `go test -v -count=1 ./internal/migrations ./cmd -run '^TestKnowledgeMigration'`. Expect missing symbol/table or missing registered version.
3. **Green:** Place the fresh table after `ai_assistants`. Copy the approved DDL: `assistant_id INTEGER NOT NULL REFERENCES ai_assistants(id) ON DELETE CASCADE`, `knowledge_item_id INTEGER NOT NULL REFERENCES ai_knowledge_base(id) ON DELETE CASCADE`, `PRIMARY KEY (assistant_id, knowledge_item_id)`, index `index_ai_assistant_knowledge_items_on_item (knowledge_item_id, assistant_id)`. Follow existing fresh-schema drop/create conventions; in the upgrade use `CREATE TABLE/INDEX IF NOT EXISTS` only. Register `{"v2.10.0-rc.4", migrations.V2_10_0_RC4}`. No `INSERT ... SELECT`, embedding rewrite or default owner.
4. **Verify/refactor:** Compare catalog definitions before/after upgrade (index **column order**, both cascades). Exercise duplicate pair rejection, two assistants sharing one item, cascading deletion from each side, and unchanged legacy vector bytes. Rerun step 2: all named tests pass twice on the same upgrade fixture.
5. Commit: `feat(db): add explicit assistant knowledge grants`.

### Task 2: Read and replace one assistant’s grants atomically

**Files:** `internal/aiagent/queries.sql`, `internal/aiagent/aiagent.go` (`queries`); create `internal/aiagent/knowledge_items.go`, `internal/aiagent/knowledge_items_test.go`.

1. **Red:** Build a real manager with `testutil.NewDB`, `testutil.NewI18n`, `logf.New` and `aiagent.New` (package-local `New`); unrelated dependencies can be nil for grant operations. Insert assistant users separately from assistant rows. Write `TestKnowledgeItemsReplace` around this contract (local fixture variables):

   ```go
   ctx := t.Context()
   if err := m.ReplaceKnowledgeItemIDs(ctx, a, []int{second, first}, actor); err != nil { t.Fatal(err) }
   for _, bad := range [][]int{nil, {0}, {-1}, {first, first}, {first, missing}} {
       if err := m.ReplaceKnowledgeItemIDs(ctx, a, bad, actor); err == nil { t.Fatal("accepted invalid set") }
       got, err := m.GetKnowledgeItemIDs(ctx, a)
       if err != nil || !slices.Equal(got, []int{first, second}) { t.Fatalf("changed grants: %v %v", got, err) }
   }
   if err := m.ReplaceKnowledgeItemIDs(ctx, a, []int{}, actor); err != nil { t.Fatal(err) }
   got, err := m.GetKnowledgeItemIDs(ctx, a)
   if err != nil || got == nil || len(got) != 0 { t.Fatalf("want []: %v %v", got, err) }
   ```

2. Run `go test -v -count=1 ./internal/aiagent -run '^TestKnowledgeItems'`: expect undefined methods. Test unknown assistant => not found; disabled snippets remain on the admin list; secondary assistant unaffected.
3. **Green (read):** Add tagged prepared statements like `GetAssistantTools`. In **one** GET query select a sorted `ARRAY(SELECT knowledge_item_id ... ORDER BY knowledge_item_id)` correlated to a live assistant parent. Scan with `pq.Int64Array`, convert to non-nil `[]int`; map `sql.ErrNoRows` to `envelope.NotFoundError`.
4. **Green (replace):** Reject nil, duplicate and nonpositive IDs. `BeginTxx(ctx,nil)`; defer rollback; `SELECT ... FOR UPDATE OF a` on the live assistant before reading old grants. Validate count of `ai_knowledge_base` rows with `type='snippet' AND id=ANY($1::int[])` equals input length (`pq.Array(ids)`); don't require enabled. Use transaction-bound statements for every read/write, delete this assistant’s pairs, bulk insert from `unnest($2::int[])`, commit once. Map invalid IDs to `InputError`; any error rolls back. Keep the old/new ID sets from the locked transaction for the post-commit audit in task 9.
5. **Regression:** Install a test-only trigger that raises an exception on the second replacement item; after failure GET must still return the old set. With independent DB connections, hold the parent lock and use a bounded context to show a writer waits/times out unchanged; release and repeat successfully. Race two overlapping replacements: result equals one complete set, never union/partial. A concurrent reader sees committed sets only. Rerun step 2 with `-race`; expect PASS.
6. Commit: `feat(aiagent): replace assistant knowledge grants transactionally`.

### Task 3: Protect the admin GET/PUT and ordinary profile saves

**Files:** `cmd/handlers.go`, `cmd/aiagent.go`, `i18n/en-US.json` for new messages; create `cmd/aiagent_knowledge_test.go`. Existing contracts to preserve: `internal/aiagent/models/models.go` (`Assistant`), `internal/aiagent/aiagent.go` (`CreateAssistant`, `UpdateAssistant`).

1. **Red:** Register the real routes in the test (`fastglue.NewGlue`, `SetContext`, `initHandlers`, dispatch with router). Mirror session and miniredis setup in `cmd/middlewares_test.go`; seed one authorized agent and one without `ai:manage`. Exercise GET/PUT at `/api/v1/ai/assistants/{id}/knowledge-items`, not just direct handlers. Expect 401 unauthenticated, 403 unauthorized, no mutation on either; an admin sees 200 and sorted `data.knowledge_item_ids`. Assert both valid replacement and idempotent repeat. Bad payload examples:

   ```go
   for _, body := range []string{
       `{}`, `{"knowledge_item_ids":null}`, `{"knowledge_item_ids":"3"}`,
       `{"knowledge_item_ids":[0]}`, `{"knowledge_item_ids":[3,3]}`, `{`,
   } {
       // put/get are local test adapters dispatching through initHandlers.
       if status := put(body); status != 400 { t.Fatalf("status %d for %s", status, body) }
       if got := get(); !slices.Equal(got, original) { t.Fatalf("changed grants: %v", got) }
   }
   ```

2. Run `go test -v -count=1 ./cmd -run '^TestAssistantKnowledgeAPI'`: expect missing routes (404 on authorized requests). Include malformed/nonpositive path ID (400), valid nonexistent assistant (404), mixed valid/unknown item IDs (400), `[]` (200, GET returns JSON array `[]`).
3. **Green:** Add both routes alongside assistant preview, each with `perm(handler,"ai:manage")`. Parse positive ID; decode a dedicated request struct with `KnowledgeItemIDs []int` tagged `json:"knowledge_item_ids"` (nil is invalid; empty is valid). Pass `r.RequestCtx` and authenticated `amodels.User.ID` from middleware to the store, never accept actor from the body. Use `sendErrorEnvelope`/`SendEnvelope`. Return the sorted committed replacement, not a potentially different subsequent GET after another writer.
4. **Compatibility test:** With grants configured, submit the existing multipart `data` payload through `handleUpdateAIAssistant`, changing the name and omitting knowledge. Assert original grants survive; a stray `knowledge_item_ids` in that profile payload cannot replace them either. New assistants get zero grants. Assert `/assistants/compact` still returns only `{id,name}` (no grant IDs/content). These are characterization tests: they may pass immediately once the grant routes exist. Keep grant fields out of `Assistant`, `decodeAssistantForm`, and the existing tool-replacement transaction.
5. Rerun step 2: expect PASS including compatibility cases. Commit: `feat(api): expose permission-checked assistant knowledge assignments`.

### Task 4: Authorize before computing similarity

**Files:** `internal/ai/queries.sql`, `internal/ai/ai.go` (`queries`), `internal/ai/embedding.go`; create `internal/ai/assistant_knowledge_test.go`.

1. **Red:** Seed `embeddings` manually using `serializeEmbedding`: query `[1,0]`, ungranted snippet `[1,0]`, granted snippet `[0.8,0.6]`; also insert a `help_article` and a `tag` with the granted snippet’s numeric ID and perfect scores. Use `httptest.NewServer` returning `{"data":[{"index":0,"embedding":[1,0]}]}` for embeddings. Configure test-only provider rows before `New`; use a dummy key with the existing provider config/decryption conventions, no network outside the mock, no background reconcile. Use different user and assistant IDs.

   ```go
   hits, err := m.SearchAssistantKnowledge(t.Context(), designID, "neutral", 1)
   if err != nil || len(hits) != 1 || hits[0].SourceType != models.SourceSnippet ||
       hits[0].SourceID != granted || hits[0].ChunkText != "design control" {
       t.Fatalf("expected permitted runner-up: %#v, err=%v", hits, err)
   }
   ```

2. Run `go test -v -count=1 ./internal/ai -run '^TestSearchAssistantKnowledge'`: expect undefined `SearchAssistantKnowledge`. A version using global top-1 then filtering must also fail this test.
3. **Green:** Add `SearchAssistantKnowledge(ctx context.Context, assistantID int, query string, k int) ([]models.SearchResult,error)` with required positive assistant PK and bounded positive `k`. Use a context-aware prepared SELECT from the primary, no cross-request grant or result cache:

   ```sql
   SELECT e.source_type, e.source_id, e.chunk_text, e.embedding, e.dimensions
   FROM ai_assistants a
   JOIN users u ON u.id = a.user_id AND u.deleted_at IS NULL
   JOIN ai_assistant_knowledge_items g ON g.assistant_id = a.id
   JOIN ai_knowledge_base i ON i.id = g.knowledge_item_id AND i.enabled AND i.type = 'snippet'
   JOIN embeddings e ON e.source_id = i.id AND e.source_type = 'snippet'
   WHERE a.id = $1 AND a.enabled;
   ```

4. Return no hits without calling the provider on an empty set; return no hits **and an error** on DB/provider failure. For eligible rows only, embed the query once; decode with `deserializeEmbedding`, skip malformed byte lengths, mismatched dimensions and zero norms; score, sort descending, then take `k`. Reuse/extract the existing cosine-ranking logic without mutating `m.index`. Do not wait on the global index or call `Search` as a fallback. Leave indexing/reconciliation shared.
5. Test disabled assistant/snippet, deleted identity, missing assistant, invalid scope, empty grants, orphan vectors, canceled DB query and provider 401. Grant a shared item, then revoke/disable on connection A; assert immediate denial on manager B and a freshly constructed manager. Keep stale vectors in DB/RAM. Assert every failure returns zero content. Rerun step 2: PASS; commit `feat(ai): rank only explicitly authorized snippet chunks`.

### Task 5: Bind both autonomous search-tool instances

**Files:** `internal/aiagent/tools.go` (`searchKnowledgeTool.Execute`), `internal/aiagent/worker.go` (`handle`, `PreviewReply`), `internal/aiagent/worker_test.go`. Read `internal/aiagent/citations.go`; keep its source-membership check.

1. **Red:** In `TestAssistantKnowledgeToolScope`, exercise the actual tool with the fixture from task 4 (or a scoped search-function spy for the tool-only test). Pass `{"query":"neutral","assistant_id":1}` to a tool constructed for a different assistant; inspect actual search arguments, returned text, and `collect` hits. Run `go test -v -count=1 ./internal/aiagent -run '^TestAssistantKnowledgeToolScope'`: current tool calls global `Search`; forged/irrelevant inputs must never change the server scope.
2. **Green:** Add a required `assistantID` when constructing the tool (and a trusted surface label for telemetry). Call exactly `t.m.ai.SearchAssistantKnowledge(ctx,t.assistantID,in.Query,searchResultLimit)`; zero value denies. Both instantiation sites must pass `.ID`: `assistant.ID` from `GetAssistantByUserID` in `handle`, `a.ID` from `GetAssistant` in `PreviewReply`. Leave model schema query-only and preserve `minConfidence` for authorized hits. Keep preview search-only and autonomous custom tools restricted by `assistant.ToolIDs`.
3. **Refactor/test:** Collect hits only after authorization and confidence filtering; neither `previewSources` nor citation parsing may resolve model-invented IDs. Existing citation parsing may remain for historical display; no new article IDs should be emitted. Run `go test -v -count=1 ./internal/aiagent -run 'TestAssistantKnowledgeToolScope|TestSplitArticleSources'`. Expect PASS. Full worker/preview coverage is required in task 8.
4. Commit: `feat(aiagent): scope live and preview knowledge tools to assistant identity`.

### Task 6: Close staff and generic global-search paths

**Files:** `internal/ai/{agent.go,tools.go,copilot.go,embedding.go,agent_approval_test.go}`, `internal/aiagent/{worker.go,faq.go}`; create `internal/ai/staff_knowledge_test.go`. Inspect `cmd/ai.go` (`handleAICopilot`, `handleAIGenerateReply`, `decideAIToolRun`) and `internal/ai/tagindex.go`; no new staff request field.

1. **Red:** `TestStaffKnowledgeUnavailable` calls actual `Manager.Copilot` (with/without persona) and `GenerateReply` (with/without conversation context) against a local scripted completion provider. Inspect outgoing tool definitions and system instructions for each surface. Have the provider **request** `search_articles` and `search_knowledge_base` even though absent: tool replies must be unknown-tool responses, no snippet text, zero embedding requests. With another allowed tool verify it still appears. Run `go test -v -count=1 ./internal/ai -run '^TestStaffKnowledge'`: existing `search_articles` registration/prompt fails.
2. **Green:** Delete `searchArticlesTool` and built-in search registration; retain both names in the reserved-name set so custom HTTP tools cannot claim them. Remove `includeBuiltinSearch` throughout `buildToolRegistry`, `newAgentRun`, `RunAgentWithTools` and callers; remove unused `RunAgent` or ensure it cannot create a search capability. Remove `search_articles` directives in both staff prompts, preserving writing and non-KB tools. Do not thread persona ID into retrieval.
3. Replace exported global `Manager.Search` with a maintenance-only boolean API, e.g. `HasSimilarFAQ(ctx,text,threshold)`, calling private `searchSources` with the old FAQ dedup source types/threshold. `isDuplicateFAQ` consumes only `(bool,error)`; `ApproveFAQSuggestion` still creates an **unbound** snippet. Tag search remains `SourceTag` only. Add dedup yes/no and staff resume regressions: pause on an approvable custom tool using `agent_approval_test.go` patterns, resolve it, attempt search in the resumed run; it remains unavailable.
4. Run `go test -count=1 -v ./internal/ai ./internal/aiagent -run 'TestStaffKnowledge|TestAgentTool|TestFAQDedup|TestTag'`; expect PASS. Check `rg -n -F -e includeBuiltinSearch -e 'type searchArticlesTool' -e 'func (m *Manager) Search(' internal/ai` returns no legacy definition. No direct grant search in staff handlers. Commit `fix(ai): remove global knowledge search from staff tools`.

### Task 7: Make selection explicit in the assistant form

**Files:** `frontend/apps/main/src/api/index.js`, `frontend/apps/main/src/features/admin/ai/AssistantForm.vue`, `frontend/apps/main/src/views/admin/ai/CreateOrEditAssistant.vue`, `i18n/en-US.json`; create `frontend/apps/main/src/features/admin/ai/AssistantForm.test.js`, `frontend/apps/main/src/views/admin/ai/CreateOrEditAssistant.test.js`.

1. **Red (component):** Use jsdom, `createApp`, `nextTick`, `vi.hoisted` API mocks following `frontend/apps/main/src/views/admin/inbox/EditInbox.test.js`. Mount the real picker, supply two snippet titles (one disabled), GET IDs, assert selected state and disabled-item label; change one checkbox and save. Include this parent-view assertion using a stub form as in `EditInbox.test.js`:

   ```js
   await form.props.submitForm(new FormData(), undefined) // no explicit knowledge edit
   expect(updateAIAssistant).toHaveBeenCalledOnce()
   expect(replaceAIAssistantKnowledgeItems).not.toHaveBeenCalled()
   ```

2. Run `(cd frontend && npx pnpm exec vitest run apps/main/src/features/admin/ai/AssistantForm.test.js apps/main/src/views/admin/ai/CreateOrEditAssistant.test.js)`: expect missing picker / no grant PUT on changed selection. Test explicitly clearing sends `{knowledge_item_ids:[]}`.
3. **Green (load):** Add `getAIAssistantKnowledgeItems(id)`/`replaceAIAssistantKnowledgeItems(id,ids)` helpers with the dedicated routes. Use `getAISnippets()` for titles (do not render snippet bodies). In the form, load grants separately from the profile/tools; track baseline, draft, and loading/error by assistant ID. On ID change discard stale responses. Disable editing on load failure, allow retry and ordinary profile save. Show “No snippets available to this assistant” only for a **known** empty set; show disabled status without silently removing grants.
4. **Green (save):** Compare selected sets ignoring order. Call `submitForm(formData, explicitIDsOrUndefined)`; `FormData.data` stays unchanged. The parent currently redirects after create and swallows errors. Keep the returned `resp.data.data.id`; perform PUT only after profile success and only on an explicit change. Delay success/redirect until required writes finish. On partial failure retain saved ID and dirty selection, report “Assistant saved; knowledge items were not saved”, allow retry of PUT without another create. Refresh the baseline only after success.
5. **Regression:** Failed GET never writes `[]`; initial load and unrelated edits never PUT; change-and-revert never PUT; failed profile save never PUT; new assistant POST precedes PUT; failed PUT then retry does not POST again. Rerun step 2 (PASS) and `(cd frontend && npx pnpm build:main)`. Commit `feat(ui): manage explicit snippet grants in assistant settings`.

### Task 8: Prove isolation at the provider and customer boundaries

**Files:** create `internal/aiagent/knowledge_acceptance_test.go`; reference `cmd/ai_test.go` for conversation seeding, `internal/ai/openai_test.go` for provider wire format, `internal/aiagent/otp_test.go` for miniredis, `internal/aiagent/{worker.go,citations.go}` for observable outputs.

1. **Fixture:** New disposable DB, real AI/assistant managers, local `httptest` embedding/completion server, real conversation manager and enabled livechat inbox with dependencies for `QueueReply`. Mirror construction in `cmd/init.go`; run `m.handle(ctx,convID)` synchronously (not an async worker queue). Seed separate contacts, fresh open conversations with an incoming contact message and correct `assigned_user_id`; disable unrelated tools. Never reuse chat history between assistants. Set `MaxSteps` >= 3 and positive `MaxHistoryMessages`.
2. Insert synthetic Allen assistant PK `1` / user `10` and a distinct mydesign PK/user, plus a unique marker `marker := "allen-test-" + uuid.NewString()` and a design-only control. Put the marker **only** in Allen’s snippet/chunk, never in the query, persona, echoed scripts or previous messages. Give Allen chunks score 1; give design score 0.8. Use at least `searchResultLimit` stronger ungranted chunks so post-filtering global top-5 loses the control (task 4 also proves `k=1`). Include a marker-independent distinctive Allen title. Enable citations and seed a published article sharing a snippet ID to exercise type separation.
3. **Red/green oracle:** Script completion turn 1 to issue `search_knowledge_base(query="neutral")` (repeat with forged scope); turn 2 records the role-`tool` message and echoes its content, plus an invented citation to the colliding article. Respond based on the request's messages, not on assistant names. Exercise **both** real `PreviewReply` and `handle` for both assistants. Read `conversation_messages.content, meta` for each posted reply (`ai_assistant_id`, no `ai_article_ids`); record sources returned by preview. Representative assertions inside the surface loop:

   ```go
   for _, text := range []string{design.toolText, design.reply} {
       if strings.Contains(text, marker) || !strings.Contains(text, "design control") {
           t.Fatalf("%s: forbidden marker or missing authorized result", surface)
       }
   }
   if !strings.Contains(allen.toolText, marker) || !strings.Contains(allen.reply, marker) {
       t.Fatalf("%s: positive control failed", surface)
   }
   // For preview require exactly the expected (type, id, title), no Allen URL or metadata.
   // For live replies require an actual new outgoing row, correct sender user ID and no article refs.
   ```

4. Run `go test -v -count=1 ./internal/aiagent -run '^TestAssistantKnowledgeCanary$'`. This is a cross-layer test of earlier green increments: prove its **red** in the disposable fixture by deliberately binding Allen’s item to mydesign and running the negative assertion (must report forbidden marker); remove the extra grant and rerun for PASS. Separately temporarily substitute a user ID at each construction site: positive control must fail. Restore before commit. Do not add an insecure product switch for the test.
5. Add one run with zero grants, one after revoke, and check provider-bound message stream and structured metadata, not just final prose. Run `go test -race -count=1 -v ./internal/aiagent -run '^TestAssistantKnowledgeCanary$'`; PASS, no skipped database. Commit `test(aiagent): prove cross-assistant isolation in replies and preview`.

### Task 9: Audit grants and keep retrieved content out of logs

**Files:** `internal/aiagent/{knowledge_items.go,tools.go,knowledge_items_test.go}`, `internal/ai/{agent.go,agent_approval.go,openai.go,ai.go}`, plus `internal/aiagent/knowledge_acceptance_test.go` and `internal/ai/assistant_knowledge_test.go`.

1. **Red:** Capture debug-level `logf` output in a `bytes.Buffer` (writer pattern in `internal/ai/ai_test.go`). Exercise successful and failed replacement, authorized search, no grants, provider error whose body echoes the request, and final answer. Use distinct synthetic sentinels for query, chunk/title/URL and fake key. Assert none occur in logs; assert expected event fields exist. Run `go test -v -count=1 ./internal/ai ./internal/aiagent -run 'TestKnowledgeAudit|TestKnowledgeLogs'`. Today `executeToolCall` logs `args` and `result`; `continueAgentRun` logs model content/final answer.
2. **Green:** After commit only, emit structured audit event with authenticated actor, assistant PK, sorted added/removed ID sets and time. Errors never claim success. Emit countable events per trusted surface (`autonomous`/`preview`) with outcome and authorized hit count, including denied/empty and failure; use low-cardinality outcome labels, not IDs as metric labels. If distinguishing no grants from granted-but-not-indexed, use an existence flag from the authorized query, not a probe of global matches.
3. Remove raw prompt/args/result/answer logging along the whole agent loop (including forced final turn); log lengths/counts/status instead. Ensure `OpenAIClient` failure and retry paths and `providerError` cannot log echoed bodies, credentials, or raw provider errors containing content; retain safe status/category diagnostics. Exercise success, denied and failure paths with debug logging enabled. Assert no snippet body/source URL in logs even when a valid assistant retrieves it.
4. Rerun step 1 and `go test -count=1 ./internal/ai -run 'TestProviderError|TestAgentTool'`; expect PASS. Commit `fix(ai): audit grant changes and redact knowledge payloads`.

### Task 10: Verify upgrade compatibility and release gates locally

**Files:** extend `internal/migrations/v2.10.0-rc.4_test.go`, `internal/aiagent/knowledge_acceptance_test.go`; no production or deployment configuration changes.

1. Add `TestKnowledgeUpgradeIsolation`: seed legacy snippets from manual, URL and conversation/FAQ sources plus pre-existing embeddings, no bindings; simulate the prior schema and run rc.4. Assert no grants, source bytes/counts unchanged, both assistants get zero hits. Create an explicit grant through the same store used by the admin API; only then may a result appear. Run the same assertions on a fresh install. Exercise manual create/import/FAQ approval with synthetic data and confirm none inserts a grant. Keep all external services fake.
2. Run `go test -v -count=1 ./internal/migrations ./cmd ./internal/ai ./internal/aiagent -run 'TestKnowledge|TestAssistantKnowledge|TestSearchAssistantKnowledge|TestStaffKnowledge'`. Expected: named DB, staff, live and preview cases actually PASS. If an implementation assumes a legacy default grant, the zero-hit assertion is red: remove that default rather than adjusting the fixture.
3. Run `make test` and `git diff --check`; confirm Go and frontend suites pass. Verify `git diff --stat` contains no credentials, real snippets, provider changes or production deployment edits. Commit added regression coverage: `test(ai): verify unbound legacy knowledge on upgrade`.
4. **Handoff to the later operations plan:** Require an additive migration before new SQL is prepared; no mixed-version serving of knowledge traffic, since old binaries ignore grants. Require old workers and pending runs drained before enabling retrieval. Rollback retains vectors and grants; disable affected traffic/workers or serve an enforcing version. Never re-enable a global reader as rollback. Record these as release conditions only; do not deploy, bind production IDs, import CV content, or reindex production as part of this plan.
