package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // TZ works even in images without zoneinfo
)

// Config is the immutable, environment-derived configuration. Anything that
// should be editable at runtime lives in the settings table instead.
type Config struct {
	AgentName    string
	DataDir      string
	WorkspaceDir string
	SourceDir    string
	Timezone     *time.Location
	TZName       string
	Language     string

	LLMBaseURL      string
	LLMAPIKey       string
	LLMModel        string
	LLMFastModel    string
	LLMMaxTokens    int
	LLMContextLimit int
	LLMTemperature  float64
	LLMHeaders      map[string]string
	LLMStyle        string // openai | anthropic

	EmbedBaseURL string
	EmbedAPIKey  string
	EmbedModel   string

	SlackBotToken    string
	SlackAppToken    string
	SlackAdmins      []string
	SlackHomeChannel string

	ExaAPIKey string

	GitHubToken    string
	GitHubUser     string
	GitAuthorName  string
	GitAuthorEmail string
	SelfRepo       string
	SelfRepoBranch string

	IMAPHost     string
	IMAPPort     int
	IMAPUser     string
	IMAPPassword string
	IMAPTLS      bool
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	SMTPMode     string // starttls | tls | plain
	EmailFrom    string
	EmailName    string
	EmailPoll    time.Duration

	CloudflareToken   string
	CloudflareAccount string

	DockerEnabled  bool
	DeployHost     string
	DeployDomain   string
	DeployNetwork  string
	DeployTraefik  bool
	DeployPortBase int

	WebAddr           string
	PublicURL         string
	SessionSecret     string
	OIDCIssuer        string
	OIDCClientID      string
	OIDCClientSecret  string
	OIDCAllowedEmails []string
	OIDCAllowedGroups []string
	AdminPassword     string

	BinaryDir string
}

func Load() *Config {
	c := &Config{
		AgentName:    env("AGENT_NAME", "BLAVINGAD"),
		DataDir:      env("DATA_DIR", "/data"),
		WorkspaceDir: env("WORKSPACE_DIR", "/workspace"),
		Language:     env("AGENT_LANGUAGE", "English"),
		TZName:       env("TZ", "Europe/Paris"),

		LLMBaseURL:      strings.TrimRight(env("LLM_BASE_URL", "https://api.openai.com/v1"), "/"),
		LLMAPIKey:       env("LLM_API_KEY", ""),
		LLMModel:        env("LLM_MODEL", "gpt-4.1"),
		LLMMaxTokens:    envInt("LLM_MAX_TOKENS", 8192),
		LLMContextLimit: envInt("LLM_CONTEXT_TOKENS", 128000),
		LLMTemperature:  envFloat("LLM_TEMPERATURE", 0.4),

		EmbedModel: env("EMBED_MODEL", ""),

		SlackBotToken:    env("SLACK_BOT_TOKEN", ""),
		SlackAppToken:    env("SLACK_APP_TOKEN", ""),
		SlackAdmins:      list("SLACK_ADMINS"),
		SlackHomeChannel: env("SLACK_HOME_CHANNEL", ""),

		ExaAPIKey: env("EXA_API_KEY", ""),

		GitHubToken:    env("GITHUB_TOKEN", ""),
		GitHubUser:     env("GITHUB_USER", ""),
		SelfRepo:       env("SELF_REPO", ""),
		SelfRepoBranch: env("SELF_REPO_BRANCH", "main"),

		IMAPHost:     env("IMAP_HOST", ""),
		IMAPPort:     envInt("IMAP_PORT", 993),
		IMAPUser:     env("IMAP_USER", ""),
		IMAPPassword: env("IMAP_PASSWORD", ""),
		IMAPTLS:      envBool("IMAP_TLS", true),
		SMTPHost:     env("SMTP_HOST", ""),
		SMTPPort:     envInt("SMTP_PORT", 587),
		SMTPUser:     env("SMTP_USER", ""),
		SMTPPassword: env("SMTP_PASSWORD", ""),
		SMTPMode:     env("SMTP_MODE", "starttls"),
		EmailFrom:    env("EMAIL_FROM", ""),
		EmailPoll:    time.Duration(envInt("EMAIL_POLL_SECONDS", 90)) * time.Second,

		CloudflareToken:   env("CLOUDFLARE_API_TOKEN", ""),
		CloudflareAccount: env("CLOUDFLARE_ACCOUNT_ID", ""),

		DockerEnabled:  envBool("DOCKER_DEPLOY", fileExists("/var/run/docker.sock")),
		DeployHost:     env("DEPLOY_HOST", "localhost"),
		DeployDomain:   env("DEPLOY_DOMAIN", ""),
		DeployNetwork:  env("DEPLOY_NETWORK", ""),
		DeployTraefik:  envBool("DEPLOY_TRAEFIK", false),
		DeployPortBase: envInt("DEPLOY_PORT_BASE", 9100),

		WebAddr:           env("WEB_ADDR", ":8080"),
		PublicURL:         strings.TrimRight(env("PUBLIC_URL", "http://localhost:8080"), "/"),
		SessionSecret:     env("SESSION_SECRET", ""),
		OIDCIssuer:        env("OIDC_ISSUER", ""),
		OIDCClientID:      env("OIDC_CLIENT_ID", ""),
		OIDCClientSecret:  env("OIDC_CLIENT_SECRET", ""),
		OIDCAllowedEmails: lower(list("OIDC_ALLOWED_EMAILS")),
		OIDCAllowedGroups: list("OIDC_ALLOWED_GROUPS"),
		AdminPassword:     env("ADMIN_PASSWORD", ""),
	}
	c.LLMFastModel = env("LLM_FAST_MODEL", c.LLMModel)
	c.LLMHeaders = headers(env("LLM_HEADERS", ""))
	c.LLMStyle = strings.ToLower(env("LLM_API_STYLE", "openai"))
	if c.LLMStyle != "anthropic" {
		c.LLMStyle = "openai"
	}
	c.EmbedBaseURL = strings.TrimRight(env("EMBED_BASE_URL", c.LLMBaseURL), "/")
	c.EmbedAPIKey = env("EMBED_API_KEY", c.LLMAPIKey)
	c.SourceDir = env("SOURCE_DIR", filepath.Join(c.DataDir, "src"))
	if os.Getenv("SOURCE_DIR") == "" {
		// Set when the agent had to relocate an unwritable source tree.
		if b, err := os.ReadFile(filepath.Join(c.DataDir, "source.path")); err == nil {
			if p := strings.TrimSpace(string(b)); p != "" && fileExists(filepath.Join(p, "go.mod")) {
				c.SourceDir = p
			}
		}
	}
	c.BinaryDir = filepath.Join(c.DataDir, "bin")
	c.GitAuthorName = env("GIT_AUTHOR_NAME", c.AgentName)
	c.GitAuthorEmail = env("GIT_AUTHOR_EMAIL", c.EmailFrom)
	c.EmailName = env("EMAIL_FROM_NAME", c.AgentName)
	if c.SMTPUser == "" {
		c.SMTPUser = c.IMAPUser
	}
	if c.SMTPPassword == "" {
		c.SMTPPassword = c.IMAPPassword
	}
	if c.EmailFrom == "" {
		c.EmailFrom = c.SMTPUser
	}
	if loc, err := time.LoadLocation(c.TZName); err == nil {
		c.Timezone = loc
	} else {
		c.Timezone = time.UTC
	}
	return c
}

