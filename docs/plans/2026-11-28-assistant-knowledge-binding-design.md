# Explicit assistant knowledge grants

**Approved phase 1:** An assistant can retrieve an enabled `ai_knowledge_base` snippet only if an administrator has bound that item to that assistant. A new assistant starts with no knowledge grants. In particular, granting Allen’s Robot an Allen CV snippet must not make it available to mydesign, even if the information is public.

## Why this needs a retrieval boundary

`ai_knowledge_base` stores individual snippets, not collections. The `embeddings` table stores chunks of multiple source types. Today `internal/ai/embedding.go` loads a shared index and `Manager.Search` ranks snippets and Help Center articles across the workspace. `search_knowledge_base` (autonomous replies and preview) and `search_articles` (staff tools) both call it. Assistant tool assignments control custom tools, not the rows returned by either search. A prompt that tells a model to use only its own knowledge is too late: the tool has already disclosed the text.

## Data and administration

Add the following to `schema.sql` and a forward migration registered in `cmd/upgrade.go`:

```sql
CREATE TABLE ai_assistant_knowledge_items (
    assistant_id INTEGER NOT NULL REFERENCES ai_assistants(id) ON DELETE CASCADE,
    knowledge_item_id INTEGER NOT NULL REFERENCES ai_knowledge_base(id) ON DELETE CASCADE,
    PRIMARY KEY (assistant_id, knowledge_item_id)
);
CREATE INDEX index_ai_assistant_knowledge_items_on_item
    ON ai_assistant_knowledge_items (knowledge_item_id, assistant_id);
```

The same item can be explicitly shared. Neither enabling an item nor creating, importing, or approving a FAQ snippet grants access. Existing assistant, snippet, and embedding rows need no rewrites. Keep the embedding provider, chunking, storage, and indexing shared; adding or revoking a grant never schedules re-embedding.

Implement `GET` and `PUT /api/v1/ai/assistants/{id}/knowledge-items` in `cmd/handlers.go` and `cmd/aiagent.go`, both guarded by `ai:manage`. Use the same data shape for a PUT body and the GET response payload: `{"knowledge_item_ids":[3,7]}`. GET returns a consistent, sorted list, including grants to temporarily disabled snippets. PUT replaces the entire set. An explicit `[]` clears it; a missing field, `null`, a duplicate, a nonpositive ID, or an unknown/non-snippet ID is invalid. A nonexistent assistant is not an empty list: return not found.

In `internal/aiagent/{aiagent.go,queries.sql}`, lock the assistant row in one transaction, validate all IDs, replace the grants, and commit. Readers see the old or new set, never an intermediate empty set. On any validation, FK, or write failure, roll back the whole operation. Concurrent valid replacements serialize; the last one wins.

In `frontend/apps/main/src/features/admin/ai/AssistantForm.vue`, show a knowledge-item multiselect using titles from the admin snippets list. Label an empty selection “No snippets available to this assistant”; distinguish disabled items. Load grants using `frontend/apps/main/src/api/index.js`. Keep this field separate from the existing assistant payload: PUT only on an explicit selection change, after the assistant has an ID. Never interpret an unloaded or failed GET as an empty selection. If the assistant save succeeds but the binding PUT fails, show that distinction and allow retry. Existing clients and unrelated assistant saves preserve grants.

## Retrieval contract

