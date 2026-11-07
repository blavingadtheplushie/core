package slackbot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/slack-go/slack"

	"blavingad/internal/core"
)

// AskRequest is a question the agent asks a human through a Slack form
// (button → modal). Secret answers never reach the model: the caller stores
// them (vault) and only reports success.
type AskRequest struct {
	Label   string // field label shown in the modal
	Hint    string // help text under the field (may contain a URL)
	Secret  bool   // secret value (stored in the vault, never shown)
	Name    string // vault item name, for display
	Timeout time.Duration
}

type pendingAsk struct {
	req       AskRequest
	requester string
	channel   string
	ts        string
	answer    chan string
	once      sync.Once
}

var (
	asksMu sync.Mutex
	asks   = map[string]*pendingAsk{}
)

// Ask posts a sober card with a button in the conversation, waits for the
// requester (or an admin) to fill the modal, and returns the value.
func (b *Bot) Ask(ctx context.Context, rc *core.RunContext, req AskRequest) (string, error) {
	if rc.UserID == "" || !strings.HasPrefix(rc.UserID, "U") {
		return "", errors.New("no Slack user to ask")
	}
	if req.Timeout <= 0 {
		req.Timeout = 15 * time.Minute
	}
	channel, thread := rc.Channel, firstNonEmpty(rc.ThreadTS, rc.MessageTS)
	if channel == "" || strings.HasPrefix(channel, "web") || strings.HasPrefix(channel, "signal:") {
		channel, thread = rc.UserID, "" // DM the person
	}
	if strings.HasPrefix(channel, "D") {
		thread = ""
	}
	id := fmt.Sprintf("ask%d", time.Now().UnixNano())
	kind := "Information requested"
	if req.Secret {
		kind = "Secret requested"
	}
	head := fmt.Sprintf("*%s* · %s", kind, escape(req.Label))
	var blocks []slack.Block
	blocks = append(blocks, section(head))
	meta := "For <@" + rc.UserID + "> · expires in " + req.Timeout.Round(time.Minute).String()
	if req.Secret {
		meta += " · stored in the vault as `" + escape(req.Name) + "`, never shown to the agent"
	}
	if req.Hint != "" {
		blocks = append(blocks, contextLine(req.Hint))
	}
	blocks = append(blocks, contextLine(meta),
		slack.NewActionBlock("ask:"+id, slack.NewButtonBlockElement("ask_open", id, plain("Fill in")).WithStyle(slack.StylePrimary)))
	opts := []slack.MsgOption{slack.MsgOptionText(kind+": "+req.Label, false), slack.MsgOptionBlocks(blocks...)}
	if thread != "" {
		opts = append(opts, slack.MsgOptionTS(thread))
	}
	ch, ts, err := b.API.PostMessageContext(ctx, channel, opts...)
	if err != nil {
		return "", fmt.Errorf("could not post the form: %w", err)
	}
	p := &pendingAsk{req: req, requester: rc.UserID, channel: ch, ts: ts, answer: make(chan string, 1)}
	asksMu.Lock()
	asks[id] = p
	asksMu.Unlock()
	defer func() {
		asksMu.Lock()
		delete(asks, id)
		asksMu.Unlock()
	}()
	finish := func(line string) {
		b.API.UpdateMessageContext(context.Background(), ch, ts, slack.MsgOptionText(line, false),
			slack.MsgOptionBlocks(contextLine(line)))
	}
	timer := time.NewTimer(req.Timeout)
	defer timer.Stop()
	select {
	case v := <-p.answer:
		finish(kind + " · " + escape(req.Label) + " · received")
		return v, nil
	case <-timer.C:
		finish(kind + " · " + escape(req.Label) + " · expired")
		return "", errors.New("nobody filled the form in time")
	case <-ctx.Done():
		finish(kind + " · " + escape(req.Label) + " · cancelled")
		return "", ctx.Err()
	}
}

func (b *Bot) askAllowed(p *pendingAsk, user string) bool {
	if user == p.requester {
		return true
	}
	prof, _ := b.access.Resolve(user)
	return prof.IsAdmin()
}

// openAsk opens the modal when someone clicks "Fill in".
func (b *Bot) openAsk(ctx context.Context, cb slack.InteractionCallback, act *slack.BlockAction) {
	asksMu.Lock()
	p := asks[act.Value]
	asksMu.Unlock()
	if p == nil {
		b.ephemeral(ctx, cb, core.Notice{Title: "Form", Task: "Expired", Status: "error", Detail: "This request is no longer open."})
		return
	}
	if !b.askAllowed(p, cb.User.ID) {
		b.ephemeral(ctx, cb, core.Notice{Title: "Form", Task: "Not allowed", Status: "error", Detail: "Only the person asked (or an admin) can answer."})
		return
	}
	in := slack.NewPlainTextInputBlockElement(plain("…"), "value")
	input := slack.NewInputBlock("v", plain(clipRunes(p.req.Label, 150)), nil, in)
	if p.req.Hint != "" {
		input.Hint = plain(clipRunes(p.req.Hint, 150))
	}
	blocks := []slack.Block{input}
	if p.req.Secret {
		blocks = append(blocks, contextLine("Goes straight into Vaultwarden as `"+escape(p.req.Name)+"`. The agent never sees it."))
	}
	view := slack.ModalViewRequest{
		Type:            slack.VTModal,
		CallbackID:      "ask_submit",
		PrivateMetadata: act.Value,
		Title:           plain("Blavingad"),
		Submit:          plain("Send"),
		Close:           plain("Cancel"),
		Blocks:          slack.Blocks{BlockSet: blocks},
	}
	if _, err := b.API.OpenViewContext(ctx, cb.TriggerID, view); err != nil {
		b.ephemeral(ctx, cb, core.Notice{Title: "Form", Task: "Could not open", Status: "error", Detail: err.Error()})
	}
}

// submitAsk receives the modal submission.
func (b *Bot) submitAsk(cb slack.InteractionCallback) {
	if cb.View.CallbackID != "ask_submit" {
		return
	}
	asksMu.Lock()
	p := asks[cb.View.PrivateMetadata]
	asksMu.Unlock()
	if p == nil || !b.askAllowed(p, cb.User.ID) {
		return
	}
	v := strings.TrimSpace(cb.View.State.Values["v"]["value"].Value)
	if v == "" {
		return
	}
	if p.req.Secret {
		core.Secrets.Add(v)
	}
	p.once.Do(func() { p.answer <- v })
}