func (c *Config) LLMConfigured() bool    { return c.LLMBaseURL != "" && c.LLMModel != "" }
func (c *Config) EmbedConfigured() bool  { return c.EmbedModel != "" }
func (c *Config) SlackConfigured() bool  { return c.SlackBotToken != "" && c.SlackAppToken != "" }
func (c *Config) ExaConfigured() bool    { return c.ExaAPIKey != "" }
func (c *Config) GitHubConfigured() bool { return c.GitHubToken != "" }
func (c *Config) IMAPConfigured() bool   { return c.IMAPHost != "" && c.IMAPUser != "" }
func (c *Config) SMTPConfigured() bool   { return c.SMTPHost != "" && c.EmailFrom != "" }
func (c *Config) OIDCConfigured() bool   { return c.OIDCIssuer != "" && c.OIDCClientID != "" }
func (c *Config) CloudflareConfigured() bool {
	return c.CloudflareToken != "" && c.CloudflareAccount != ""
}

// VaultKeys are the settings that may live in Vaultwarden (as
// KEY=vault://Item/field, or as an item named KEY in the agent's folder).
// Slack tokens are deliberately absent: they stay in the environment.
var VaultKeys = []string{
	"LLM_API_KEY", "EMBED_API_KEY", "EXA_API_KEY",
	"GITHUB_TOKEN", "GITHUB_USER",
	"IMAP_USER", "IMAP_PASSWORD", "SMTP_USER", "SMTP_PASSWORD", "EMAIL_FROM",
	"CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID",
	"OIDC_CLIENT_ID", "OIDC_CLIENT_SECRET", "SESSION_SECRET", "ADMIN_PASSWORD",
}

// secretEnv lists variables removed from the process environment once the
// config is loaded, so tool subprocesses (shell, custom tools, builds) never
// inherit them.
var secretEnv = []string{
	"LLM_API_KEY", "EMBED_API_KEY", "EXA_API_KEY", "GITHUB_TOKEN", "IMAP_PASSWORD", "SMTP_PASSWORD",
	"CLOUDFLARE_API_TOKEN", "OIDC_CLIENT_SECRET", "SESSION_SECRET", "ADMIN_PASSWORD",
	"SLACK_BOT_TOKEN", "SLACK_APP_TOKEN",
	"VAULT_CLIENT_ID", "VAULT_CLIENT_SECRET", "VAULT_MASTER_PASSWORD", "BW_SESSION", "BW_PASSWORD", "BW_CLIENTID", "BW_CLIENTSECRET",
}

func ScrubEnv() {
	for _, k := range secretEnv {
		os.Unsetenv(k)
	}
}

// SecretValues returns every secret held by the config, for masking.
func (c *Config) SecretValues() []string {
	return []string{c.LLMAPIKey, c.EmbedAPIKey, c.ExaAPIKey, c.GitHubToken, c.IMAPPassword, c.SMTPPassword,
		c.CloudflareToken, c.OIDCClientSecret, c.SessionSecret, c.AdminPassword, c.SlackBotToken, c.SlackAppToken}
}

// headers parses "Name: value; Other-Name: value".
func headers(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ";") {
		if k, v, ok := strings.Cut(part, ":"); ok && strings.TrimSpace(k) != "" {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(env(key, "")); err == nil {
		return v
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	if v, err := strconv.ParseFloat(env(key, ""), 64); err == nil {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	switch strings.ToLower(env(key, "")) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return fallback
}

func list(key string) []string {
	var out []string
	for _, part := range strings.Split(env(key, ""), ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func lower(items []string) []string {
	for i := range items {
		items[i] = strings.ToLower(items[i])
	}
	return items
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
