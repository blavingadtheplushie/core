// BLAVINGAD — an autonomous, self-improving Slack agent.
//
//	blavingad supervise   container entrypoint: runs `serve` and handles self-upgrades/rollbacks
//	blavingad serve       the agent itself (Slack, web UI, email, scheduler)
//	blavingad update      rebuild from the source in the container and restart (no image rebuild)
//	blavingad version     print the version
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"blavingad/internal/agent"
	"blavingad/internal/config"
	"blavingad/internal/core"
	"blavingad/internal/llm"
	"blavingad/internal/mail"
	"blavingad/internal/memory"
	"blavingad/internal/scheduler"
	"blavingad/internal/selfupdate"
	"blavingad/internal/slackbot"
	"blavingad/internal/store"
	"blavingad/internal/tools"
	"blavingad/internal/vault"
	"blavingad/internal/web"
)

var version = "dev"

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	switch cmd {
	case "version", "--version", "-v":
		fmt.Println("blavingad", version)
	case "supervise":
		os.Exit(selfupdate.Supervise(config.Load().DataDir))
	case "serve":
		os.Exit(serve())
	case "update":
		os.Exit(update(os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "usage: blavingad [serve|supervise|update [--pull] [--force]|version]\n")
		os.Exit(2)
	}
}

func serve() int {
	// Secrets first: the vault fills the environment before the config reads it.
	vc := vault.New(vault.ConfigFromEnv())
	rawEnv := vault.Snapshot(config.VaultKeys)
	vaultValues, vaultSources := map[string]string{}, map[string]string{}
	if vc.Enabled() {
		vctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		if err := vc.Open(vctx); err != nil {
			slog.Error("vault unavailable — settings stored in it are missing until it is reachable", "err", err)
		} else {
			vals, srcs, errs := vc.ResolveEnv(vctx, rawEnv)
			for k, v := range vals {
				os.Setenv(k, v)
			}
			for _, e := range errs {
				slog.Warn("vault reference not resolved", "detail", e)
			}
			vaultValues, vaultSources = vals, srcs
			slog.Info("settings loaded from vault", "keys", sortedKeys(srcs))
		}
		cancel()
	}
	// An unresolved reference must never be used as a literal value.
	for k, v := range rawEnv {
		if strings.HasPrefix(v, "vault://") && vaultValues[k] == "" {
			os.Unsetenv(k)
		}
	}
	cfg := config.Load()
	core.Secrets.Add(cfg.SecretValues()...)
	core.Secrets.Add(vc.Secrets()...)
	config.ScrubEnv()

	for _, d := range []string{cfg.DataDir, cfg.WorkspaceDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			slog.Error("cannot create dir", "dir", d, "err", err)
			return 1
		}
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		slog.Error("database", "err", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	bus := core.NewBus()
	client := llm.New(cfg.LLMBaseURL, cfg.LLMAPIKey)
	client.Headers = cfg.LLMHeaders
	// API style and base URL can be switched from the dashboard at runtime.
	client.Endpoint = func() (string, string) {
		s := agent.LoadSettings(st, cfg)
		return s.APIStyle, s.BaseURL
	}
	embedClient := llm.New(cfg.EmbedBaseURL, cfg.EmbedAPIKey)
	embedClient.Headers = cfg.LLMHeaders
	mem := memory.New(st, embedClient, cfg.EmbedModel)
	reg := core.NewRegistry(st)
	approvals := core.NewApprovals(st, bus, reg)
	access := core.NewAccess(st, cfg.SlackAdmins)

	self := selfupdate.NewManager(cfg, st, bus, version)
	self.ConfigureGit()
	if err := self.EnsureSource(envOr("SEED_DIR", "/opt/blavingad/src")); err != nil {
		slog.Warn("self source unavailable — self-modification disabled until fixed", "err", err)
	}
	// Must run before tools are registered: it may move SOURCE_DIR.
	sourceNotice := self.EnsureWritable(context.Background())

	var ml *mail.Service
	if cfg.IMAPConfigured() || cfg.SMTPConfigured() {
		ml = mail.New(cfg, st, bus)
	}
	runner := agent.NewRunner(cfg, st, mem, client, reg, bus, approvals, access)

	var bot *slackbot.Bot
	if cfg.SlackConfigured() {
		bot = slackbot.New(cfg, st, mem, runner, approvals, access, bus)
		approvals.SetNotifier(bot)
	} else {
		slog.Warn("Slack not configured (SLACK_BOT_TOKEN / SLACK_APP_TOKEN) — web UI only")
	}

	post := func(channel, thread, text string) {
		if bot != nil {
			bot.Announce(channel, thread, text)
		}
	}

	runSchedule := func(sc scheduler.Schedule) {
		var prof *core.Profile
		if sc.Profile != "" {
			p, ok := access.Profile(sc.Profile)
			if !ok {
				p, _ = access.Resolve("") // profile deleted since: fall back to the default
			}
			prof = &p
		}
		res := runner.Run(ctx, agent.Request{
			Source: "schedule", Channel: sc.Channel, ThreadTS: sc.ThreadTS, UserName: "⏰ " + sc.Name, Profile: prof,
			Text: fmt.Sprintf("[Scheduled task %q, created by %s]\n%s", sc.Name, sc.CreatedBy, sc.Prompt),
		})
		st.DB.Exec(`UPDATE schedules SET last_status = ? WHERE id = ?`, res.Status, sc.ID)
		if sc.Channel != "" && sc.Channel != "web" {
			post(sc.Channel, sc.ThreadTS, res.Output)
		} else {
			post("", "", "⏰ *"+sc.Name+"*\n"+res.Output)
		}
	}
	sched := scheduler.New(st, cfg.Timezone, runSchedule)

	selfReview := func() {
		res := runner.SelfReview(ctx)
		post("", "", "🧬 *Self-improvement review*\n"+res.Output)
	}

	deps := &tools.Deps{Cfg: cfg, St: st, Mem: mem, LLM: client, Reg: reg, Bus: bus, Mail: ml, Self: self, Sched: sched, Access: access, Vault: vc}
	if bot != nil {
		deps.Slack = bot.API
		deps.Reveal = bot.RevealSecret
	}

	// vaultSync pulls the vault; if a setting it provides changed, restart
	// so the new value is used everywhere.
	vaultSync := func(ctx context.Context) ([]string, error) {
		if !vc.Enabled() {
			return nil, errors.New("no vault configured")
		}
		if err := vc.Refresh(ctx); err != nil {
			return nil, err
		}
		core.Secrets.Add(vc.Secrets()...)
		vals, srcs, _ := vc.ResolveEnv(ctx, rawEnv)
		var changed []string
		for _, key := range config.VaultKeys {
			if vals[key] != vaultValues[key] {
				changed = append(changed, key)
			}
		}
		if len(changed) > 0 {
			slog.Info("vault settings changed, restarting", "keys", changed)
			post("", "", "🔐 Secrets changed in the vault ("+strings.Join(changed, ", ")+"): restarting to apply them. ♻️")
			vaultValues, vaultSources = vals, srcs
			self.RequestRestart("vault: " + strings.Join(changed, ","))
		}
		return changed, nil
	}
	tools.RegisterAll(deps)
	slog.Info("tools registered", "count", len(reg.All()))

	if ml != nil {
		ml.OnIncoming = func(e mail.Email) {
			s := runner.Settings()
			note := fmt.Sprintf("📬 New email from *%s* — %q (email #%d)", e.From, e.Subject, e.ID)
			if !s.EmailAuto {
				post("", "", note)
				return
			}
			res := runner.Run(ctx, agent.Request{
				Source: "email", UserName: e.From,
				Text: fmt.Sprintf("You received an email (#%d) in your mailbox %s.\nFrom: %s\nTo: %s\nSubject: %s\n\n%s\n\n"+
					"Handle it: reply with email_reply if it is relevant and safe, ignore spam, and summarize what you did in 2-3 lines.",
					e.ID, cfg.EmailFrom, e.From, e.To, e.Subject, e.Body),
			})
			st.DB.Exec(`UPDATE emails SET run_id = ? WHERE id = ?`, res.RunID, e.ID)
			post("", "", note+"\n"+res.Output)
		}
		go ml.Run(ctx)
	}

	srv := web.New(cfg, st, mem, runner, reg, approvals, access, bus, self, ml, web.Hooks{
		SlackStatus: func() map[string]any {
			if bot == nil {
				return map[string]any{"connected": false}
			}
			return map[string]any{"connected": bot.Connected, "bot_user": bot.BotUserID, "team_url": bot.TeamURL}
		},
		VaultStatus: func() map[string]any {
			return map[string]any{"status": vc.Status(), "sources": vaultSources, "masked": core.Secrets.Count()}
		},
		VaultSync:     vaultSync,
		LLM:           client,
		RunSchedule:   runSchedule,
		RunSelfReview: selfReview,
		ReloadSched:   sched.Reload,
	})
	webErr := make(chan error, 1)
	go func() { webErr <- srv.ListenAndServe(ctx) }()

	if bot != nil {
		if err := bot.Start(ctx); err != nil {
			slog.Error("slack start failed", "err", err)
		}
	}
	go sched.Run(ctx)
	go mem.Backfill(ctx)
	go selfReviewLoop(ctx, runner, cfg, selfReview)
	go self.WatchRestartRequests(ctx)
	if vc.Enabled() {
		go func() {
			t := time.NewTicker(15 * time.Minute)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if _, err := vaultSync(ctx); err != nil {
						slog.Warn("vault sync failed", "err", err)
					}
				}
			}
		}()
	}
	self.AfterBoot(post)
	if sourceNotice != "" {
		go func() {
			time.Sleep(5 * time.Second) // let Slack connect
			post("", "", sourceNotice)
		}()
	}
	slog.Info("BLAVINGAD is up 🤖", "version", version)

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
		runner.Wait(20 * time.Second)
		return 0
	case err := <-webErr:
		if err != nil {
			slog.Error("web server", "err", err)
			return 1
		}
		return 0
	case reason := <-self.RestartRequests():
		slog.Info("restart requested", "reason", reason)
		// Let the run that asked for it (and any other) finish and reply.
		time.Sleep(2 * time.Second)
		runner.Wait(3 * time.Minute)
		time.Sleep(4 * time.Second)
		return selfupdate.ExitRestart
	}
}

