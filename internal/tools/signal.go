package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"blavingad/internal/core"
)

// Signal goes through a signal-cli-rest-api container (json-rpc mode) that
// listens on the loopback of the LXC. Group joins by invite link are not in
// the REST API, so they use signal-cli's JSON-RPC socket inside the container.
func signalBase() string {
	return strings.TrimRight(firstNonEmpty(os.Getenv("SIGNAL_API_URL"), "http://127.0.0.1:8085"), "/")
}
func signalNumber() string { return firstNonEmpty(os.Getenv("SIGNAL_NUMBER"), "+15103287661") }
func signalContainer() string {
	return firstNonEmpty(os.Getenv("SIGNAL_CONTAINER"), "signal-api")
}

var signalHTTP = &http.Client{Timeout: 60 * time.Second}

func signalDo(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, signalBase()+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := signalHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("signal api unreachable: %w", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return out, fmt.Errorf("signal api %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return out, nil
}

type signalGroup struct {
	Name       string   `json:"name"`
	ID         string   `json:"id"`
	InternalID string   `json:"internal_id"`
	Members    []string `json:"members"`
	Admins     []string `json:"admins"`
	Blocked    bool     `json:"blocked"`
	InviteLink string   `json:"invite_link"`
}

func signalGroups(ctx context.Context) ([]signalGroup, error) {
	b, err := signalDo(ctx, "GET", "/v1/groups/"+url.PathEscape(signalNumber()), nil)
	if err != nil {
		return nil, err
	}
	var gs []signalGroup
	if err := json.Unmarshal(b, &gs); err != nil {
		return nil, fmt.Errorf("decode groups: %w", err)
	}
	return gs, nil
}

var rePhone = regexp.MustCompile(`^\+[1-9][0-9]{6,15}$`)

// signalRecipient normalises a recipient: phone number, "group.…" id, or a
// group name (resolved against listGroups).
func signalRecipient(ctx context.Context, to string) (string, error) {
	to = strings.TrimSpace(to)
	clean := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "").Replace(to)
	switch {
	case to == "":
		return "", errors.New("empty recipient")
	case strings.HasPrefix(to, "group."):
		return to, nil
	case rePhone.MatchString(clean):
		return clean, nil
	}
	gs, err := signalGroups(ctx)
	if err != nil {
		return "", err
	}
	for _, g := range gs {
		if strings.EqualFold(g.Name, to) || g.InternalID == to {
			return g.ID, nil
		}
	}
	return "", fmt.Errorf("unknown recipient %q: give a +E.164 number, a group.… id, or an exact group name (see signal_groups)", to)
}

// SignalSend is exported so the inbound bridge can reuse it.
func SignalSend(ctx context.Context, to, text string) (string, error) {
	rcpt, err := signalRecipient(ctx, to)
	if err != nil {
		return "", err
	}
	b, err := signalDo(ctx, "POST", "/v2/send", map[string]any{
		"number": signalNumber(), "recipients": []string{rcpt}, "message": text, "text_mode": "styled",
	})
	if err != nil {
		return "", err
	}
	var r struct {
		Timestamp string `json:"timestamp"`
	}
	_ = json.Unmarshal(b, &r)
	return fmt.Sprintf("Sent to %s (timestamp %s).", rcpt, r.Timestamp), nil
}

// signalRPC calls signal-cli's JSON-RPC socket inside the container.
func signalRPC(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	if params == nil {
		params = map[string]any{}
	}
	params["account"] = signalNumber()
	id := fmt.Sprintf("blav-%d", time.Now().UnixNano())
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	script := `exec 3<>/dev/tcp/127.0.0.1/6001 || exit 3; head -n1 >&3; timeout 55 grep -m1 --line-buffered '"id":"` + id + `"' <&3`
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", signalContainer(), "bash", "-c", script)
	cmd.Stdin = bytes.NewReader(append(req, '\n'))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("signal-cli rpc %s: %v: %s", method, err, strings.TrimSpace(string(out)))
	}
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &r); err != nil {
		return nil, fmt.Errorf("signal-cli rpc %s: bad reply: %s", method, Clip(string(out), 500))
	}
	if r.Error != nil {
		return nil, fmt.Errorf("signal-cli %s: %s (code %d)", method, r.Error.Message, r.Error.Code)
	}
	return r.Result, nil
}

