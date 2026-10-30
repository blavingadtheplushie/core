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
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/slack-go/slack"

	"blavingad/internal/core"
)

// The ElevenLabs API key lives in the vault (password field of this item).
const elevenRef = "vault://ELEVENLABS_API_KEY/password"

var elevenBase = "https://api.elevenlabs.io"

var reVoiceID = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)

// elevenKey resolves the API key, syncing the vault once if it is still empty
// (the item is created empty and filled in by a human later).
func (d *Deps) elevenKey(ctx context.Context, rc *core.RunContext) (string, error) {
	if err := d.vaultAllowed(rc); err != nil {
		return "", err
	}
	k, err := d.resolveRef(ctx, elevenRef)
	if err != nil || strings.TrimSpace(k) == "" {
		if rerr := d.Vault.Refresh(ctx); rerr == nil {
			k, err = d.resolveRef(ctx, elevenRef)
		}
	}
	k = strings.TrimSpace(k)
	if err != nil || k == "" {
		return "", fmt.Errorf("no ElevenLabs API key: put it in the Password field of the vault item ELEVENLABS_API_KEY (%v)", err)
	}
	return k, nil
}

func (d *Deps) elevenDo(ctx context.Context, key, method, path string, body any) ([]byte, string, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, elevenBase+path, rd)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("xi-api-key", key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("elevenlabs %s: %s", resp.Status, trunc(string(out), 500))
	}
	return out, resp.Header.Get("Content-Type"), nil
}

type elevenVoice struct {
	VoiceID  string            `json:"voice_id"`
	Name     string            `json:"name"`
	Category string            `json:"category"`
	Labels   map[string]string `json:"labels"`
}

