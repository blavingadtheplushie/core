package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"blavingad/internal/core"
)

// Discord: a bot application that people install as a *user app*
// (integration_type 1). The bot token lives in the vault; the inbound gateway
// bridge is in internal/discord. A user-installed app may DM the people who
// installed it even without a mutual server.
const discordTokenRef = "vault://DISCORD_BOT_TOKEN/password"

var discordAPI = "https://discord.com/api/v10"

var discordHTTP = &http.Client{Timeout: 60 * time.Second}

// DiscordToken resolves the bot token (set by registerDiscord).
var DiscordToken = func(ctx context.Context) (string, error) {
	return "", errors.New("discord not wired")
}

// DiscordState is updated by the gateway bridge.
var DiscordState struct {
	Connected atomic.Bool
	BotID     atomic.Value // string
	BotName   atomic.Value // string
	AppID     atomic.Value // string
	LastError atomic.Value // string
}

func loadStr(v *atomic.Value) string {
	s, _ := v.Load().(string)
	return s
}

func DiscordAppID() string   { return loadStr(&DiscordState.AppID) }
func DiscordBotName() string { return loadStr(&DiscordState.BotName) }
func DiscordBotID() string   { return loadStr(&DiscordState.BotID) }
func DiscordLastError() string {
	return loadStr(&DiscordState.LastError)
}

// DiscordInstallURL is the user-install link of the app.
func DiscordInstallURL(appID string) string {
	if appID == "" {
		return ""
	}
	return "https://discord.com/oauth2/authorize?client_id=" + appID + "&integration_type=1&scope=applications.commands"
}

// DiscordDo calls the REST API with the bot token (one retry on 429).
func DiscordDo(ctx context.Context, method, path string, body any) ([]byte, error) {
	tok, err := DiscordToken(ctx)
	if err != nil {
		return nil, err
	}
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	for attempt := 0; ; attempt++ {
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, discordAPI+path, rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bot "+tok)
		req.Header.Set("User-Agent", "DiscordBot (https://github.com/blavingadtheplushie/core, 1.0)")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := discordHTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("discord api unreachable: %w", err)
		}
		out, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode == 429 && attempt < 2 {
			var rl struct {
				RetryAfter float64 `json:"retry_after"`
			}
			json.Unmarshal(out, &rl)
			wait := time.Duration(rl.RetryAfter*1000) * time.Millisecond
			if wait <= 0 || wait > 30*time.Second {
				wait = 2 * time.Second
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		if resp.StatusCode >= 300 {
			return out, discordError(method, path, resp.StatusCode, out)
		}
		return out, nil
	}
}

func discordError(method, path string, status int, out []byte) error {
	var e struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	json.Unmarshal(out, &e)
	hint := ""
	switch e.Code {
	case 50007:
		hint = " — this person must install the app as a user app (or share a server with the bot) and not block DMs from it"
	case 10013:
		hint = " — unknown user: check the numeric Discord ID"
	case 50001, 50013:
		hint = " — the bot has no access to this channel"
	}
	if e.Message != "" {
		return fmt.Errorf("discord %s %s: HTTP %d: %s (code %d)%s", method, path, status, e.Message, e.Code, hint)
	}
	return fmt.Errorf("discord %s %s: HTTP %d: %s", method, path, status, Clip(strings.TrimSpace(string(out)), 400))
}

// SplitDiscord cuts a message into ≤2000-character chunks, on line breaks
// when possible.
func SplitDiscord(text string, max int) []string {
	if max <= 0 {
		max = 1990
	}
	var out []string
	r := []rune(strings.TrimSpace(text))
	for len(r) > max {
		cut := max
		for i := max; i > max/2; i-- {
			if r[i] == '\n' {
				cut = i
				break
			}
		}
		out = append(out, strings.TrimSpace(string(r[:cut])))
		r = []rune(strings.TrimSpace(string(r[cut:])))
	}
	if len(r) > 0 {
		out = append(out, string(r))
	}
	return out
}

// DiscordSendChannel posts text (split if needed) in a channel; returns the
// id of the last message.
func DiscordSendChannel(ctx context.Context, channelID, text string) (string, error) {
	var last string
	for _, part := range SplitDiscord(text, 0) {
		b, err := DiscordDo(ctx, "POST", "/channels/"+channelID+"/messages", map[string]any{
			"content": part, "allowed_mentions": map[string]any{"parse": []string{}},
		})
		if err != nil {
			return last, err
		}
		var m struct {
			ID string `json:"id"`
		}
		json.Unmarshal(b, &m)
		last = m.ID
	}
	return last, nil
}

// DiscordDMChannel opens (or reuses) the DM channel with a user.
func DiscordDMChannel(ctx context.Context, userID string) (string, error) {
	b, err := DiscordDo(ctx, "POST", "/users/@me/channels", map[string]any{"recipient_id": userID})
	if err != nil {
		return "", err
	}
	var c struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(b, &c) != nil || c.ID == "" {
		return "", errors.New("discord: could not open DM channel")
	}
	return c.ID, nil
}

func DiscordTyping(ctx context.Context, channelID string) {
	DiscordDo(ctx, "POST", "/channels/"+channelID+"/typing", nil)
}

var reSnowflake = regexp.MustCompile(`^[0-9]{15,21}$`)

func cleanSnowflake(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimSuffix(s, ">"), "<@")
	s = strings.TrimPrefix(s, "!")
	return s
}

// DiscordLinksKey maps Discord user IDs to Slack IDs (setting discord.links).
const DiscordLinksKey = "discord.links"

