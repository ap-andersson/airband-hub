// Package store keeps the hub's own data (bookmarks for now) in one SQLite file in the data
// directory. There is no database server: the file is opened by this process only.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ap-andersson/airband-hub/internal/airband"
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

	`CREATE TABLE transmissions (
		start_ms    INTEGER NOT NULL, -- Unix milliseconds
		end_ms      INTEGER NOT NULL,
		freq        TEXT    NOT NULL,
		label       TEXT    NOT NULL,
		duration_s  REAL    NOT NULL,
		snr_min_db  REAL    NOT NULL, -- both 0 when unknown
		snr_max_db  REAL    NOT NULL,
		approximate INTEGER NOT NULL  -- 1 when several ended within one scrape
	);
	CREATE INDEX transmissions_end ON transmissions (end_ms);`,

	// seconds per hour in which the hub had working metrics from rtl_airband, so averages
	// can leave out hours when the receiver was down
	`CREATE TABLE uptime (
		hour_ms INTEGER PRIMARY KEY, -- start of the hour, Unix milliseconds
		seconds REAL    NOT NULL
	);`,
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

// AddTransmissions stores transmissions as the tracker records them.
func (s *Store) AddTransmissions(ctx context.Context, txs []airband.Transmission) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range txs {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO transmissions (start_ms, end_ms, freq, label, duration_s, snr_min_db, snr_max_db, approximate) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			t.Start.UnixMilli(), t.End.UnixMilli(), t.Freq, t.Label, t.DurationSeconds, t.SNRMinDB, t.SNRMaxDB, t.Approximate); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Transmissions returns those that ended at or after since, oldest first.
func (s *Store) Transmissions(ctx context.Context, since time.Time) ([]airband.Transmission, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT start_ms, end_ms, freq, label, duration_s, snr_min_db, snr_max_db, approximate FROM transmissions WHERE end_ms >= ? ORDER BY end_ms, rowid",
		since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []airband.Transmission{}
	for rows.Next() {
		var t airband.Transmission
		var start, end int64
		if err := rows.Scan(&start, &end, &t.Freq, &t.Label, &t.DurationSeconds, &t.SNRMinDB, &t.SNRMaxDB, &t.Approximate); err != nil {
			return nil, err
		}
		t.Start, t.End = time.UnixMilli(start).UTC(), time.UnixMilli(end).UTC()
		out = append(out, t)
	}
	return out, rows.Err()
}

// PruneTransmissions deletes those that ended before before, and says how many.
func (s *Store) PruneTransmissions(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM transmissions WHERE end_ms < ?", before.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TransmissionsBetween returns those that started in [from, to), oldest first.
func (s *Store) TransmissionsBetween(ctx context.Context, from, to time.Time) ([]airband.Transmission, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT start_ms, end_ms, freq, label, duration_s, snr_min_db, snr_max_db, approximate FROM transmissions WHERE start_ms >= ? AND start_ms < ? ORDER BY start_ms, rowid",
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []airband.Transmission{}
	for rows.Next() {
		var t airband.Transmission
		var start, end int64
		if err := rows.Scan(&start, &end, &t.Freq, &t.Label, &t.DurationSeconds, &t.SNRMinDB, &t.SNRMaxDB, &t.Approximate); err != nil {
			return nil, err
		}
		t.Start, t.End = time.UnixMilli(start).UTC(), time.UnixMilli(end).UTC()
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddUptime adds seconds of working reception to the hour starting at hour.
func (s *Store) AddUptime(ctx context.Context, hour time.Time, seconds float64) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO uptime (hour_ms, seconds) VALUES (?, MIN(3600, ?)) ON CONFLICT (hour_ms) DO UPDATE SET seconds = MIN(3600, seconds + excluded.seconds)",
		hour.UnixMilli(), seconds)
	return err
}

// Uptime returns the seconds of working reception per hour, for hours starting in [from, to).
func (s *Store) Uptime(ctx context.Context, from, to time.Time) (map[time.Time]float64, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT hour_ms, seconds FROM uptime WHERE hour_ms >= ? AND hour_ms < ?", from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[time.Time]float64{}
	for rows.Next() {
		var ms int64
		var sec float64
		if err := rows.Scan(&ms, &sec); err != nil {
			return nil, err
		}
		out[time.UnixMilli(ms).UTC()] = sec
	}
	return out, rows.Err()
}

// PruneUptime deletes the hours before before.
func (s *Store) PruneUptime(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM uptime WHERE hour_ms < ?", before.UnixMilli())
	return err
}

// Frequency is a frequency seen in the stored transmissions, with its latest label.
type Frequency struct {
	Freq  string `json:"freq"`
	Label string `json:"label"`
}

// Frequencies returns every frequency in the stored transmissions, ordered by frequency.
func (s *Store) Frequencies(ctx context.Context) ([]Frequency, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT freq, label FROM transmissions t WHERE rowid = (SELECT rowid FROM transmissions WHERE freq = t.freq ORDER BY end_ms DESC LIMIT 1) ORDER BY CAST(freq AS REAL)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Frequency{}
	for rows.Next() {
		var f Frequency
		if err := rows.Scan(&f.Freq, &f.Label); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// HistoryStart returns when the stored transmissions and the uptime record begin; zero times
// when there are none.
func (s *Store) HistoryStart(ctx context.Context) (transmissions, uptime time.Time, err error) {
	var tx, up sql.NullInt64
	if err = s.db.QueryRowContext(ctx, "SELECT MIN(start_ms) FROM transmissions").Scan(&tx); err != nil {
		return
	}
	if err = s.db.QueryRowContext(ctx, "SELECT MIN(hour_ms) FROM uptime").Scan(&up); err != nil {
		return
	}
	if tx.Valid {
		transmissions = time.UnixMilli(tx.Int64).UTC()
	}
	if up.Valid {
		uptime = time.UnixMilli(up.Int64).UTC()
	}
	return
}
