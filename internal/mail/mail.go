// Package mail gives BLAVINGAD its own mailbox: IMAP polling for inbound
// mail and SMTP for outbound, with everything mirrored in the emails table.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/smtp"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	_ "github.com/emersion/go-message/charset"
	gomail "github.com/emersion/go-message/mail"

	"blavingad/internal/config"
	"blavingad/internal/core"
	"blavingad/internal/store"
)

type Email struct {
	ID        int64  `json:"id"`
	Direction string `json:"direction"`
	MessageID string `json:"message_id"`
	InReplyTo string `json:"in_reply_to"`
	From      string `json:"from"`
	To        string `json:"to"`
	Cc        string `json:"cc"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	RunID     string `json:"run_id"`
	// Automated is true for auto-replies, bounces and bulk mail (never stored).
	Automated bool   `json:"-"`
	CreatedAt string `json:"created_at"`
}

type Service struct {
	cfg        *config.Config
	st         *store.Store
	bus        *core.Bus
	OnIncoming func(e Email)
	LastError  string
	LastPoll   time.Time
}

func New(cfg *config.Config, st *store.Store, bus *core.Bus) *Service {
	return &Service{cfg: cfg, st: st, bus: bus}
}

func (s *Service) Address() string { return s.cfg.EmailFrom }

// ---------- outbound ----------

// Attachment is a file attached to an outgoing email.
type Attachment struct {
	Name        string
	ContentType string
	Data        []byte
}

func writeBase64(buf *bytes.Buffer, data []byte) {
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 76 {
		buf.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	buf.WriteString(enc + "\r\n")
}

func (s *Service) Send(ctx context.Context, to, cc []string, subject, body, inReplyTo string, atts ...Attachment) (Email, error) {
	if !s.cfg.SMTPConfigured() {
		return Email{}, errors.New("SMTP is not configured")
	}
	if len(to) == 0 {
		return Email{}, errors.New("no recipients")
	}
	domain := "blavingad.local"
	if at := strings.LastIndex(s.cfg.EmailFrom, "@"); at >= 0 {
		domain = s.cfg.EmailFrom[at+1:]
	}
	rnd := make([]byte, 12)
	rand.Read(rnd)
	msgID := fmt.Sprintf("<%s@%s>", hex.EncodeToString(rnd), domain)

	var buf bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&buf, "%s: %s\r\n", k, v) }
	header("From", fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("utf-8", s.cfg.EmailName), s.cfg.EmailFrom))
	header("To", strings.Join(to, ", "))
	if len(cc) > 0 {
		header("Cc", strings.Join(cc, ", "))
	}
	header("Subject", mime.QEncoding.Encode("utf-8", subject))
	header("Date", time.Now().Format(time.RFC1123Z))
	header("Message-ID", msgID)
	if inReplyTo != "" {
		header("In-Reply-To", inReplyTo)
		header("References", inReplyTo)
	}
	header("MIME-Version", "1.0")
	header("X-Mailer", "BLAVINGAD")
	if len(atts) == 0 {
		header("Content-Type", `text/plain; charset="utf-8"`)
		header("Content-Transfer-Encoding", "base64")
		buf.WriteString("\r\n")
		writeBase64(&buf, []byte(body))
	} else {
		bnd := make([]byte, 16)
		rand.Read(bnd)
		boundary := "blv-" + hex.EncodeToString(bnd)
		header("Content-Type", fmt.Sprintf(`multipart/mixed; boundary="%s"`, boundary))
		buf.WriteString("\r\n")
		fmt.Fprintf(&buf, "--%s\r\nContent-Type: text/plain; charset=\"utf-8\"\r\nContent-Transfer-Encoding: base64\r\n\r\n", boundary)
		writeBase64(&buf, []byte(body))
		for _, a := range atts {
			ct := a.ContentType
			if ct == "" {
				ct = "application/octet-stream"
			}
			name := mime.QEncoding.Encode("utf-8", a.Name)
			fmt.Fprintf(&buf, "--%s\r\nContent-Type: %s; name=\"%s\"\r\nContent-Disposition: attachment; filename=\"%s\"\r\nContent-Transfer-Encoding: base64\r\n\r\n", boundary, ct, name, name)
			writeBase64(&buf, a.Data)
		}
		fmt.Fprintf(&buf, "--%s--\r\n", boundary)
	}

	rcpts := append(append([]string{}, to...), cc...)
	if err := s.deliver(ctx, rcpts, buf.Bytes()); err != nil {
		return Email{}, err
	}
	e := Email{Direction: "out", MessageID: msgID, InReplyTo: inReplyTo, From: s.cfg.EmailFrom, To: strings.Join(to, ", "),
		Cc: strings.Join(cc, ", "), Subject: subject, Body: body, CreatedAt: store.Now()}
	res, err := s.st.DB.Exec(`INSERT INTO emails(direction, message_id, in_reply_to, from_addr, to_addr, cc, subject, body, created_at) VALUES('out',?,?,?,?,?,?,?,?)`,
		e.MessageID, e.InReplyTo, e.From, e.To, e.Cc, e.Subject, e.Body, e.CreatedAt)
	if err == nil {
		e.ID, _ = res.LastInsertId()
	}
	s.bus.Publish("email.sent", e)
	return e, nil
}

func (s *Service) deliver(ctx context.Context, rcpts []string, msg []byte) error {
	addr := net.JoinHostPort(s.cfg.SMTPHost, fmt.Sprint(s.cfg.SMTPPort))
	tlsCfg := &tls.Config{ServerName: s.cfg.SMTPHost}
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	var conn net.Conn
	var err error
	if s.cfg.SMTPMode == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("SMTP connection: %w", err)
	}
	conn.SetDeadline(time.Now().Add(60 * time.Second))
	c, err := smtp.NewClient(conn, s.cfg.SMTPHost)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if s.cfg.SMTPMode == "starttls" {
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if s.cfg.SMTPUser != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.SMTPUser, s.cfg.SMTPPassword, s.cfg.SMTPHost)); err != nil {
			return fmt.Errorf("SMTP authentication: %w", err)
		}
	}
	if err := c.Mail(s.cfg.EmailFrom); err != nil {
		return err
	}
	for _, r := range rcpts {
		if err := c.Rcpt(strings.TrimSpace(r)); err != nil {
			return fmt.Errorf("recipient %s rejected: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// ---------- inbound ----------

func (s *Service) Run(ctx context.Context) {
	if !s.cfg.IMAPConfigured() {
		return
	}
	slog.Info("email poller started", "user", s.cfg.IMAPUser, "every", s.cfg.EmailPoll)
	t := time.NewTicker(s.cfg.EmailPoll)
	defer t.Stop()
	for {
		if n, err := s.Fetch(ctx); err != nil {
			s.LastError = err.Error()
			slog.Warn("email poll failed", "err", err)
		} else {
			s.LastError = ""
			if n > 0 {
				slog.Info("emails received", "count", n)
			}
		}
		s.LastPoll = time.Now()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Fetch pulls unseen messages from INBOX, stores them and marks them seen.
func (s *Service) Fetch(ctx context.Context) (int, error) {
	addr := net.JoinHostPort(s.cfg.IMAPHost, fmt.Sprint(s.cfg.IMAPPort))
	var c *client.Client
	var err error
	if s.cfg.IMAPTLS {
		c, err = client.DialTLS(addr, &tls.Config{ServerName: s.cfg.IMAPHost})
	} else {
		c, err = client.Dial(addr)
		if err == nil {
			if ok, _ := c.SupportStartTLS(); ok {
				err = c.StartTLS(&tls.Config{ServerName: s.cfg.IMAPHost})
			}
		}
	}
	if err != nil {
		return 0, fmt.Errorf("IMAP connection: %w", err)
	}
	c.Timeout = 60 * time.Second
	defer c.Logout()
	if err := c.Login(s.cfg.IMAPUser, s.cfg.IMAPPassword); err != nil {
		return 0, fmt.Errorf("IMAP login: %w", err)
	}
	if _, err := c.Select("INBOX", false); err != nil {
		return 0, err
	}
	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{imap.SeenFlag}
	uids, err := c.UidSearch(criteria)
	if err != nil || len(uids) == 0 {
		return 0, err
	}
	if len(uids) > 25 {
		uids = uids[len(uids)-25:]
	}
	seq := new(imap.SeqSet)
	seq.AddNum(uids...)
	section := &imap.BodySectionName{}
	msgs := make(chan *imap.Message, 25)
	done := make(chan error, 1)
	go func() {
		done <- c.UidFetch(seq, []imap.FetchItem{imap.FetchEnvelope, imap.FetchUid, section.FetchItem()}, msgs)
	}()
	var received []Email
	for m := range msgs {
		e, err := parse(m, section)
		if err != nil {
			slog.Warn("email parse", "err", err)
			continue
		}
		if e.MessageID != "" && s.st.Count(`SELECT COUNT(*) FROM emails WHERE message_id = ?`, e.MessageID) > 0 {
			continue
		}
		res, err := s.st.DB.Exec(`INSERT INTO emails(direction, uid, message_id, in_reply_to, from_addr, to_addr, cc, subject, body, created_at) VALUES('in',?,?,?,?,?,?,?,?,?)`,
			m.Uid, e.MessageID, e.InReplyTo, e.From, e.To, e.Cc, e.Subject, e.Body, store.Now())
		if err != nil {
			continue
		}
		e.ID, _ = res.LastInsertId()
		e.Direction = "in"
		e.CreatedAt = store.Now()
		received = append(received, e)
	}
	if err := <-done; err != nil {
		return len(received), err
	}
	flags := []any{imap.SeenFlag}
	c.UidStore(seq, imap.FormatFlagsOp(imap.AddFlags, true), flags, nil)
	for _, e := range received {
		s.bus.Publish("email.received", e)
		if s.OnIncoming != nil {
			go s.OnIncoming(e)
		}
	}
	return len(received), nil
}

func parse(m *imap.Message, section *imap.BodySectionName) (Email, error) {
	var e Email
	if m.Envelope != nil {
		e.Subject = m.Envelope.Subject
		e.MessageID = m.Envelope.MessageId
		e.InReplyTo = m.Envelope.InReplyTo
		e.From = addrs(m.Envelope.From)
		e.To = addrs(m.Envelope.To)
		e.Cc = addrs(m.Envelope.Cc)
	}
	r := m.GetBody(section)
	if r == nil {
		return e, errors.New("empty body")
	}
	mr, err := gomail.CreateReader(r)
	if err != nil {
		return e, err
	}
	e.Automated = isAutomated(mr.Header.Get("Auto-Submitted"), mr.Header.Get("Precedence"), mr.Header.Get("List-Id"), mr.Header.Get("X-Autoreply"), e.From)
	var plain, html string
	var attachments []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch h := p.Header.(type) {
		case *gomail.InlineHeader:
			ct, _, _ := h.ContentType()
			b, _ := io.ReadAll(io.LimitReader(p.Body, 512<<10))
			if ct == "text/plain" && plain == "" {
				plain = string(b)
			} else if ct == "text/html" && html == "" {
				html = string(b)
			}
		case *gomail.AttachmentHeader:
			name, _ := h.Filename()
			attachments = append(attachments, name)
		}
	}
	e.Body = strings.TrimSpace(plain)
	if e.Body == "" && html != "" {
		e.Body = HTMLToText(html)
	}
	if len(attachments) > 0 {
		e.Body += "\n\n[Attachments: " + strings.Join(attachments, ", ") + "]"
	}
	if len(e.Body) > 60000 {
		e.Body = e.Body[:60000] + "\n…[truncated]"
	}
	return e, nil
}

func addrs(list []*imap.Address) string {
	var out []string
	for _, a := range list {
		addr := a.Address()
		if a.PersonalName != "" {
			addr = fmt.Sprintf("%s <%s>", a.PersonalName, addr)
		}
		out = append(out, addr)
	}
	return strings.Join(out, ", ")
}

var (
	reScript = regexp.MustCompile(`(?is)<(script|style|head)[^>]*>.*?</(script|style|head)>`)
	reBreak  = regexp.MustCompile(`(?i)<(br|/p|/div|/h[1-6]|/li|/tr)[^>]*>`)
	reTag    = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpace  = regexp.MustCompile(`[ \t]+`)
	reLines  = regexp.MustCompile(`\n{3,}`)
)

func HTMLToText(h string) string {
	h = reScript.ReplaceAllString(h, "")
	h = reBreak.ReplaceAllString(h, "\n")
	h = reTag.ReplaceAllString(h, "")
	r := strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&eacute;", "é", "&egrave;", "è", "&agrave;", "à")
	h = r.Replace(h)
	h = reSpace.ReplaceAllString(h, " ")
	h = reLines.ReplaceAllString(h, "\n\n")
	return strings.TrimSpace(h)
}

func (s *Service) Get(id int64) (Email, error) {
	var e Email
	err := s.st.DB.QueryRow(`SELECT id, direction, COALESCE(message_id,''), COALESCE(in_reply_to,''), COALESCE(from_addr,''), COALESCE(to_addr,''), COALESCE(cc,''), COALESCE(subject,''), COALESCE(body,''), COALESCE(run_id,''), created_at FROM emails WHERE id = ?`, id).
		Scan(&e.ID, &e.Direction, &e.MessageID, &e.InReplyTo, &e.From, &e.To, &e.Cc, &e.Subject, &e.Body, &e.RunID, &e.CreatedAt)
	return e, err
}

// isAutomated spots mail that must never get an automatic answer
// (RFC 3834 auto-replies, bounces, mailing lists, no-reply senders).
func isAutomated(autoSubmitted, precedence, listID, xAutoreply, from string) bool {
	if a := strings.ToLower(strings.TrimSpace(autoSubmitted)); a != "" && a != "no" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(precedence)) {
	case "bulk", "list", "junk", "auto_reply":
		return true
	}
	if strings.TrimSpace(listID) != "" || strings.TrimSpace(xAutoreply) != "" {
		return true
	}
	addr := strings.ToLower(ExtractAddress(from))
	for _, p := range []string{"mailer-daemon@", "postmaster@", "noreply", "no-reply", "do-not-reply", "donotreply", "bounce"} {
		if strings.Contains(addr, p) {
			return true
		}
	}
	return false
}

// AutoRepliesTo counts the emails sent to addr in the last 24 hours.
func (s *Service) AutoRepliesTo(addr string) int {
	return s.st.Count(`SELECT COUNT(*) FROM emails WHERE direction = 'out' AND lower(to_addr) LIKE ? AND created_at >= ?`,
		"%"+strings.ToLower(addr)+"%", time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339))
}

// ExtractAddress pulls the bare address out of "Name <addr>".
func ExtractAddress(s string) string {
	if i := strings.LastIndex(s, "<"); i >= 0 {
		if j := strings.Index(s[i:], ">"); j > 0 {
			return s[i+1 : i+j]
		}
	}
	return strings.TrimSpace(s)
}
