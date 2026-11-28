package migrations

import (
	"github.com/jmoiron/sqlx"
	"github.com/knadh/koanf/v2"
	"github.com/knadh/stuffbin"
)

func V2_10_0_RC4(db *sqlx.DB, fs stuffbin.FileSystem, ko *koanf.Koanf) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS ai_assistant_knowledge_items (
			assistant_id INTEGER NOT NULL REFERENCES ai_assistants(id) ON DELETE CASCADE,
			knowledge_item_id INTEGER NOT NULL REFERENCES ai_knowledge_base(id) ON DELETE CASCADE,
			PRIMARY KEY (assistant_id, knowledge_item_id)
		);
		CREATE INDEX IF NOT EXISTS index_ai_assistant_knowledge_items_on_item
			ON ai_assistant_knowledge_items(knowledge_item_id, assistant_id);
	`)
	return err
}
