// Package store keeps the hub's own data (bookmarks for now) in one SQLite file in the data
// directory. There is no database server: the file is opened by this process only.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// migrations[i] moves the schema from version i to i+1. Append only; never edit a released one.
var migrations = []string{
	`CREATE TABLE bookmarks (
		id         INTEGER PRIMARY KEY AUTOINCREMENT, -- never reused, so an old shared link cannot open a newer bookmark
		name       TEXT    NOT NULL,
		path       TEXT    NOT NULL, -- recording, relative to the recordings directory, without .tmp
		offset_s   REAL    NOT NULL, -- seconds into the recording
		at         INTEGER NOT NULL, -- the moment itself, Unix milliseconds
		freq       TEXT    NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL  -- Unix milliseconds
	);
	CREATE INDEX bookmarks_at ON bookmarks (at);`,
}

type Store struct {
	db *sql.DB
}

// Open opens (or creates) the database file and brings its schema up to date.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	// one connection: writes are rare and tiny, and it rules out SQLITE_BUSY between our own connections
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema %d is newer than this hub (%d); was it downgraded?", version, len(migrations))
	}
	for v := version; v < len(migrations); v++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Bookmark is a named moment in a recording.
type Bookmark struct {
	ID      int64     `json:"id"`
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Offset  float64   `json:"offset"` // seconds into the recording
	At      time.Time `json:"at"`
	Freq    string    `json:"freq,omitempty"`
	Created time.Time `json:"created"`
}

var ErrNotFound = errors.New("not found")

const bookmarkCols = "id, name, path, offset_s, at, freq, created_at"

func scanBookmark(row interface{ Scan(...any) error }) (Bookmark, error) {
	var b Bookmark
	var at, created int64
	if err := row.Scan(&b.ID, &b.Name, &b.Path, &b.Offset, &at, &b.Freq, &created); err != nil {
		return Bookmark{}, err
	}
	b.At, b.Created = time.UnixMilli(at).UTC(), time.UnixMilli(created).UTC()
	return b, nil
}

// AddBookmark stores b and returns it with its ID and creation time set.
func (s *Store) AddBookmark(ctx context.Context, b Bookmark) (Bookmark, error) {
	b.Created = time.Now().UTC().Truncate(time.Millisecond)
	b.At = b.At.UTC().Truncate(time.Millisecond)
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO bookmarks (name, path, offset_s, at, freq, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		b.Name, b.Path, b.Offset, b.At.UnixMilli(), b.Freq, b.Created.UnixMilli())
	if err != nil {
		return Bookmark{}, err
	}
	b.ID, err = res.LastInsertId()
	return b, err
}

// Bookmarks returns every bookmark, the most recent moment first.
func (s *Store) Bookmarks(ctx context.Context) ([]Bookmark, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+bookmarkCols+" FROM bookmarks ORDER BY at DESC, id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Bookmark{}
	for rows.Next() {
		b, err := scanBookmark(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) Bookmark(ctx context.Context, id int64) (Bookmark, error) {
	b, err := scanBookmark(s.db.QueryRowContext(ctx, "SELECT "+bookmarkCols+" FROM bookmarks WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Bookmark{}, ErrNotFound
	}
	return b, err
}

func (s *Store) RenameBookmark(ctx context.Context, id int64, name string) error {
	return s.changeOne(ctx, "UPDATE bookmarks SET name = ? WHERE id = ?", name, id)
}

func (s *Store) DeleteBookmark(ctx context.Context, id int64) error {
	return s.changeOne(ctx, "DELETE FROM bookmarks WHERE id = ?", id)
}

func (s *Store) changeOne(ctx context.Context, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}
