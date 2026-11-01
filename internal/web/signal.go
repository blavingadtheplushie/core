package web

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

// signalIntegration pings the local signal-cli-rest-api container.
func signalIntegration() map[string]any {
	base := strings.TrimRight(firstNonEmpty(os.Getenv("SIGNAL_API_URL"), "http://127.0.0.1:8085"), "/")
	number := firstNonEmpty(os.Getenv("SIGNAL_NUMBER"), "+15103287661")
	row := map[string]any{"key": "signal", "label": "Signal", "emoji": "📡", "ok": false, "configured": true, "detail": "signal-cli unreachable"}
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(base + "/v1/about")
	if err != nil {
		return row
	}
	defer resp.Body.Close()
	var about struct {
		Version string `json:"version"`
		Mode    string `json:"mode"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&about)
	if resp.StatusCode == 200 {
		row["ok"] = true
		row["detail"] = number + " · signal-cli-rest-api " + about.Version + " (" + about.Mode + ")"
	}
	return row
}
