package slackbot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/slack-go/slack"
)

// Native Slack streaming (chat.startStream / appendStream / stopStream),
// used when the stream_replies setting is on and the reply goes in a thread.
//
// While the agent works, tool calls and thoughts are streamed as task_update
// chunks (rendered by Slack as a live plan) and the answer text is streamed
// as markdown_text once it is clearly an answer (see looksLikeAnswer). When the run ends the stream is stopped and the
// message is replaced (chat.update) by the usual final layout, so the stored
// answer is identical to the non-streamed one (clean mrkdwn, full plan,
// reflection updates). Any API error falls back to the classic live message.

const nativeFlushEvery = 700 * time.Millisecond // appendStream is Tier 4 (100+/min)

// startNative opens the stream. It returns false when the classic live
// message must be used instead.
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
		slog.Warn("native Slack streaming unavailable, using the classic live message", "err", err)
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
				l.flushNative(context.Background(), false)
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
			c.Details = clipRunes(d, 600)
		}
		out := strings.TrimSpace(e.output)
		if e.kind == "tool" && privateOutput[e.category] && !e.denied && out != "" {
			out = "(result hidden in Slack — see the trace)"
		}
		if out != "" {
			c.Output = clipRunes(out, 600)
		}
		sig, _ := json.Marshal(c)
		if l.sent[i] == string(sig) {
			continue
		}
		l.sent[i] = string(sig)
		chunks = append(chunks, c)
	}
	if l.iteration != l.textIter {
		l.textIter, l.streamed, l.textBroken = l.iteration, "", false
	}
	// Text the model writes before a tool call (narration) belongs in the
	// plan, but appendStream cannot take text back. So a turn's text is only
	// streamed once it looks like a real answer; narration stays hidden and
	// shows up as a "Thinking" card when the turn ends with tool calls.
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

// flushNative sends pending chunks. last=true closes the stream.
func (l *liveMessage) flushNative(ctx context.Context, last bool, extra ...slack.StreamChunk) {
	l.nmu.Lock()
	defer l.nmu.Unlock()
	if !l.native || l.closed {
		return
	}
	chunks := append(l.nativeChunks(), extra...)
	var err error
	switch {
	case last:
		l.closed = true
		opts := []slack.MsgOption{}
		if len(chunks) > 0 {
			opts = append(opts, slack.MsgOptionChunks(chunks...))
		}
		_, _, err = l.b.API.StopStreamContext(ctx, l.channel, l.ts, opts...)
	case len(chunks) > 0:
		_, _, err = l.b.API.AppendStreamContext(ctx, l.channel, l.ts, slack.MsgOptionChunks(chunks...))
	}
	if err != nil {
		slog.Warn("native Slack stream update failed", "err", err, "last", last)
		if !last {
			// Stop appending; finish() still closes the stream and posts the answer.
			l.mu.Lock()
			l.textBroken = true
			l.mu.Unlock()
		}
	}
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