func registerDiscord(d *Deps) {
	DiscordToken = func(ctx context.Context) (string, error) {
		if !d.vaultReady() {
			return "", errors.New("no Vaultwarden vault is configured (the Discord bot token lives in vault item DISCORD_BOT_TOKEN)")
		}
		k, err := d.resolveRef(ctx, discordTokenRef)
		if err != nil || strings.TrimSpace(k) == "" {
			if rerr := d.Vault.Refresh(ctx); rerr == nil {
				k, err = d.resolveRef(ctx, discordTokenRef)
			}
		}
		k = strings.TrimSpace(k)
		if err != nil || k == "" {
			return "", fmt.Errorf("no Discord bot token: put it in the Password field of the vault item DISCORD_BOT_TOKEN (%v)", err)
		}
		return k, nil
	}

	d.Reg.Add(&core.Tool{
		Name: "discord_status", Category: "discord", Emoji: "🎮",
		Description: "Shows Blavingad's Discord bot status: gateway connection, bot account, user-install count and the install link (user app).",
		Params:      core.Obj(map[string]any{}),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			b, err := DiscordDo(ctx, "GET", "/applications/@me", nil)
			if err != nil {
				return "", err
			}
			var app struct {
				ID                      string         `json:"id"`
				Name                    string         `json:"name"`
				ApproximateUserInstalls int            `json:"approximate_user_install_count"`
				ApproximateGuildCount   int            `json:"approximate_guild_count"`
				IntegrationTypesConfig  map[string]any `json:"integration_types_config"`
				Bot                     struct {
					Username string `json:"username"`
					ID       string `json:"id"`
				} `json:"bot"`
			}
			json.Unmarshal(b, &app)
			lines := []string{
				fmt.Sprintf("App: %s (%s), bot @%s (%s)", app.Name, app.ID, app.Bot.Username, app.Bot.ID),
				fmt.Sprintf("Gateway connected: %v", DiscordState.Connected.Load()),
				fmt.Sprintf("User installs: ~%d · servers: ~%d", app.ApproximateUserInstalls, app.ApproximateGuildCount),
				"User-install link: " + DiscordInstallURL(app.ID),
			}
			if _, ok := app.IntegrationTypesConfig["1"]; !ok {
				lines = append(lines, "Warning: User Install is not enabled in the Developer Portal (Installation → Installation Contexts → User Install).")
			}
			if e := DiscordLastError(); e != "" && !DiscordState.Connected.Load() {
				lines = append(lines, "Last gateway error: "+e)
			}
			return strings.Join(lines, "\n"), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "discord_send", Category: "discord", Emoji: "🎮", Risk: core.RiskSensitive,
		Description: "Sends a Discord message from Blavingad's bot: a DM to a user (numeric Discord user ID; works if they installed Blavingad as a user app or share a server with the bot) or a message in a channel ID. Discord markdown (**bold**, [text](url)). Max ~2000 chars per message (longer is split).",
		Params: core.Obj(map[string]any{
			"user":    core.Str("Discord user ID (numeric) for a DM."),
			"channel": core.Str("Or a channel ID (DM or server channel) instead of a user."),
			"text":    core.Str("Message text."),
		}, "text"),
		Summarize: func(args json.RawMessage) string {
			a, _ := core.Parse[struct{ User, Channel, Text string }](args)
			return fmt.Sprintf("Discord → %s: %s", firstNonEmpty(a.User, "#"+a.Channel), Clip(a.Text, 200))
		},
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct{ User, Channel, Text string }](args)
			if strings.TrimSpace(a.Text) == "" {
				return "", errors.New("empty text")
			}
			ch := cleanSnowflake(a.Channel)
			if u := cleanSnowflake(a.User); u != "" {
				if !reSnowflake.MatchString(u) {
					return "", fmt.Errorf("invalid Discord user ID %q (numeric snowflake expected)", a.User)
				}
				c, err := DiscordDMChannel(ctx, u)
				if err != nil {
					return "", err
				}
				ch = c
			}
			if !reSnowflake.MatchString(ch) {
				return "", errors.New("give a Discord user ID (user) or a channel ID (channel)")
			}
			id, err := DiscordSendChannel(ctx, ch, a.Text)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Sent in channel %s (message %s).", ch, id), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "discord_link", Category: "discord", Emoji: "🔗",
		Description: "Links a Discord user ID to a Slack user, so their Discord DMs get that person's access profile (and approval rights). Empty user = unlink. No discord_id = list links. Admins only.",
		Params: core.Obj(map[string]any{
			"discord_id": core.Str("Discord user ID (numeric). Empty = list links."),
			"user":       core.Str("Slack ID (U…) or mention; empty = remove the link."),
		}),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, _ := core.Parse[struct {
				DiscordID string `json:"discord_id"`
				User      string `json:"user"`
			}](args)
			links := map[string]string{}
			json.Unmarshal([]byte(d.St.GetSetting(DiscordLinksKey, "{}")), &links)
			id := cleanSnowflake(a.DiscordID)
			if id == "" {
				if len(links) == 0 {
					return "No Discord links yet.", nil
				}
				var l []string
				for k, v := range links {
					l = append(l, fmt.Sprintf("%s → <@%s>", k, v))
				}
				return strings.Join(l, "\n"), nil
			}
			if !rc.CanManageAccess() {
				return "", errors.New("only admins can link Discord accounts")
			}
			if !reSnowflake.MatchString(id) {
				return "", fmt.Errorf("invalid Discord user ID %q", a.DiscordID)
			}
			u := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(a.User), "<@"), ">")
			if u == "" {
				delete(links, id)
			} else {
				links[id] = u
			}
			b, _ := json.Marshal(links)
			if err := d.St.SetSetting(DiscordLinksKey, string(b)); err != nil {
				return "", err
			}
			if u == "" {
				return "Unlinked Discord " + id + ".", nil
			}
			return fmt.Sprintf("Discord %s is now linked to <@%s>.", id, u), nil
		},
	})
}
