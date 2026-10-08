package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/session"
)

// optionalTime 把零值 time.Time 转为 NULL，避免 pending 阶段的 started_at 落成非空假时间。
func optionalTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// ---- Turns ----

type turnStore struct{ s *dbStore }

func (st *turnStore) Create(ctx context.Context, t *session.Turn) error {
	now := time.Now()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = now
	}
	_, err := st.s.db.ExecContext(ctx, `
		INSERT INTO turns (id, session_id, user_id, status, input, output, error,
			prompt_tokens, completion_tokens, total_tokens, started_at, finished_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.SessionID, t.UserID, string(t.Status),
		redactJSONPayload(t.Input), redactJSONPayload(t.Output), redactJSONPayload(t.Error),
		t.Token.PromptTokens, t.Token.CompletionTokens, t.Token.TotalTokens,
		optionalTime(t.StartedAt), nilTime(t.FinishedAt), t.CreatedAt, t.UpdatedAt)
	return mapErr(err, "create turn")
}

func scanTurn(scanner interface{ Scan(...any) error }) (*session.Turn, error) {
	t := &session.Turn{}
	var started, finished sql.NullTime
	if err := scanner.Scan(&t.ID, &t.SessionID, &t.UserID, &t.Status, &t.Input, &t.Output, &t.Error,
		&t.Token.PromptTokens, &t.Token.CompletionTokens, &t.Token.TotalTokens,
		&started, &finished, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	t.StartedAt = started.Time
	if finished.Valid {
		ft := finished.Time
		t.FinishedAt = &ft
	}
	return t, nil
}

func (st *turnStore) Get(ctx context.Context, id string) (*session.Turn, error) {
	row := st.s.db.QueryRowContext(ctx, `
		SELECT id, session_id, user_id, status, input, output, error,
			prompt_tokens, completion_tokens, total_tokens, started_at, finished_at, created_at, updated_at
		FROM turns WHERE id = ?`, id)
	t, err := scanTurn(row)
	if err != nil {
		return nil, mapErr(err, "get turn")
	}
	return t, nil
}

func (st *turnStore) Update(ctx context.Context, t *session.Turn) error {
	t.UpdatedAt = time.Now()
	res, err := st.s.db.ExecContext(ctx, `
		UPDATE turns SET session_id=?, user_id=?, status=?, input=?, output=?, error=?,
			prompt_tokens=?, completion_tokens=?, total_tokens=?, started_at=?, finished_at=?, updated_at=?
		WHERE id=?`,
		t.SessionID, t.UserID, string(t.Status),
		redactJSONPayload(t.Input), redactJSONPayload(t.Output), redactJSONPayload(t.Error),
		t.Token.PromptTokens, t.Token.CompletionTokens, t.Token.TotalTokens,
		optionalTime(t.StartedAt), nilTime(t.FinishedAt), t.UpdatedAt, t.ID)
	if err != nil {
		return fmt.Errorf("update turn: %w", err)
	}
	return ensureAffected(res, "update turn")
}

func (st *turnStore) ListBySession(ctx context.Context, sessionID string) ([]*session.Turn, error) {
	rows, err := st.s.db.QueryContext(ctx, `
		SELECT id, session_id, user_id, status, input, output, error,
			prompt_tokens, completion_tokens, total_tokens, started_at, finished_at, created_at, updated_at
		FROM turns WHERE session_id = ? ORDER BY created_at, id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list turns: %w", err)
	}
	defer rows.Close()
	var out []*session.Turn
	for rows.Next() {
		t, err := scanTurn(rows)
		if err != nil {
			return nil, fmt.Errorf("scan turn: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (st *turnStore) Delete(ctx context.Context, id string) error {
	res, err := st.s.db.ExecContext(ctx, `DELETE FROM turns WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete turn: %w", err)
	}
	return ensureAffected(res, "delete turn")
}

// ---- Steps ----

type stepStore struct{ s *dbStore }

func (st *stepStore) Create(ctx context.Context, s *session.Step) error {
	now := time.Now()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = now
	}
	_, err := st.s.db.ExecContext(ctx, `
		INSERT INTO steps (id, turn_id, session_id, seq, type, status, tool, params, result, error,
			started_at, finished_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.TurnID, s.SessionID, s.Seq, string(s.Type), string(s.Status), s.Tool,
		redactJSONPayload(s.Params), redactJSONPayload(s.Result), redactJSONPayload(s.Error),
		optionalTime(s.StartedAt), nilTime(s.FinishedAt), s.CreatedAt, s.UpdatedAt)
	return mapErr(err, "create step")
}

func scanStep(scanner interface{ Scan(...any) error }) (*session.Step, error) {
	s := &session.Step{}
	var started, finished sql.NullTime
	if err := scanner.Scan(&s.ID, &s.TurnID, &s.SessionID, &s.Seq, &s.Type, &s.Status, &s.Tool,
		&s.Params, &s.Result, &s.Error,
		&started, &finished, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	s.StartedAt = started.Time
	if finished.Valid {
		ft := finished.Time
		s.FinishedAt = &ft
	}
	return s, nil
}

func (st *stepStore) Get(ctx context.Context, id string) (*session.Step, error) {
	row := st.s.db.QueryRowContext(ctx, `
		SELECT id, turn_id, session_id, seq, type, status, tool, params, result, error,
			started_at, finished_at, created_at, updated_at
		FROM steps WHERE id = ?`, id)
	s, err := scanStep(row)
	if err != nil {
		return nil, mapErr(err, "get step")
	}
	return s, nil
}

func (st *stepStore) Update(ctx context.Context, s *session.Step) error {
	s.UpdatedAt = time.Now()
	res, err := st.s.db.ExecContext(ctx, `
		UPDATE steps SET turn_id=?, session_id=?, seq=?, type=?, status=?, tool=?, params=?, result=?, error=?,
			started_at=?, finished_at=?, updated_at=?
		WHERE id=?`,
		s.TurnID, s.SessionID, s.Seq, string(s.Type), string(s.Status), s.Tool,
		redactJSONPayload(s.Params), redactJSONPayload(s.Result), redactJSONPayload(s.Error),
		optionalTime(s.StartedAt), nilTime(s.FinishedAt), s.UpdatedAt, s.ID)
	if err != nil {
		return fmt.Errorf("update step: %w", err)
	}
	return ensureAffected(res, "update step")
}

func (st *stepStore) ListByTurn(ctx context.Context, turnID string) ([]*session.Step, error) {
	rows, err := st.s.db.QueryContext(ctx, `
		SELECT id, turn_id, session_id, seq, type, status, tool, params, result, error,
			started_at, finished_at, created_at, updated_at
		FROM steps WHERE turn_id = ? ORDER BY seq, id`, turnID)
	if err != nil {
		return nil, fmt.Errorf("list steps: %w", err)
	}
	defer rows.Close()
	var out []*session.Step
	for rows.Next() {
		s, err := scanStep(rows)
		if err != nil {
			return nil, fmt.Errorf("scan step: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (st *stepStore) Delete(ctx context.Context, id string) error {
	res, err := st.s.db.ExecContext(ctx, `DELETE FROM steps WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete step: %w", err)
	}
	return ensureAffected(res, "delete step")
}
