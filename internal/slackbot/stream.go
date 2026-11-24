package slackbot

import (
	"blavingad/internal/textutil"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/slack-go/slack"
)

// Native Slack streaming (chat.startStream / appendStream / stopStream).
//
// While the agent works, tool calls and thoughts are streamed as task_update
// chunks (rendered by Slack as a live plan) and the answer text is streamed
// as markdown_text once it is clearly an answer (see looksLikeAnswer). The
// stream is closed append-only (finishNative). If Slack refuses or drops
// the stream, nothing more is sent until the end: finish() then replaces
// the message (or reposts it) with the final layout.

const nativeFlushEvery = 700 * time.Millisecond // appendStream is Tier 4 (100+/min)

// startNative opens the stream. It returns false when Slack refuses it (the
// answer is then posted once at the end).
func (l *liveMessage) startNative(ctx context.Context) bool {
	if l.thread == "" || l.user == "" || l.b.API == nil {
		return false
	}
	opts := []slack.MsgOption{
		slack.MsgOptionTS(l.thread),
		slack.MsgOptionRecipientUserID(l.user),
		slack.MsgOptionTaskDisplayMode(slack.TaskDisplayModePlan),
		slack.MsgOptionChunks(slack.NewPlanUpdateChunk("Working on it…")),
	}
	if l.b.TeamID != "" {
		opts = append(opts, slack.MsgOptionRecipientTeamID(l.b.TeamID))
	}
	_, ts, err := l.b.API.StartStreamContext(ctx, l.channel, opts...)
	if err != nil || ts == "" {
		slog.Warn("native Slack streaming unavailable, the answer will be posted at the end", "err", err)
		return false
	}
	l.ts, l.native, l.sent = ts, true, map[int]string{}
	go func() {
		t := time.NewTicker(nativeFlushEvery)
		defer t.Stop()
		for {
			select {
			case <-l.stop:
				return
			case <-t.C:
				l.flushNative(context.Background())
			}
		}
	}()
	return true
}

// nativeChunks collects what changed since the last flush: task cards whose
// state moved, and the new text of the current turn.
func (l *liveMessage) nativeChunks() []slack.StreamChunk {
	l.mu.Lock()
	defer l.mu.Unlock()
	var chunks []slack.StreamChunk
	for i, e := range l.events {
		c := slack.NewTaskUpdateChunk(fmt.Sprintf("t%d", i+1), e.taskTitle())
		c.Status = slack.TaskCardStatus(e.status)
		if d := strings.TrimSpace(e.details); d != "" {
			c.Details = textutil.Clip(d, 600)
		}
		out := strings.TrimSpace(e.output)
		if e.kind == "tool" && privateOutput[e.category] && !e.denied && out != "" {
			out = "(result hidden in Slack — see the trace)"
		}
		if out != "" {
			c.Output = textutil.Clip(out, 600)
		}
		sig, _ := json.Marshal(c)
		if l.sent[i] == string(sig) {
			continue
		}
		l.sent[i] = string(sig)
		chunks = append(chunks, c)
	}
	if l.iteration != l.textIter {
		if l.streamed != "" {
			l.earlierText = true // that turn's text stays in the message
		}
		l.textIter, l.streamed, l.textBroken = l.iteration, "", false
	}
	// Text the model writes before a tool call (narration) belongs in the
	// plan, but appendStream cannot take text back. So a turn's text is only
	// streamed once it looks like a real answer; narration stays hidden and
	// shows up as a "Thinking" card when the turn ends with tool calls.
	// Slack keeps streamed markdown_text even after chat.update, so once
	// text is streamed the message is never rewritten: finishNative only
	// appends what is missing (or deletes and reposts if the text diverged).
	if l.streamed == "" && !looksLikeAnswer(l.draft) {
		return chunks
	}
	if conv := streamMarkdown(l.draft); !l.textBroken && conv != "" {
		if !strings.HasPrefix(conv, l.streamed) {
			l.textBroken = true // redaction rewrote earlier text: wait for the final answer
		} else if delta := conv[len(l.streamed):]; delta != "" {
			if l.streamed == "" && l.anyText {
				delta = "\n\n" + delta
			}
			l.streamed, l.anyText = conv, true
			chunks = append(chunks, slack.NewMarkdownTextChunk(delta))
		}
	}
	return chunks
}

// streamAnswerAfter is how much text a turn needs before it is streamed as
// the answer: narration next to tool calls is almost always one short line.
const streamAnswerAfter = 240