func (d *Deps) elevenVoices(ctx context.Context, key string) ([]elevenVoice, error) {
	b, _, err := d.elevenDo(ctx, key, "GET", "/v1/voices", nil)
	if err != nil {
		return nil, err
	}
	var r struct {
		Voices []elevenVoice `json:"voices"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("unreadable voice list: %w", err)
	}
	return r.Voices, nil
}

// pickVoice maps a name or ID to a voice ID ("" = first available voice).
func pickVoice(voices []elevenVoice, want string) (elevenVoice, error) {
	want = strings.TrimSpace(want)
	for _, v := range voices {
		if v.VoiceID == want {
			return v, nil
		}
	}
	if want == "" && len(voices) > 0 {
		return voices[0], nil
	}
	lw := strings.ToLower(want)
	for _, v := range voices {
		if strings.EqualFold(v.Name, want) {
			return v, nil
		}
	}
	for _, v := range voices {
		if strings.HasPrefix(strings.ToLower(v.Name), lw) {
			return v, nil
		}
	}
	if reVoiceID.MatchString(want) {
		return elevenVoice{VoiceID: want, Name: want}, nil
	}
	return elevenVoice{}, fmt.Errorf("unknown voice %q: call tts_voices", want)
}

func registerElevenLabs(d *Deps) {
	ok := func() bool { return d.vaultReady() }

	d.Reg.Add(&core.Tool{
		Name: "tts_voices", Category: "audio", Emoji: "🎙️", Available: ok,
		Description: "Lists the ElevenLabs voices available for tts_speak (name, ID, accent/gender labels).",
		Params:      core.Obj(map[string]any{"query": core.Str("Optional filter on name/labels.")}),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			key, err := d.elevenKey(ctx, rc)
			if err != nil {
				return "", err
			}
			voices, err := d.elevenVoices(ctx, key)
			if err != nil {
				return "", err
			}
			q := strings.ToLower(field(args, "query"))
			var lines []string
			for _, v := range voices {
				var labels []string
				for k, val := range v.Labels {
					labels = append(labels, k+"="+val)
				}
				line := fmt.Sprintf("%s — %s (%s) %s", v.Name, v.VoiceID, v.Category, strings.Join(labels, ", "))
				if q == "" || strings.Contains(strings.ToLower(line), q) {
					lines = append(lines, line)
				}
			}
			if len(lines) == 0 {
				return "No voice matches.", nil
			}
			return Clip(strings.Join(lines, "\n"), 0), nil
		},
	})

	d.Reg.Add(&core.Tool{
		Name: "tts_speak", Category: "audio", Emoji: "🗣️", Available: ok,
		Description: "Reads a text aloud with ElevenLabs text-to-speech, saves the MP3 in the workspace and (by default) uploads it to the current Slack thread. Use it when someone wants you to say, read, sing or declaim something out loud. Max 5000 characters; costs ElevenLabs credits, so keep it reasonable.",
		Params: core.Obj(map[string]any{
			"text":    core.Str("Text to speak (max 5000 chars). Write it as it should be heard."),
			"voice":   core.Str("Voice name or ID (see tts_voices). Default: first voice of the account."),
			"model":   core.Str("Model ID (default eleven_multilingual_v2, handles French and English)."),
			"title":   core.Str("Title of the uploaded file (optional)."),
			"comment": core.Str("Message posted with the audio (optional)."),
			"post":    map[string]any{"type": "boolean", "description": "Upload to the current Slack thread (default true)."},
		}, "text"),
		Summarize: summarizeField("Speaking: ", "text", 60),
		Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
			a, err := core.Parse[struct {
				Text, Voice, Model, Title, Comment string
				Post                               *bool
			}](args)
			if err != nil {
				return "", err
			}
			a.Text = strings.TrimSpace(a.Text)
			if a.Text == "" {
				return "", errors.New("text is empty")
			}
			if n := len([]rune(a.Text)); n > 5000 {
				return "", fmt.Errorf("text too long (%d chars, max 5000)", n)
			}
			if a.Model == "" {
				a.Model = "eleven_multilingual_v2"
			}
			key, err := d.elevenKey(ctx, rc)
			if err != nil {
				return "", err
			}
			var voice elevenVoice
			if reVoiceID.MatchString(a.Voice) {
				voice = elevenVoice{VoiceID: a.Voice, Name: a.Voice}
			} else {
				voices, err := d.elevenVoices(ctx, key)
				if err != nil {
					return "", err
				}
				if voice, err = pickVoice(voices, a.Voice); err != nil {
					return "", err
				}
			}
			audio, _, err := d.elevenDo(ctx, key, "POST",
				"/v1/text-to-speech/"+url.PathEscape(voice.VoiceID)+"?output_format=mp3_44100_128",
				map[string]any{"text": a.Text, "model_id": a.Model})
			if err != nil {
				return "", err
			}
			dir := filepath.Join(d.Cfg.WorkspaceDir, "audio")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", err
			}
			name := fmt.Sprintf("tts-%s.mp3", time.Now().Format("20060102-150405"))
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, audio, 0o644); err != nil {
				return "", err
			}
			d.St.Audit(rc.Actor(), "tts.speak", voice.Name, map[string]any{"chars": len([]rune(a.Text)), "model": a.Model, "file": p})
			res := fmt.Sprintf("Audio generated with voice %s (%d KB): %s", voice.Name, len(audio)/1024, p)
			if a.Post != nil && !*a.Post {
				return res, nil
			}
			if d.Slack == nil || rc.Channel == "" {
				return res + " (not posted: no Slack conversation)", nil
			}
			if a.Title == "" {
				a.Title = name
			}
			_, err = d.Slack.UploadFileContext(ctx, slack.UploadFileParameters{File: p, FileSize: len(audio), Filename: name, Title: a.Title,
				Channel: rc.Channel, ThreadTimestamp: firstNonEmpty(rc.ThreadTS, rc.MessageTS), InitialComment: a.Comment})
			if err != nil {
				return res + " — Slack upload failed: " + err.Error(), nil
			}
			return res + " — uploaded to Slack.", nil
		},
	})
}