func registerSignal(d *Deps) {
	d.Reg.Add(&core.Tool{
		Name: "signal_status", Category: "signal", Emoji: "📡",
		Description: "Shows Blavingad's Signal account status: number, signal-cli version, registration, number of groups.",
		Params:      core.Obj(map[string]any{}),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			about, err := signalDo(ctx, "GET", "/v1/about", nil)
			if err != nil {
				return "", err
			}
			accs, _ := signalDo(ctx, "GET", "/v1/accounts", nil)
			gs, gerr := signalGroups(ctx)
			lines := []string{
				"Number: " + signalNumber(),
				"API: " + strings.TrimSpace(string(about)),
				"Registered accounts: " + strings.TrimSpace(string(accs)),
			}
			if gerr != nil {
				lines = append(lines, "Groups: error "+gerr.Error())
			} else {
				lines = append(lines, fmt.Sprintf("Groups: %d", len(gs)))
			}
			return strings.Join(lines, "\n"), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "signal_groups", Category: "signal", Emoji: "👥",
		Description: "Lists the Signal groups Blavingad is in (name, group id to use with signal_send, members count, invite link).",
		Params:      core.Obj(map[string]any{"members": core.Bool("Include member numbers (default false).")}),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			gs, err := signalGroups(ctx)
			if err != nil {
				return "", err
			}
			if len(gs) == 0 {
				return "Not in any Signal group yet.", nil
			}
			a, _ := core.Parse[struct{ Members bool }](args)
			var lines []string
			for _, g := range gs {
				l := fmt.Sprintf("• %s — %s (%d members, %d admins)", g.Name, g.ID, len(g.Members), len(g.Admins))
				if g.Blocked {
					l += " [blocked]"
				}
				if g.InviteLink != "" {
					l += " link: " + g.InviteLink
				}
				if a.Members {
					l += "\n  members: " + strings.Join(g.Members, ", ")
				}
				lines = append(lines, l)
			}
			return Clip(strings.Join(lines, "\n"), 0), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "signal_send", Category: "signal", Emoji: "📨", Risk: core.RiskSensitive,
		Description: "Sends a Signal message from Blavingad's number to a person (+E.164 phone number) or a group (group.… id from signal_groups, or exact group name). Supports **bold**, *italic*, ~strike~, `mono`.",
		Params: core.Obj(map[string]any{
			"to":   core.Str("Phone number (+33…) or group id/name."),
			"text": core.Str("Message text."),
		}, "to", "text"),
		Summarize: summarizeField("Signal → ", "to", 40),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ To, Text string }](args)
			if strings.TrimSpace(a.Text) == "" {
				return "", errors.New("empty text")
			}
			return SignalSend(ctx, a.To, a.Text)
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "signal_join", Category: "signal", Emoji: "🚪", Risk: core.RiskSensitive,
		Description: "Joins a Signal group from an invite link (https://signal.group/#…).",
		Params:      core.Obj(map[string]any{"link": core.Str("Group invite link (https://signal.group/#…).")}, "link"),
		Summarize:   summarizeField("Joining Signal group ", "link", 40),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			link := strings.TrimSpace(field(args, "link"))
			if !strings.HasPrefix(link, "https://signal.group/") && !strings.HasPrefix(link, "sgnl://") {
				return "", errors.New("not a Signal group link (expected https://signal.group/#…)")
			}
			res, err := signalRPC(ctx, "joinGroup", map[string]any{"uri": link})
			if err != nil {
				return "", err
			}
			return "Joined (or join requested, if the group needs admin approval): " + Clip(string(res), 2000), nil
		},
	})
}