// update is meant for `docker exec blavingad blavingad update [--pull]`: it
// rebuilds from SOURCE_DIR (after a git pull from SELF_REPO with --pull) and
// asks the running agent to restart on the new binary.
func update(args []string) int {
	opt := selfupdate.UpdateOptions{Actor: "cli"}
	for _, a := range args {
		switch a {
		case "--pull", "-p":
			opt.Pull = true
		case "--force", "-f":
			opt.Force = true
		case "-h", "--help":
			fmt.Println("usage: blavingad update [--pull] [--force]\n  --pull   git pull from SELF_REPO first\n  --force  rebuild even if nothing changed")
			return 0
		default:
			fmt.Fprintf(os.Stderr, "unknown flag %s\n", a)
			return 2
		}
	}
	cfg := config.Load()
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "database:", err)
		return 1
	}
	self := selfupdate.NewManager(cfg, st, core.NewBus(), version)
	opt.Log = func(line string) { fmt.Println(line) }
	opt.Restart = func(reason string) {
		if err := self.RequestRestartFromOutside(reason); err != nil {
			fmt.Fprintln(os.Stderr, "could not ask the agent to restart:", err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	res, err := self.Update(ctx, opt)
	switch {
	case errors.Is(err, selfupdate.ErrUpToDate):
		fmt.Println("✅ Already up to date (use --force to rebuild anyway).")
		return 0
	case err != nil:
		fmt.Fprintln(os.Stderr, "❌", err)
		if res.BuildLog != "" {
			fmt.Fprintln(os.Stderr, "\n--- build log ---\n"+res.BuildLog)
		}
		return 1
	}
	fmt.Printf("🚀 Staged %s%s. The agent restarts once its running conversations finish; if the new version crashes it rolls back on its own.\n",
		res.Commit, map[bool]string{true: " (pushed)", false: ""}[res.Pushed])
	return 0
}

func selfReviewLoop(ctx context.Context, runner *agent.Runner, cfg *config.Config, review func()) {
	last := time.Now()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s := runner.Settings()
			if s.SelfReview && strings.TrimSpace(s.SelfReviewCron) != "" {
				if next, err := scheduler.Next(s.SelfReviewCron, "", last, cfg.Timezone); err == nil && !next.After(now) {
					go review()
				}
			}
			last = now
		}
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
