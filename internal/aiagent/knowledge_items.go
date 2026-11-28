package aiagent

import (
	"context"
	"database/sql"
	"math"
	"slices"

	"github.com/abhinavxd/libredesk/internal/envelope"
	"github.com/lib/pq"
)

// GetKnowledgeItemIDs lists explicit grants, including grants to disabled snippets.
func (m *Manager) GetKnowledgeItemIDs(ctx context.Context, assistantID int) ([]int, error) {
	var stored pq.Int64Array
	err := m.q.GetKnowledgeItemIDs.QueryRowContext(ctx, assistantID).Scan(&stored)
	if err == sql.ErrNoRows {
		return nil, envelope.NewError(envelope.NotFoundError, m.i18n.T("globals.messages.notFound"), nil)
	}
	if err != nil {
		return nil, err
	}
	ids := make([]int, len(stored))
	for i, id := range stored {
		ids[i] = int(id)
	}
	return ids, nil
}

// ReplaceKnowledgeItemIDs serializes replacements for an assistant. Only an
// explicit empty slice clears its grants; ordinary profile updates do not.
func (m *Manager) ReplaceKnowledgeItemIDs(ctx context.Context, assistantID int, ids []int, actorID int) error {
	invalid := func() error {
		return envelope.NewError(envelope.InputError, m.i18n.T("validation.invalidKnowledgeItemIDs"), nil)
	}
	if ids == nil {
		return invalid()
	}
	ordered := slices.Clone(ids)
	slices.Sort(ordered)
	for i, id := range ordered {
		if id <= 0 || id > math.MaxInt32 || (i > 0 && id == ordered[i-1]) {
			return invalid()
		}
	}
	tx, err := m.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var locked int
	err = tx.Stmtx(m.q.LockKnowledgeAssistant).GetContext(ctx, &locked, assistantID)
	if err == sql.ErrNoRows {
		return envelope.NewError(envelope.NotFoundError, m.i18n.T("globals.messages.notFound"), nil)
	}
	if err != nil {
		return err
	}
	var n int
	if err := tx.Stmtx(m.q.CountSnippetIDs).GetContext(ctx, &n, pq.Array(ordered)); err != nil {
		return err
	}
	if n != len(ordered) {
		return invalid()
	}
	var previous pq.Int64Array
	if err := tx.Stmtx(m.q.GetKnowledgeItemIDs).QueryRowContext(ctx, assistantID).Scan(&previous); err != nil {
		return err
	}
	if !slices.EqualFunc(previous, ordered, func(a int64, b int) bool { return a == int64(b) }) {
		if _, err := tx.Stmtx(m.q.DeleteKnowledgeItemIDs).ExecContext(ctx, assistantID); err != nil {
			return err
		}
		if _, err := tx.Stmtx(m.q.InsertKnowledgeItemIDs).ExecContext(ctx, assistantID, pq.Array(ordered)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
