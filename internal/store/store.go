package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB *sql.DB
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(dataDir, "blavingad.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	var version int
	if err := s.DB.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func NewID(prefix string) string {
	b := make([]byte, 9)
	rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func ToJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// FTSQuery turns free text into a safe FTS5 query: every token is quoted and
// prefix-matched, joined with OR so partial matches still rank.
func FTSQuery(text string) string {
	var parts []string
	for _, f := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r == '_' || r == '-' || r == '.' || r == '@' || r > 127 || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	}) {
		f = strings.Trim(f, "-.")
		if len([]rune(f)) < 2 {
			continue
		}
		parts = append(parts, `"`+strings.ReplaceAll(f, `"`, ``)+`"*`)
		if len(parts) >= 12 {
			break
		}
	}
	return strings.Join(parts, " OR ")
}

// ---------- settings ----------

func (s *Store) GetSetting(key, fallback string) string {
	var v string
	if err := s.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return fallback
	}
	return v
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO settings(key, value, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, Now())
	return err
}

func (s *Store) DeleteSetting(key string) error {
	_, err := s.DB.Exec(`DELETE FROM settings WHERE key = ?`, key)
	return err
}

func (s *Store) SettingsWithPrefix(prefix string) map[string]string {
	out := map[string]string{}
	rows, err := s.DB.Query(`SELECT key, value FROM settings WHERE key LIKE ?`, prefix+"%")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			out[k] = v
		}
	}
	return out
}

// ---------- audit ----------

func (s *Store) Audit(actor, action, target string, details any) {
	d := ""
	if details != nil {
		d = ToJSON(details)
	}
	s.DB.Exec(`INSERT INTO audit(actor, action, target, details, created_at) VALUES(?,?,?,?,?)`, actor, action, target, d, Now())
}

// Rows scans any query into a slice of maps — used by read-only admin
// endpoints where a struct per table would be pure ceremony.
func (s *Store) Rows(query string, args ...any) ([]map[string]any, error) {
	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			switch v := vals[i].(type) {
			case []byte:
				if c == "embedding" {
					continue
				}
				m[c] = string(v)
			default:
				m[c] = v
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Row(query string, args ...any) (map[string]any, error) {
	rows, err := s.Rows(query, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return rows[0], nil
}

func (s *Store) Count(query string, args ...any) int {
	var n int
	s.DB.QueryRow(query, args...).Scan(&n)
	return n
}
