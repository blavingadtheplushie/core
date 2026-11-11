package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"blavingad/internal/store"
)

type usage struct{ in, out int64 }

// relay copies a successful upstream answer to the client. SSE streams are
// forwarded line by line with a flush after each event; token usage is
// sniffed on the way.
func relay(w http.ResponseWriter, resp *http.Response) usage {
	copyHeaders(w, resp.Header)
	w.WriteHeader(resp.StatusCode)
	var u usage
	ct := resp.Header.Get("Content-Type")
	if !bytes.Contains([]byte(ct), []byte("event-stream")) {
		b, _ := io.ReadAll(resp.Body)
		w.Write(b)
		u.sniff(b)
		return u
	}
	fl, _ := w.(http.Flusher)
	rd := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		line, err := rd.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := w.Write(line); werr != nil {
				return u
			}
			if bytes.HasPrefix(line, []byte("data:")) {
				u.sniff(bytes.TrimSpace(line[5:]))
			}
			if fl != nil && len(bytes.TrimSpace(line)) == 0 {
				fl.Flush()
			}
		}
		if err != nil {
			if fl != nil {
				fl.Flush()
			}
			return u
		}
	}
}

// sniff reads usage from an OpenAI or Anthropic JSON payload (full response
// or one stream event). Later values overwrite earlier non-zero ones.
func (u *usage) sniff(b []byte) {
	if len(b) == 0 || b[0] != '{' {
		return
	}
	var v struct {
		Usage *struct {
			Prompt     int64 `json:"prompt_tokens"`
			Completion int64 `json:"completion_tokens"`
			Input      int64 `json:"input_tokens"`
			Output     int64 `json:"output_tokens"`
		} `json:"usage"`
		Message *struct {
			Usage *struct {
				Input  int64 `json:"input_tokens"`
				Output int64 `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(b, &v) != nil {
		return
	}
	if v.Message != nil && v.Message.Usage != nil {
		u.max(v.Message.Usage.Input, v.Message.Usage.Output)
	}
	if x := v.Usage; x != nil {
		u.max(x.Prompt+x.Input, x.Completion+x.Output)
	}
}

func (u *usage) max(in, out int64) {
	if in > u.in {
		u.in = in
	}
	if out > u.out {
		u.out = out
	}
}

func (p *Proxy) logUsage(keyID, upstream, model string, status int, stream bool, start time.Time, u usage, errMsg string) {
	s := 0
	if stream {
		s = 1
	}
	p.st.DB.Exec(`INSERT INTO llm_usage(ts, key_id, upstream, model, status, stream, latency_ms, input_tokens, output_tokens, error) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		store.Now(), keyID, upstream, model, status, s, time.Since(start).Milliseconds(), u.in, u.out, errMsg)
	if status >= 200 && status < 300 && keyID != "" {
		p.st.DB.Exec(`UPDATE llm_keys SET requests = requests + 1, input_tokens = input_tokens + ?, output_tokens = output_tokens + ?, last_used_at = ? WHERE id = ?`,
			u.in, u.out, store.Now(), keyID)
	}
}

// Stats summarises usage over the last `hours` per upstream.
func (p *Proxy) Stats(hours int) ([]map[string]any, error) {
	if hours <= 0 {
		hours = 24
	}
	since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339Nano)
	return p.st.Rows(`SELECT upstream, COUNT(*) AS attempts,
		SUM(CASE WHEN status BETWEEN 200 AND 299 THEN 1 ELSE 0 END) AS ok,
		SUM(input_tokens) AS input_tokens, SUM(output_tokens) AS output_tokens,
		CAST(AVG(latency_ms) AS INTEGER) AS avg_ms
		FROM llm_usage WHERE ts >= ? GROUP BY upstream ORDER BY attempts DESC`, since)
}
