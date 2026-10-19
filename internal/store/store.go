// Package store keeps the command server's jobs in one SQLite table.
//
// A job is queued, then sent when the board takes it, then done or failed when
// the board reports. Nothing moves a job backwards and nothing times it out: a
// job left sent is one whose outcome is unknown.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/RolandWarburton/ptouch-print-server/internal/wire"
)

const (
	Queued = "queued"
	Sent   = "sent"
	Done   = "done"
	Failed = "failed"
)

var (
	ErrNotFound = errors.New("no such job")
	ErrNotSent  = errors.New("job isn't waiting for a result")
)

// Job is a job as people see it. The image itself isn't loaded.
type Job struct {
	ID         int64        `json:"id"`
	Kind       string       `json:"kind"`
	Text       string       `json:"text,omitempty"`
	TapeMM     int          `json:"tape_mm,omitempty"`
	Status     string       `json:"status"`
	Submitter  string       `json:"submitter"`
	CreatedAt  time.Time    `json:"created_at"`
	SentAt     *time.Time   `json:"sent_at,omitempty"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
	Result     *wire.Result `json:"result,omitempty"`
}

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS jobs (
	id          INTEGER PRIMARY KEY,
	kind        TEXT    NOT NULL,
	text        TEXT    NOT NULL DEFAULT '',
	tape_mm     INTEGER NOT NULL DEFAULT 0,
	image       BLOB,
	status      TEXT    NOT NULL,
	submitter   TEXT    NOT NULL,
	created_at  INTEGER NOT NULL, -- Unix milliseconds, as are the other times
	sent_at     INTEGER,
	finished_at INTEGER,
	result      TEXT              -- wire.Result as JSON
);
CREATE INDEX IF NOT EXISTS jobs_status ON jobs (status, id);
CREATE INDEX IF NOT EXISTS jobs_created ON jobs (created_at);`

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// One connection: writes are rare, and it makes every statement atomic
	// with respect to the others.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating schema: %w", err)
	}
	return &Store{db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Add queues a job. job.ID is ignored.
func (s *Store) Add(ctx context.Context, job wire.Job, submitter string, now time.Time) (Job, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO jobs (kind, text, tape_mm, image, status, submitter, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		job.Kind, job.Text, job.TapeMM, job.Image, Queued, submitter, now.UnixMilli())
	if err != nil {
		return Job{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Job{}, err
	}
	return s.Get(ctx, id)
}

// Claim marks the oldest queued job sent and returns it, or nil if the queue
// is empty.
func (s *Store) Claim(ctx context.Context, now time.Time) (*wire.Job, error) {
	var j wire.Job
	err := s.db.QueryRowContext(ctx, `
		UPDATE jobs SET status = ?, sent_at = ?
		WHERE id = (SELECT id FROM jobs WHERE status = ? ORDER BY id LIMIT 1)
		RETURNING id, kind, text, tape_mm, image`,
		Sent, now.UnixMilli(), Queued).Scan(&j.ID, &j.Kind, &j.Text, &j.TapeMM, &j.Image)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// Finish records the board's result for a sent job. The image is dropped:
// nothing will print it again.
func (s *Store) Finish(ctx context.Context, id int64, r wire.Result, now time.Time) error {
	status := Failed
	if r.Printed {
		status = Done
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET status = ?, finished_at = ?, result = ?, image = NULL WHERE id = ? AND status = ?`,
		status, now.UnixMilli(), string(b), id, Sent)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.Get(ctx, id); err != nil {
			return err
		}
		return ErrNotSent
	}
	return nil
}

const columns = `id, kind, text, tape_mm, status, submitter, created_at, sent_at, finished_at, result`

func (s *Store) Get(ctx context.Context, id int64) (Job, error) {
	j, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return j, err
}

// List returns the newest jobs first.
func (s *Store) List(ctx context.Context, limit int) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM jobs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		j, err := scan(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// Prune deletes jobs created before cutoff and returns how many went.
func (s *Store) Prune(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE created_at < ?`, cutoff.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func scan(row interface{ Scan(...any) error }) (Job, error) {
	var (
		j              Job
		created        int64
		sent, finished sql.NullInt64
		result         sql.NullString
	)
	if err := row.Scan(&j.ID, &j.Kind, &j.Text, &j.TapeMM, &j.Status, &j.Submitter, &created, &sent, &finished, &result); err != nil {
		return Job{}, err
	}
	j.CreatedAt = time.UnixMilli(created).UTC()
	j.SentAt, j.FinishedAt = timePtr(sent), timePtr(finished)
	if result.Valid {
		j.Result = new(wire.Result)
		if err := json.Unmarshal([]byte(result.String), j.Result); err != nil {
			return Job{}, fmt.Errorf("job %d result: %w", j.ID, err)
		}
	}
	return j, nil
}

func timePtr(ms sql.NullInt64) *time.Time {
	if !ms.Valid {
		return nil
	}
	t := time.UnixMilli(ms.Int64).UTC()
	return &t
}