1. In `internal/aiagent/worker.go`, `handle` resolves `AssignedUserID` via `GetAssistantByUserID`; `PreviewReply` loads the assistant identified by the authorized admin route. Construct `searchKnowledgeTool` with that assistant's **primary key**, not its user ID. Its model-facing arguments contain only the query; extra arguments cannot override scope.
2. Replace the global call in `internal/aiagent/tools.go` with `SearchAssistantKnowledge(ctx, assistantID, query, k)`. In `internal/ai/{queries.sql,embedding.go}`, read eligible chunks from the primary database per search: join the requested enabled assistant and its live user to its grants, then to enabled snippet rows, then to `embeddings` on **both** `source_type = 'snippet'` and matching `source_id`. Numeric IDs from other source types can collide. Rank only these rows by cosine similarity; apply the limit and confidence rules afterward. Do not filter a global top-k or use a cached grant snapshot from run start.
3. Missing, disabled, or unbound assistant: no chunks. Invalid scope, DB failure, or provider failure: no content; errors may propagate to the existing failure handler. Never retry with global search. A committed revoke is effective for searches started afterward, including on another worker with a warm vector index. It cannot retract content already delivered to a run.
4. Only authorized hits may become tool messages, ground final replies, or supply IDs, titles, URLs, or scores to `splitArticleSources` and `previewSources`. Phase 1 produces no Help Center citation IDs. This boundary governs retrieval; it does not erase text already in conversations, custom tool responses, or model training data.

**Staff surfaces:** In `internal/ai/{agent.go,tools.go,copilot.go}`, omit built-in knowledge search from Copilot and Generate Reply, and remove prompt instructions to call it. Cover persona-selected Copilot, drafts without a conversation, and resumed approvals. Neither staff authentication nor choosing an assistant's voice grants its knowledge. Preserve unrelated staff tools. Retire the generic unrestricted model-facing search path; keep FAQ deduplication (`internal/aiagent/faq.go`) a score/boolean-only maintenance operation and tag search restricted to tag vectors.

## Deployment and operations

Apply the additive migration with **zero grants**. There is no wildcard, default assistant, implicit backfill, or inference from source URLs. Drain old workers and pending model runs and cut over every serving instance before importing Allen content or enabling retrieval traffic. Old binaries ignore the table. Verify Allen’s Robot is assistant `1`, linked to user `10`, in this deployment; use the admin endpoint to bind only reviewed public-career snippet IDs to assistant `1`. Never seed an environment-specific grant in a migration.

Audit successful replacements with actor, assistant ID, added/removed item IDs, and time. Count searches, empty-grant outcomes, errors, and authorized hits by surface; use assistant IDs in restricted diagnostic logs. Never log chunk text, titles, source URLs, raw queries, provider keys, or tool payloads. On rollback, retain the grants and vectors and disable knowledge retrieval or stop affected traffic/workers. Resume only with a version enforcing the boundary; restoring the old unrestricted reader is not a safe rollback.

## Acceptance tests

- **Cross-assistant canary:** Seed a random marker only in an Allen snippet; give its vector the highest global similarity. Bind it only to Allen; bind a lower-ranked control to mydesign. With `k=1` and a deterministic model that echoes tool output, both autonomous and preview paths return the marker for Allen and only the control for mydesign. Inspect provider-bound messages, final output, citations, and preview sources: no Allen marker or source metadata for mydesign. Repeat with a forged assistant ID in tool arguments.
- **Source and identity:** Use assistant `1` with user `10`, and create a Help Center article and tag sharing an allowed snippet's numeric ID. Only the snippet is returned. Explicitly grant a second assistant the same snippet and verify both can retrieve it.
- **Deny and revoke:** Cover missing scope, nonexistent/disabled assistant, disabled/deleted snippet, no grants, and DB/provider errors. Assert no retrieved content and no fallback. Revoke between two tool calls; the next call on a second worker and after restart returns no hit even with old vectors present.
- **Administration:** Test both routes without `ai:manage`, idempotent replacement, clearing, invalid mixed lists, a forced write failure, concurrent replacements, and cascade deletion. Verify no partial grants. Test an ordinary form save, a failed grants load, and a failed PUT. Legacy, manual, URL-imported, and FAQ-created snippets start unbound.
- **Staff and cutover:** Inspect tool definitions and attempted tool calls on both staff surfaces, with and without a persona/conversation as supported: no knowledge tool or snippet payload. Start from an upgraded database containing legacy chunks; verify the empty-grant case before binding any item and the scoped case afterward.

Help Center article grants, authorized staff knowledge search, and named knowledge collections are follow-on designs. None may widen phase-1 retrieval by default.
