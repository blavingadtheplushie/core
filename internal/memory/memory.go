// Package memory implements BLAVINGAD's long-term memory: typed facts with
// hybrid retrieval (SQLite FTS5 bm25 + cosine similarity over embeddings,
// fused with reciprocal rank fusion), near-duplicate merging, people
// profiles and channel digests.
package memory

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"blavingad/internal/llm"
	"blavingad/internal/store"
)

var Kinds = []string{"fact", "preference", "person", "project", "lesson", "procedure", "event", "note"}

type Memory struct {
	ID         int64   `json:"id"`
	Kind       string  `json:"kind"`
	Subject    string  `json:"subject"`
	Content    string  `json:"content"`
	Tags       string  `json:"tags"`
	Importance int     `json:"importance"`
	Source     string  `json:"source"`
	UseCount   int     `json:"use_count"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
	Score      float64 `json:"score,omitempty"`
}

type Service struct {
	st    *store.Store
	llm   *llm.Client
	model string

	mu      sync.RWMutex
	vectors map[int64][]float32
	loaded  bool
}

func New(st *store.Store, client *llm.Client, embedModel string) *Service {
	return &Service{st: st, llm: client, model: embedModel, vectors: map[int64][]float32{}}
}

func (s *Service) VectorsEnabled() bool { return s.llm != nil && s.model != "" }

func (s *Service) embed(ctx context.Context, text string) []float32 {
	if !s.VectorsEnabled() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := s.llm.Embed(ctx, s.model, []string{text})
	if err != nil || len(out) == 0 {
		if err != nil {
			slog.Warn("embedding failed", "err", err)
		}
		return nil
	}
	return out[0]
}

func encode(v []float32) []byte {
	if len(v) == 0 {
		return nil
	}
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(x))
	}
	return b
}

func decode(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot
}

func (s *Service) loadVectors() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return
	}
	rows, err := s.st.DB.Query(`SELECT id, embedding FROM memories WHERE embedding IS NOT NULL`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var b []byte
		if rows.Scan(&id, &b) == nil && len(b) > 0 {
			s.vectors[id] = decode(b)
		}
	}
	s.loaded = true
}

// Backfill embeds memories saved while no embedding model was configured.
func (s *Service) Backfill(ctx context.Context) {
	if !s.VectorsEnabled() {
		return
	}
	rows, err := s.st.DB.Query(`SELECT id, subject, content FROM memories WHERE embedding IS NULL LIMIT 500`)
	if err != nil {
		return
	}
	type item struct {
		id   int64
		text string
	}
	var items []item
	for rows.Next() {
		var it item
		var subj, content string
		rows.Scan(&it.id, &subj, &content)
		it.text = strings.TrimSpace(subj + "\n" + content)
		items = append(items, it)
	}
	rows.Close()
	for _, it := range items {
		if v := s.embed(ctx, it.text); v != nil {
			s.st.DB.Exec(`UPDATE memories SET embedding = ? WHERE id = ?`, encode(v), it.id)
			s.mu.Lock()
			s.vectors[it.id] = v
			s.mu.Unlock()
		}
	}
	if len(items) > 0 {
		slog.Info("memory embeddings backfilled", "count", len(items))
	}
}

type SaveResult struct {
	ID     int64 `json:"id"`
	Merged bool  `json:"merged"`
}

// Save stores a memory, merging into an existing one when it is a near
// duplicate (same kind, cosine ≥ 0.9) so the store does not fill with
// paraphrases of the same fact.
func (s *Service) Save(ctx context.Context, m Memory) (SaveResult, error) {
	m.Content = strings.TrimSpace(m.Content)
	if m.Content == "" {
		return SaveResult{}, errors.New("contenu vide")
	}
	if m.Kind == "" {
		m.Kind = "fact"
	}
	if m.Importance < 1 || m.Importance > 5 {
		m.Importance = 3
	}
	vec := s.embed(ctx, strings.TrimSpace(m.Subject+"\n"+m.Content))
	now := store.Now()
	if vec != nil {
		s.loadVectors()
		var bestID int64
		var best float64
		s.mu.RLock()
		for id, v := range s.vectors {
			if c := cosine(vec, v); c > best {
				best, bestID = c, id
			}
		}
		s.mu.RUnlock()
		if best >= 0.9 {
			var kind string
			if s.st.DB.QueryRow(`SELECT kind FROM memories WHERE id = ?`, bestID).Scan(&kind) == nil && kind == m.Kind {
				_, err := s.st.DB.Exec(`UPDATE memories SET content = ?, subject = COALESCE(NULLIF(?, ''), subject), tags = COALESCE(NULLIF(?, ''), tags),
					importance = MAX(importance, ?), embedding = ?, updated_at = ? WHERE id = ?`,
					m.Content, m.Subject, m.Tags, m.Importance, encode(vec), now, bestID)
				if err == nil {
					s.mu.Lock()
					s.vectors[bestID] = vec
					s.mu.Unlock()
				}
				return SaveResult{ID: bestID, Merged: true}, err
			}
		}
	} else {
		// Without vectors, at least avoid exact duplicates.
		var id int64
		if s.st.DB.QueryRow(`SELECT id FROM memories WHERE kind = ? AND content = ?`, m.Kind, m.Content).Scan(&id) == nil {
			s.st.DB.Exec(`UPDATE memories SET updated_at = ? WHERE id = ?`, now, id)
			return SaveResult{ID: id, Merged: true}, nil
		}
	}
	res, err := s.st.DB.Exec(`INSERT INTO memories(kind, subject, content, tags, importance, source, embedding, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, m.Kind, m.Subject, m.Content, m.Tags, m.Importance, m.Source, encode(vec), now, now)
	if err != nil {
		return SaveResult{}, err
	}
	id, _ := res.LastInsertId()
	if vec != nil {
		s.mu.Lock()
		s.vectors[id] = vec
		s.mu.Unlock()
	}
	return SaveResult{ID: id}, nil
}

