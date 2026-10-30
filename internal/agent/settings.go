package agent

import (
	"strconv"

	"blavingad/internal/config"
	"blavingad/internal/store"
)

// Settings are the runtime knobs editable from the web UI. Defaults come
// from the environment.
type Settings struct {
	APIStyle       string  `json:"api_style"` // openai | anthropic
	BaseURL        string  `json:"base_url"`  // "" = LLM_BASE_URL
	Model          string  `json:"model"`
	FastModel      string  `json:"fast_model"`
	Temperature    float64 `json:"temperature"`
	MaxIterations  int     `json:"max_iterations"`
	Reflection     bool    `json:"reflection"`
	SelfReview     bool    `json:"self_review"`
	SelfReviewCron string  `json:"self_review_cron"`
	FollowThreads  bool    `json:"follow_threads"`
	EmailAuto      bool    `json:"email_auto"`
	EmailProfile   string  `json:"email_profile"` // access profile of email senders
	HomeChannel    string  `json:"home_channel"`
	ChannelDigest  bool    `json:"channel_digest"`
	ExtraPrompt    string  `json:"extra_prompt"`
	// BotReplyLimit caps consecutive bot messages answered in one thread
	// until a human writes again (0 = never answer other bots).
	BotReplyLimit int `json:"bot_reply_limit"`
}

var SettingKeys = []string{"api_style", "base_url", "model", "fast_model", "temperature", "max_iterations", "reflection", "self_review", "self_review_cron", "follow_threads", "email_auto", "email_profile", "home_channel", "channel_digest", "extra_prompt", "bot_reply_limit"}

func LoadSettings(st *store.Store, cfg *config.Config) Settings {
	get := func(k, def string) string { return st.GetSetting("agent."+k, def) }
	b := func(k string, def bool) bool {
		v := get(k, strconv.FormatBool(def))
		return v == "true" || v == "1"
	}
	temp, err := strconv.ParseFloat(get("temperature", ""), 64)
	if err != nil {
		temp = cfg.LLMTemperature
	}
	iters, err := strconv.Atoi(get("max_iterations", ""))
	if err != nil || iters <= 0 {
		iters = 30
	}
	botLimit, err := strconv.Atoi(get("bot_reply_limit", ""))
	if err != nil || botLimit < 0 {
		botLimit = 10
	}
	return Settings{
		APIStyle:       get("api_style", cfg.LLMStyle),
		BaseURL:        get("base_url", ""),
		Model:          get("model", cfg.LLMModel),
		FastModel:      get("fast_model", cfg.LLMFastModel),
		Temperature:    temp,
		MaxIterations:  iters,
		Reflection:     b("reflection", true),
		SelfReview:     b("self_review", true),
		SelfReviewCron: get("self_review_cron", "30 4 * * *"),
		FollowThreads:  b("follow_threads", true),
		EmailAuto:      b("email_auto", false),
		EmailProfile:   get("email_profile", "guest"),
		HomeChannel:    get("home_channel", cfg.SlackHomeChannel),
		ChannelDigest:  b("channel_digest", true),
		ExtraPrompt:    get("extra_prompt", ""),
		BotReplyLimit:  botLimit,
	}
}