func looksLikeAnswer(draft string) bool {
	d := strings.TrimSpace(draft)
	return utf8.RuneCountInString(d) >= streamAnswerAfter || strings.Contains(d, "\n\n")
}

// flushNative appends what changed since the last flush.
func (l *liveMessage) flushNative(ctx context.Context) {
	l.nmu.Lock()
	defer l.nmu.Unlock()
	if !l.native || l.closed {
		return
	}
	chunks := l.nativeChunks()
	if len(chunks) == 0 {
		return
	}
	_, _, err := l.b.API.AppendStreamContext(ctx, l.channel, l.ts, slack.MsgOptionChunks(chunks...))
	if err == nil {
		return
	}
	slog.Warn("native Slack stream update failed", "err", err)
	if strings.Contains(err.Error(), "message_not_in_streaming_state") {
		// Slack closed the stream (10 min cap, or a long retry pause):
		// stay quiet, finish() posts the final layout.
		l.closed = true
		return
	}
	// Stop appending text; finish() still closes the stream and posts the answer.
	l.mu.Lock()
	l.textBroken = true
	l.mu.Unlock()
}

// streamMarkdown turns the model's Slack-mrkdwn draft into the standard
// markdown chat.appendStream expects, in an append-only way: every lone "*"
// (mrkdwn bold) becomes "**", outside code. The draft always ends before a
// whitespace (see agent.draftStreamer), so star runs are never split.
func streamMarkdown(s string) string {
	var b strings.Builder
	inline, fence := false, false
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		c := r[i]
		if c == '`' {
			n := 1
			for i+n < len(r) && r[i+n] == '`' {
				n++
			}
			if n >= 3 {
				fence = !fence
			} else if !fence {
				inline = !inline
			}
			b.WriteString(strings.Repeat("`", n))
			i += n - 1
			continue
		}
		if c == '*' && !inline && !fence {
			n := 1
			for i+n < len(r) && r[i+n] == '*' {
				n++
			}
			if n == 1 {
				b.WriteString("**")
			} else {
				b.WriteString(strings.Repeat("*", n))
			}
			i += n - 1
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// Outcomes of finishNative.
const (
	nativeUpdate   = iota // no text was streamed: replace the message with the final layout
	nativeComplete        // the streamed text is the whole answer: leave the message alone
	nativeRepost          // streamed text diverged from the answer: delete and repost
)

// maxStopText caps the text sent with chat.stopStream; longer remainders
// take the repost path (the final layout splits long answers).
const maxStopText = 8000

// finishNative closes the stream append-only: the missing end of the
// answer goes into chat.stopStream, and the message is never rewritten
// with the full text (that is what duplicated answers before).
func (l *liveMessage) finishNative(ctx context.Context, output string, ok bool) int {
	l.nmu.Lock()
	defer l.nmu.Unlock()
	if !l.native {
		return nativeUpdate
	}
	if l.closed {
		// Slack dropped the stream: streamed text can't be edited away.
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.streamed != "" || l.earlierText {
			return nativeRepost
		}
		return nativeUpdate
	}
	chunks := l.nativeChunks()
	l.mu.Lock()
	mode := nativeUpdate
	if l.streamed != "" || l.earlierText {
		mode = nativeRepost
		final := streamMarkdown(output)
		if ok && !l.earlierText && !l.textBroken && strings.HasPrefix(final, l.streamed) {
			if delta := final[len(l.streamed):]; utf8.RuneCountInString(delta) <= maxStopText {
				if strings.TrimSpace(delta) != "" {
					chunks = append(chunks, slack.NewMarkdownTextChunk(delta))
				}
				l.streamed, mode = final, nativeComplete
			}
		}
	}
	l.mu.Unlock()
	l.closed = true
	opts := []slack.MsgOption{}
	if len(chunks) > 0 {
		opts = append(opts, slack.MsgOptionChunks(chunks...))
	}
	if _, _, err := l.b.API.StopStreamContext(ctx, l.channel, l.ts, opts...); err != nil {
		slog.Warn("native Slack stream stop failed", "err", err, "mode", mode)
		if mode == nativeComplete {
			mode = nativeRepost // the end of the answer is missing
		}
		// The stream must be closed anyway: a message left in streaming
		// state can be neither deleted nor updated (streaming_state_conflict),
		// so the half-streamed text stayed next to the reposted answer.
		// Retry with no chunks at all (msg_too_long, bad chunk…).
		if len(opts) > 0 {
			if _, _, err := l.b.API.StopStreamContext(ctx, l.channel, l.ts); err != nil {
				slog.Warn("native Slack stream stop retry failed", "err", err)
			}
		}
	}
	return mode
}
