package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// schema is applied idempotently on connect. Mirrors db/schema.sql.
const schema = `
CREATE TABLE IF NOT EXISTS documents (
    id         TEXT PRIMARY KEY,
    type       TEXT NOT NULL DEFAULT 'pdf',
    draft      TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS script_versions (
    doc_id     TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    version    INT  NOT NULL,
    script     TEXT NOT NULL,
    pdf_path   TEXT NOT NULL DEFAULT '',
    cover_path TEXT NOT NULL DEFAULT '',
    pptx_path  TEXT NOT NULL DEFAULT '',
    manifest   TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (doc_id, version)
);
CREATE TABLE IF NOT EXISTS chat_messages (
    id           BIGSERIAL PRIMARY KEY,
    doc_id       TEXT NOT NULL,
    role         TEXT NOT NULL,
    content      TEXT,
    tool_calls   TEXT,
    tool_call_id TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_chat_messages_doc ON chat_messages (doc_id, id);
ALTER TABLE documents ADD COLUMN IF NOT EXISTS type TEXT NOT NULL DEFAULT 'pdf';
ALTER TABLE script_versions ADD COLUMN IF NOT EXISTS cover_path TEXT NOT NULL DEFAULT '';
ALTER TABLE script_versions ADD COLUMN IF NOT EXISTS pptx_path TEXT NOT NULL DEFAULT '';
`

// Postgres is a Store backed by the shared Postgres database.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres connects and ensures the schema, returning a Postgres store.
func NewPostgres(ctx context.Context, connString string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

// Close releases the pool.
func (p *Postgres) Close() { p.pool.Close() }

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (p *Postgres) Create(script string) (Document, error) {
	ctx := context.Background()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return Document{}, err
	}
	defer tx.Rollback(ctx)

	var id string
	if err := tx.QueryRow(ctx,
		`INSERT INTO documents (id, draft) VALUES (gen_random_uuid()::text, $1) RETURNING id`, script,
	).Scan(&id); err != nil {
		return Document{}, err
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx,
		`INSERT INTO script_versions (doc_id, version, script, created_at) VALUES ($1, 1, $2, $3)`, id, script, now,
	); err != nil {
		return Document{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Document{}, err
	}
	return Document{
		ID:       id,
		Type:     TypePDF,
		Versions: []ScriptVersion{{Version: 1, Script: script, CreatedAt: now}},
		Draft:    script,
	}, nil
}

func (p *Postgres) Seed(id, script string) (Document, error) {
	ctx := context.Background()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return Document{}, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `INSERT INTO documents (id, draft) VALUES ($1, $2)`, id, script); err != nil {
		if isUniqueViolation(err) {
			return Document{}, fmt.Errorf("document %q already exists", id)
		}
		return Document{}, err
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx,
		`INSERT INTO script_versions (doc_id, version, script, created_at) VALUES ($1, 1, $2, $3)`, id, script, now,
	); err != nil {
		return Document{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Document{}, err
	}
	return Document{
		ID:       id,
		Type:     TypePDF,
		Versions: []ScriptVersion{{Version: 1, Script: script, CreatedAt: now}},
		Draft:    script,
	}, nil
}

func (p *Postgres) Get(id string) (Document, error) {
	ctx := context.Background()
	var draft, projectType string
	if err := p.pool.QueryRow(ctx, `SELECT draft, type FROM documents WHERE id = $1`, id).Scan(&draft, &projectType); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Document{}, fmt.Errorf("document %q not found", id)
		}
		return Document{}, err
	}
	vs, err := p.ListVersions(id)
	if err != nil {
		return Document{}, err
	}
	return Document{ID: id, Type: NormalizeType(projectType), Versions: vs, Draft: draft}, nil
}

func (p *Postgres) Edit(id string, baseVersion int, script string) error {
	ctx := context.Background()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var cur int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM script_versions WHERE doc_id = $1`, id,
	).Scan(&cur); err != nil {
		return err
	}
	if cur == 0 {
		return fmt.Errorf("document %q not found", id)
	}
	if baseVersion != cur {
		return fmt.Errorf("stale base_version %d: current version is %d", baseVersion, cur)
	}
	if _, err := tx.Exec(ctx, `UPDATE documents SET draft = $2 WHERE id = $1`, id, script); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Postgres) Commit(id string) (ScriptVersion, error) {
	ctx := context.Background()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return ScriptVersion{}, err
	}
	defer tx.Rollback(ctx)

	var draft string
	if err := tx.QueryRow(ctx, `SELECT draft FROM documents WHERE id = $1`, id).Scan(&draft); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ScriptVersion{}, fmt.Errorf("document %q not found", id)
		}
		return ScriptVersion{}, err
	}
	var cur int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM script_versions WHERE doc_id = $1`, id,
	).Scan(&cur); err != nil {
		return ScriptVersion{}, err
	}
	v := cur + 1
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx,
		`INSERT INTO script_versions (doc_id, version, script, created_at) VALUES ($1, $2, $3, $4)`, id, v, draft, now,
	); err != nil {
		return ScriptVersion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ScriptVersion{}, err
	}
	return ScriptVersion{Version: v, Script: draft, CreatedAt: now}, nil
}

func (p *Postgres) RecordOutput(id string, version int, pdfPath, coverPath, pptxPath, manifest string) error {
	ctx := context.Background()
	tag, err := p.pool.Exec(ctx,
		`UPDATE script_versions SET pdf_path = $3, cover_path = $4, pptx_path = $5, manifest = $6 WHERE doc_id = $1 AND version = $2`,
		id, version, pdfPath, coverPath, pptxPath, manifest,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("version %d not found in document %q", version, id)
	}
	return nil
}

func (p *Postgres) ListVersions(id string) ([]ScriptVersion, error) {
	ctx := context.Background()
	rows, err := p.pool.Query(ctx,
		`SELECT version, script, pdf_path, cover_path, pptx_path, manifest, created_at FROM script_versions WHERE doc_id = $1 ORDER BY version`, id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var vs []ScriptVersion
	for rows.Next() {
		var v ScriptVersion
		if err := rows.Scan(&v.Version, &v.Script, &v.PDFKey, &v.CoverKey, &v.PPTXKey, &v.Manifest, &v.CreatedAt); err != nil {
			return nil, err
		}
		vs = append(vs, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return nil, fmt.Errorf("document %q not found", id)
	}
	return vs, nil
}

func (p *Postgres) Restore(id string, version int) (ScriptVersion, error) {
	ctx := context.Background()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return ScriptVersion{}, err
	}
	defer tx.Rollback(ctx)

	var src ScriptVersion
	if err := tx.QueryRow(ctx,
		`SELECT version, script, pdf_path, cover_path, pptx_path, manifest, created_at FROM script_versions WHERE doc_id = $1 AND version = $2`,
		id, version,
	).Scan(&src.Version, &src.Script, &src.PDFKey, &src.CoverKey, &src.PPTXKey, &src.Manifest, &src.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ScriptVersion{}, fmt.Errorf("version %d not found in document %q", version, id)
		}
		return ScriptVersion{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE documents SET draft = $2 WHERE id = $1`, id, src.Script); err != nil {
		return ScriptVersion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ScriptVersion{}, err
	}
	return src, nil
}