func (s *Service) Update(ctx context.Context, id int64, m Memory) error {
	cur, err := s.Get(id)
	if err != nil {
		return err
	}
	if m.Content != "" {
		cur.Content = m.Content
	}
	if m.Subject != "" {
		cur.Subject = m.Subject
	}
	if m.Kind != "" {
		cur.Kind = m.Kind
	}
	if m.Tags != "" {
		cur.Tags = m.Tags
	}
	if m.Importance >= 1 && m.Importance <= 5 {
		cur.Importance = m.Importance
	}
	vec := s.embed(ctx, strings.TrimSpace(cur.Subject+"\n"+cur.Content))
	_, err = s.st.DB.Exec(`UPDATE memories SET kind=?, subject=?, content=?, tags=?, importance=?, embedding=COALESCE(?, embedding), updated_at=? WHERE id=?`,
		cur.Kind, cur.Subject, cur.Content, cur.Tags, cur.Importance, encode(vec), store.Now(), id)
	if err == nil && vec != nil {
		s.mu.Lock()
		s.vectors[id] = vec
		s.mu.Unlock()
	}
	return err
}

func (s *Service) Delete(id int64) error {
	res, err := s.st.DB.Exec(`DELETE FROM memories WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	s.mu.Lock()
	delete(s.vectors, id)
	s.mu.Unlock()
	return nil
}

const cols = `id, kind, COALESCE(subject,''), content, COALESCE(tags,''), importance, COALESCE(source,''), use_count, created_at, updated_at`

func scan(rows interface{ Scan(...any) error }) (Memory, error) {
	var m Memory
	err := rows.Scan(&m.ID, &m.Kind, &m.Subject, &m.Content, &m.Tags, &m.Importance, &m.Source, &m.UseCount, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

func (s *Service) Get(id int64) (Memory, error) {
	return scan(s.st.DB.QueryRow(`SELECT `+cols+` FROM memories WHERE id = ?`, id))
}

func (s *Service) List(query, kind string, limit, offset int) ([]Memory, int, error) {
	where := []string{"1=1"}
	args := []any{}
	if kind != "" {
		where = append(where, "kind = ?")
		args = append(args, kind)
	}
	if q := store.FTSQuery(query); q != "" {
		where = append(where, "id IN (SELECT rowid FROM memories_fts WHERE memories_fts MATCH ?)")
		args = append(args, q)
	}
	w := strings.Join(where, " AND ")
	total := s.st.Count(`SELECT COUNT(*) FROM memories WHERE `+w, args...)
	rows, err := s.st.DB.Query(`SELECT `+cols+` FROM memories WHERE `+w+` ORDER BY updated_at DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Memory{}
	for rows.Next() {
		if m, err := scan(rows); err == nil {
			out = append(out, m)
		}
	}
	return out, total, nil
}

// Search runs hybrid retrieval and returns the best memories for a query.
func (s *Service) Search(ctx context.Context, query, kind string, limit int) []Memory {
	if limit <= 0 {
		limit = 8
	}
	scores := map[int64]float64{}
	const k = 60.0

	if q := store.FTSQuery(query); q != "" {
		sqlq := `SELECT m.id FROM memories_fts f JOIN memories m ON m.id = f.rowid WHERE memories_fts MATCH ?`
		args := []any{q}
		if kind != "" {
			sqlq += ` AND m.kind = ?`
			args = append(args, kind)
		}
		sqlq += ` ORDER BY bm25(memories_fts) LIMIT 30`
		if rows, err := s.st.DB.Query(sqlq, args...); err == nil {
			rank := 0
			for rows.Next() {
				var id int64
				rows.Scan(&id)
				scores[id] += 1 / (k + float64(rank))
				rank++
			}
			rows.Close()
		}
	}

	if vec := s.embed(ctx, query); vec != nil {
		s.loadVectors()
		type pair struct {
			id  int64
			sim float64
		}
		var sims []pair
		s.mu.RLock()
		for id, v := range s.vectors {
			if c := cosine(vec, v); c > 0.25 {
				sims = append(sims, pair{id, c})
			}
		}
		s.mu.RUnlock()
		sort.Slice(sims, func(i, j int) bool { return sims[i].sim > sims[j].sim })
		for rank, p := range sims {
			if rank >= 30 {
				break
			}
			scores[p.id] += 1.2 / (k + float64(rank))
		}
	}
	if len(scores) == 0 {
		return nil
	}
	var out []Memory
	for id, score := range scores {
		m, err := s.Get(id)
		if err != nil || (kind != "" && m.Kind != kind) {
			continue
		}
		boost := 1 + 0.08*float64(m.Importance-3)
		if t, err := time.Parse(time.RFC3339Nano, m.UpdatedAt); err == nil {
			age := time.Since(t).Hours() / 24
			boost *= 1 + 0.15*math.Exp(-age/30)
		}
		m.Score = score * boost
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	ids := make([]any, 0, len(out))
	for _, m := range out {
		ids = append(ids, m.ID)
	}
	if len(ids) > 0 {
		s.st.DB.Exec(`UPDATE memories SET use_count = use_count + 1, last_used_at = ? WHERE id IN (`+placeholders(len(ids))+`)`, append([]any{store.Now()}, ids...)...)
	}
	return out
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func (s *Service) Stats() map[string]int {
	out := map[string]int{}
	rows, err := s.st.DB.Query(`SELECT kind, COUNT(*) FROM memories GROUP BY kind`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		rows.Scan(&k, &n)
		out[k] = n
	}
	return out
}

// ---------- people ----------

type Person struct {
	SlackID      string `json:"slack_id"`
	Name         string `json:"name"`
	RealName     string `json:"real_name"`
	Title        string `json:"title"`
	Email        string `json:"email"`
	TZ           string `json:"tz"`
	Notes        string `json:"notes"`
	MessageCount int    `json:"message_count"`
	LastSeenAt   string `json:"last_seen_at"`
}

func (s *Service) UpsertPerson(p Person) {
	s.st.DB.Exec(`INSERT INTO people(slack_id, name, real_name, title, email, tz, updated_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(slack_id) DO UPDATE SET name=excluded.name, real_name=excluded.real_name, title=excluded.title,
		email=excluded.email, tz=excluded.tz, updated_at=excluded.updated_at`,
		p.SlackID, p.Name, p.RealName, p.Title, p.Email, p.TZ, store.Now())
}

func (s *Service) SeenPerson(slackID string) {
	s.st.DB.Exec(`UPDATE people SET message_count = message_count + 1, last_seen_at = ? WHERE slack_id = ?`, store.Now(), slackID)
}

func (s *Service) GetPerson(slackID string) (Person, bool) {
	var p Person
	err := s.st.DB.QueryRow(`SELECT slack_id, COALESCE(name,''), COALESCE(real_name,''), COALESCE(title,''), COALESCE(email,''), COALESCE(tz,''), notes, message_count, COALESCE(last_seen_at,'')
		FROM people WHERE slack_id = ?`, slackID).Scan(&p.SlackID, &p.Name, &p.RealName, &p.Title, &p.Email, &p.TZ, &p.Notes, &p.MessageCount, &p.LastSeenAt)
	return p, err == nil
}

// SetPersonNotes replaces the agent's free-form notes about someone.
func (s *Service) SetPersonNotes(slackID, notes string) error {
	if len(notes) > 4000 {
		notes = notes[len(notes)-4000:]
	}
	res, err := s.st.DB.Exec(`UPDATE people SET notes = ?, updated_at = ? WHERE slack_id = ?`, strings.TrimSpace(notes), store.Now(), slackID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err = s.st.DB.Exec(`INSERT INTO people(slack_id, notes, updated_at) VALUES(?,?,?)`, slackID, notes, store.Now())
	}
	return err
}

func (s *Service) AppendPersonNote(slackID, note string) error {
	p, _ := s.GetPerson(slackID)
	notes := strings.TrimSpace(p.Notes)
	line := "- " + strings.TrimSpace(note)
	if strings.Contains(notes, strings.TrimSpace(note)) {
		return nil
	}
	if notes != "" {
		notes += "\n"
	}
	return s.SetPersonNotes(slackID, notes+line)
}

// ---------- channels ----------

func (s *Service) ChannelDigest(channel string) string {
	var d string
	s.st.DB.QueryRow(`SELECT digest FROM channels WHERE id = ?`, channel).Scan(&d)
	return d
}
