// Package vault connects BLAVINGAD to a Vaultwarden (or Bitwarden) server
// through the official Bitwarden CLI (`bw`), which does all the crypto.
//
// The decrypted vault is kept in memory only. The master password and the
// session key are passed exclusively to `bw` child processes, never to the
// agent's own tools.
package vault

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Config struct {
	URL          string
	ClientID     string
	ClientSecret string
	Email        string
	Password     string
	Folder       string
	CACert       string
	DataDir      string
	Binary       string
	// Organization/Collection (name or id) make vault_save write into an
	// organization collection instead of the personal vault — needed when
	// an org policy removes the individual vault.
	Organization string
	Collection   string
}

func ConfigFromEnv() Config {
	get := func(k string) string { return strings.TrimSpace(os.Getenv(k)) }
	c := Config{
		URL: strings.TrimRight(get("VAULT_URL"), "/"), ClientID: get("VAULT_CLIENT_ID"), ClientSecret: get("VAULT_CLIENT_SECRET"),
		Email: get("VAULT_EMAIL"), Password: get("VAULT_MASTER_PASSWORD"), Folder: get("VAULT_FOLDER"), CACert: get("VAULT_CA_CERT"),
		Binary: get("VAULT_BW_BIN"), Organization: get("VAULT_ORGANIZATION"), Collection: get("VAULT_COLLECTION"),
	}
	if c.Folder == "" {
		c.Folder = "BLAVINGAD"
	}
	if c.Binary == "" {
		c.Binary = "bw"
	}
	dataDir := get("DATA_DIR")
	if dataDir == "" {
		dataDir = "/data"
	}
	c.DataDir = filepath.Join(dataDir, "vault-cli")
	return c
}

func (c Config) Enabled() bool {
	return c.URL != "" && c.Password != "" && (c.ClientID != "" || c.Email != "")
}

type Field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Type  int    `json:"type"` // 0 text, 1 hidden, 2 boolean
}

type Item struct {
	ID       string   `json:"id"`
	Type     int      `json:"type"` // 1 login, 2 note, 3 card, 4 identity
	Name     string   `json:"name"`
	FolderID string   `json:"folderId"`
	OrgID    string   `json:"organizationId"`
	CollIDs  []string `json:"collectionIds"`
	Notes    string   `json:"notes"`
	Fields   []Field  `json:"fields"`
	Login    *struct {
		Username string `json:"username"`
		Password string `json:"password"`
		TOTP     string `json:"totp"`
		URIs     []struct {
			URI string `json:"uri"`
		} `json:"uris"`
	} `json:"login"`
	Revision string `json:"revisionDate"`
}

type Client struct {
	cfg Config

	mu       sync.RWMutex
	session  string
	items    []Item
	folders  map[string]string // id → name
	orgs     map[string]string // id → name
	colls    []Collection
	lastSync time.Time
	lastErr  string
	opMu     sync.Mutex
}

// Collection is an organization collection the account can access.
type Collection struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	OrgID string `json:"organizationId"`
}

func New(cfg Config) *Client {
	return &Client{cfg: cfg, folders: map[string]string{}, orgs: map[string]string{}}
}

func (c *Client) Enabled() bool { return c.cfg.Enabled() }

func (c *Client) Folder() string { return c.cfg.Folder }

// bw runs the CLI with a minimal environment: nothing from our own process
// leaks into it, and the session/password never leak out of it.
func (c *Client) bw(ctx context.Context, extraEnv []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.cfg.Binary, args...)
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + c.cfg.DataDir, "BITWARDENCLI_APPDATA_DIR=" + c.cfg.DataDir,
		"NODE_NO_WARNINGS=1", "BW_NOINTERACTION=true", "BW_NO_COLOR=true"}
	if c.cfg.CACert != "" {
		env = append(env, "NODE_EXTRA_CA_CERTS="+c.cfg.CACert)
	}
	c.mu.RLock()
	if c.session != "" {
		env = append(env, "BW_SESSION="+c.session)
	}
	c.mu.RUnlock()
	cmd.Env = append(env, extraEnv...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		msg := cleanErr(errb.String() + "\n" + out.String())
		if ctx.Err() == context.DeadlineExceeded {
			msg = "timed out"
		}
		return "", fmt.Errorf("bw %s: %s", args[0], msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// cleanErr keeps the meaningful line of a bw error (it prints JS stacks).
func cleanErr(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "at ") || strings.HasPrefix(line, "Could not find") {
			continue
		}
		if len(line) > 300 {
			line = line[:300]
		}
		return line
	}
	return "unknown error"
}

type status struct {
	ServerURL string `json:"serverUrl"`
	Status    string `json:"status"`
	UserEmail string `json:"userEmail"`
}

func (c *Client) status(ctx context.Context) (status, error) {
	var st status
	out, err := c.bw(ctx, nil, "status")
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal([]byte(lastJSONLine(out)), &st)
}

// lastJSONLine skips warnings bw may print before its JSON (which may span
// several lines) and returns the JSON document.
func lastJSONLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "{") || strings.HasPrefix(l, "[") {
			return strings.Join(lines[i:], "\n")
		}
	}
	return s
}

// Open logs in if needed, unlocks and loads the vault.
func (c *Client) Open(ctx context.Context) error {
	if !c.Enabled() {
		return errors.New("vault not configured")
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := os.MkdirAll(c.cfg.DataDir, 0o700); err != nil {
		return err
	}
	c.mu.Lock()
	c.session = ""
	c.mu.Unlock()
	st, err := c.status(ctx)
	if err != nil {
		return c.fail(err)
	}
	if st.Status != "unauthenticated" && st.ServerURL != c.cfg.URL {
		c.bw(ctx, nil, "logout")
		st.Status = "unauthenticated"
	}
	freshLogin := false
	if st.Status == "unauthenticated" {
		if err := c.login(ctx); err != nil {
			return c.fail(err)
		}
		freshLogin = true
	}
	session, err := c.unlock(ctx)
	if err != nil && !freshLogin && isDecryptError(err) {
		// Stale local data (master password or KDF changed, other account
		// cached…): start over from a clean login once.
		slog.Warn("vault unlock failed with the cached login, logging in again", "err", err)
		c.bw(ctx, nil, "logout")
		if lerr := c.login(ctx); lerr != nil {
			return c.fail(lerr)
		}
		session, err = c.unlock(ctx)
	}
	if err != nil {
		return c.fail(c.explainUnlockError(ctx, err))
	}
	c.mu.Lock()
	c.session = strings.TrimSpace(session)
	c.mu.Unlock()
	return c.load(ctx, false)
}

func (c *Client) login(ctx context.Context) error {
	if _, err := c.bw(ctx, nil, "config", "server", c.cfg.URL); err != nil {
		return err
	}
	var err error
	if c.cfg.ClientID != "" {
		_, err = c.bw(ctx, []string{"BW_CLIENTID=" + c.cfg.ClientID, "BW_CLIENTSECRET=" + c.cfg.ClientSecret}, "login", "--apikey")
	} else {
		_, err = c.bw(ctx, []string{"BW_PASSWORD=" + c.cfg.Password}, "login", c.cfg.Email, "--passwordenv", "BW_PASSWORD", "--raw")
	}
	if err != nil {
		return fmt.Errorf("vault login: %w", err)
	}
	return nil
}

// unlock tries the master password as given, then without the quotes an
// env file may have kept around it (podman/docker --env-file keep them).
func (c *Client) unlock(ctx context.Context) (string, error) {
	session, err := c.bw(ctx, []string{"BW_PASSWORD=" + c.cfg.Password}, "unlock", "--passwordenv", "BW_PASSWORD", "--raw")
	if err != nil && isDecryptError(err) {
		if p := unquote(c.cfg.Password); p != c.cfg.Password {
			if s, qerr := c.bw(ctx, []string{"BW_PASSWORD=" + p}, "unlock", "--passwordenv", "BW_PASSWORD", "--raw"); qerr == nil {
				slog.Warn("VAULT_MASTER_PASSWORD is wrapped in quotes in the env file: they were stripped — remove them from .env")
				c.cfg.Password = p
				return s, nil
			}
		}
	}
	return session, err
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

func isDecryptError(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "decrypt") || strings.Contains(m, "invalid master password") || strings.Contains(m, "master_key")
}

// explainUnlockError says what to check, without ever revealing the password.
func (c *Client) explainUnlockError(ctx context.Context, err error) error {
	if !isDecryptError(err) {
		return fmt.Errorf("unlock: %w", err)
	}
	account := "the logged-in account"
	if st, serr := c.status(ctx); serr == nil && st.UserEmail != "" {
		account = st.UserEmail
	}
	var hints []string
	p := c.cfg.Password
	if strings.ContainsRune(p, '$') {
		hints = append(hints, "it contains a \"$\": docker compose interpolates it in env files, write \"$$\" instead")
	}
	if strings.ContainsAny(p, `"'`) {
		hints = append(hints, "it contains quotes: env files passed with --env-file keep them literally")
	}
	if strings.ContainsRune(p, '#') {
		hints = append(hints, "it contains a \"#\", which some env-file parsers treat as a comment")
	}
	if c.cfg.ClientID != "" {
		hints = append(hints, "the API key (VAULT_CLIENT_ID) must belong to the same account as this master password")
	}
	msg := fmt.Sprintf("unlock: VAULT_MASTER_PASSWORD does not decrypt the vault of %s (%d characters received)", account, len([]rune(p)))
	if len(hints) > 0 {
		msg += " — check: " + strings.Join(hints, "; ")
	}
	return fmt.Errorf("%s. CLI said: %v", msg, err)
}

// ensureOpen (re)opens the vault when the boot-time unlock failed, so a
// write never runs without a session.
func (c *Client) ensureOpen(ctx context.Context) error {
	c.mu.RLock()
	open := c.session != ""
	c.mu.RUnlock()
	if open {
		return nil
	}
	return c.Open(ctx)
}

func isLockedError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "vault is locked")
}

func (c *Client) fail(err error) error {
	c.mu.Lock()
	c.lastErr = err.Error()
	c.mu.Unlock()
	return err
}

// Refresh pulls the latest version of the vault from the server (and
// opens it first if the boot-time unlock failed).
func (c *Client) Refresh(ctx context.Context) error {
	c.mu.RLock()
	open := c.session != ""
	c.mu.RUnlock()
	if !open {
		return c.Open(ctx)
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()
	return c.load(ctx, true)
}

func (c *Client) load(ctx context.Context, sync bool) error {
	if sync {
		if _, err := c.bw(ctx, nil, "sync"); err != nil {
			return c.fail(err)
		}
	}
	out, err := c.bw(ctx, nil, "list", "items")
	if err != nil {
		return c.fail(err)
	}
	var items []Item
	if err := json.Unmarshal([]byte(lastJSONLine(out)), &items); err != nil {
		return c.fail(fmt.Errorf("unreadable vault response: %w", err))
	}
	folders := map[string]string{}
	if fo, err := c.bw(ctx, nil, "list", "folders"); err == nil {
		var list []struct{ ID, Name string }
		if json.Unmarshal([]byte(lastJSONLine(fo)), &list) == nil {
			for _, f := range list {
				if f.ID != "" {
					folders[f.ID] = f.Name
				}
			}
		}
	}
	orgs := map[string]string{}
	if out, err := c.bw(ctx, nil, "list", "organizations"); err == nil {
		var list []struct{ ID, Name string }
		if json.Unmarshal([]byte(lastJSONLine(out)), &list) == nil {
			for _, o := range list {
				orgs[o.ID] = o.Name
			}
		}
	}
	var colls []Collection
	if len(orgs) > 0 {
		if out, err := c.bw(ctx, nil, "list", "collections"); err == nil {
			json.Unmarshal([]byte(lastJSONLine(out)), &colls)
		}
	}
	c.mu.Lock()
	c.items, c.folders, c.orgs, c.colls, c.lastSync, c.lastErr = items, folders, orgs, colls, time.Now(), ""
	c.mu.Unlock()
	slog.Info("vault loaded", "items", len(items))
	return nil
}

type Status struct {
	Enabled  bool   `json:"enabled"`
	Unlocked bool   `json:"unlocked"`
	URL      string `json:"url"`
	Folder   string `json:"folder"`
	Items    int    `json:"items"`
	LastSync string `json:"last_sync"`
	Error    string `json:"error"`
	Auth     string `json:"auth"`
	Writes   string `json:"writes"` // where vault_save puts new items
}

func (c *Client) Status() Status {
	writes := ""
	if t, err := c.WriteTarget(""); err == nil {
		writes = t.String()
	} else {
		writes = "⚠️ " + err.Error()
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := Status{Enabled: c.Enabled(), Unlocked: c.session != "" && c.lastErr == "", URL: c.cfg.URL, Folder: c.cfg.Folder,
		Items: len(c.items), Error: c.lastErr, Auth: "API key", Writes: writes}
	if c.cfg.ClientID == "" {
		s.Auth = "email + password"
	}
	if !c.lastSync.IsZero() {
		s.LastSync = c.lastSync.UTC().Format(time.RFC3339)
	}
	return s
}

// ---------- lookup ----------

func (c *Client) inFolder(it Item) bool {
	return strings.EqualFold(c.folders[it.FolderID], c.cfg.Folder)
}

// Find resolves an item by id or (case-insensitive) name; an item inside the
// agent's folder wins over a homonym elsewhere.
func (c *Client) Find(ref string) (Item, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.session == "" {
		if c.lastErr != "" {
			return Item{}, errors.New("vault locked: " + c.lastErr)
		}
		return Item{}, errors.New("vault locked or unavailable")
	}
	var matches []Item
	for _, it := range c.items {
		if it.ID == ref {
			return it, nil
		}
		if strings.EqualFold(strings.TrimSpace(it.Name), strings.TrimSpace(ref)) {
			matches = append(matches, it)
		}
	}
	switch len(matches) {
	case 0:
		return Item{}, fmt.Errorf("no item \"%s\" in the vault", ref)
	case 1:
		return matches[0], nil
	}
	var inFolder []Item
	for _, it := range matches {
		if c.inFolder(it) {
			inFolder = append(inFolder, it)
		}
	}
	if len(inFolder) == 1 {
		return inFolder[0], nil
	}
	return Item{}, fmt.Errorf("several items are named \"%s\": use their ID", ref)
}

var builtinFields = map[string]bool{"password": true, "username": true, "notes": true, "totp": true, "uri": true}

// Value extracts a field: password (default), username, notes, uri, totp
// (current code) or any custom field name.
func (c *Client) Value(ctx context.Context, it Item, field string) (string, error) {
	field = strings.TrimSpace(field)
	f := strings.ToLower(field)
	switch f {
	case "", "password":
		if it.Login != nil && it.Login.Password != "" {
			return it.Login.Password, nil
		}
		if field == "" {
			// Convenience: a note or a single hidden field also counts as "the" secret.
			for _, fl := range it.Fields {
				if fl.Type == 1 && fl.Value != "" {
					return fl.Value, nil
				}
			}
			if strings.TrimSpace(it.Notes) != "" {
				return strings.TrimSpace(it.Notes), nil
			}
		}
		return "", fmt.Errorf("\"%s\" has no password", it.Name)
	case "username":
		if it.Login != nil && it.Login.Username != "" {
			return it.Login.Username, nil
		}
	case "notes":
		if it.Notes != "" {
			return it.Notes, nil
		}
	case "uri":
		if it.Login != nil && len(it.Login.URIs) > 0 {
			return it.Login.URIs[0].URI, nil
		}
	case "totp":
		if it.Login == nil || it.Login.TOTP == "" {
			return "", fmt.Errorf("\"%s\" has no TOTP", it.Name)
		}
		code, err := c.bw(ctx, nil, "get", "totp", it.ID)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(code), nil
	default:
		for _, fl := range it.Fields {
			if strings.EqualFold(fl.Name, field) {
				return fl.Value, nil
			}
		}
	}
	return "", fmt.Errorf("field \"%s\" is empty or missing in \"%s\"", field, it.Name)
}

// ParseRef splits "vault://Item name/field". The part after the last "/"
// is a field only if it names one; otherwise the whole string is the item.
func (c *Client) ParseRef(ref string) (item, field string, ok bool) {
	if !strings.HasPrefix(ref, "vault://") {
		return "", "", false
	}
	rest := strings.TrimPrefix(ref, "vault://")
	if i := strings.LastIndex(rest, "/"); i > 0 {
		name, f := rest[:i], rest[i+1:]
		if builtinFields[strings.ToLower(f)] {
			return name, f, true
		}
		if it, err := c.Find(name); err == nil {
			for _, fl := range it.Fields {
				if strings.EqualFold(fl.Name, f) {
					return name, f, true
				}
			}
		}
	}
	return rest, "", true
}

// Resolve turns a vault:// reference into its value.
func (c *Client) Resolve(ctx context.Context, ref string) (string, error) {
	name, field, ok := c.ParseRef(ref)
	if !ok {
		return "", fmt.Errorf("invalid reference (expected vault://Item/field): %s", ref)
	}
	it, err := c.Find(name)
	if err != nil {
		return "", err
	}
	return c.Value(ctx, it, field)
}

// Secrets returns every secret-looking value in the vault, for masking.
func (c *Client) Secrets() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []string
	for _, it := range c.items {
		if it.Login != nil {
			out = append(out, it.Login.Password, it.Login.TOTP)
		}
		for _, f := range it.Fields {
			if f.Type == 1 {
				out = append(out, f.Value)
			}
		}
		if it.Type == 2 {
			out = append(out, strings.TrimSpace(it.Notes))
		}
	}
	return out
}

type Meta struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Folder      string   `json:"folder"`
	Username    string   `json:"username,omitempty"`
	URI         string   `json:"uri,omitempty"`
	HasPassword bool     `json:"has_password"`
	HasTOTP     bool     `json:"has_totp"`
	HasNotes    bool     `json:"has_notes"`
	Fields      []string `json:"fields,omitempty"`
	Collections []string `json:"collections,omitempty"`
	Reference   string   `json:"reference"`
}

func typeName(t int) string {
	return map[int]string{1: "login", 2: "secure note", 3: "card", 4: "identity"}[t]
}

// Search lists items matching a query — metadata only, never secret values.
func (c *Client) Search(query string, limit int) []Meta {
	c.mu.RLock()
	defer c.mu.RUnlock()
	q := strings.ToLower(strings.TrimSpace(query))
	var out []Meta
	for _, it := range c.items {
		m := Meta{ID: it.ID, Name: it.Name, Type: typeName(it.Type), Folder: c.folders[it.FolderID], HasNotes: it.Notes != "", Reference: "vault://" + it.Name}
		if it.Login != nil {
			m.Username, m.HasPassword, m.HasTOTP = it.Login.Username, it.Login.Password != "", it.Login.TOTP != ""
			if len(it.Login.URIs) > 0 {
				m.URI = it.Login.URIs[0].URI
			}
		}
		for _, f := range it.Fields {
			m.Fields = append(m.Fields, f.Name)
		}
		for _, id := range it.CollIDs {
			for _, col := range c.colls {
				if col.ID == id {
					m.Collections = append(m.Collections, col.Name)
				}
			}
		}
		hay := strings.ToLower(strings.Join(append([]string{it.Name, m.Username, m.URI, m.Folder}, m.Fields...), " "))
		if q == "" || strings.Contains(hay, q) {
			out = append(out, m)
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// ---------- write ----------

type SaveInput struct {
	Name     string
	Kind     string // login (default) | note
	Username string
	Password string
	Notes    string
	URI      string
	Fields   map[string]string
	// Collection (name or id) overrides where the item goes; "personal"
	// forces the personal vault.
	Collection string
	// ID edits that exact item (wherever it lives) instead of matching by
	// name in the write target; Name then renames it.
	ID string
	// RemoveFields deletes these custom fields (case-insensitive).
	RemoveFields []string
}

// Target is where new items are written.
type Target struct {
	Collection *Collection // nil = personal vault
	OrgName    string
	Reason     string // how it was chosen
}

func (t Target) String() string {
	if t.Collection == nil {
		return "personal vault"
	}
	return fmt.Sprintf("collection %q (organization %q)", t.Collection.Name, t.OrgName)
}

func encode(v any) string {
	b, _ := json.Marshal(v)
	return base64.StdEncoding.EncodeToString(b)
}

func (c *Client) collectionNames() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []string
	for _, col := range c.colls {
		out = append(out, fmt.Sprintf("%q (%s)", col.Name, c.orgs[col.OrgID]))
	}
	return out
}

// findCollection matches a collection by id or name, optionally within an
// organization (name or id).
func (c *Client) findCollection(ref, org string) (*Collection, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var matches []Collection
	for _, col := range c.colls {
		if org != "" && col.OrgID != org && !strings.EqualFold(c.orgs[col.OrgID], org) {
			continue
		}
		if col.ID == ref || strings.EqualFold(col.Name, ref) {
			matches = append(matches, col)
		}
	}
	switch len(matches) {
	case 1:
		return &matches[0], nil
	case 0:
		return nil, fmt.Errorf("no collection %q is shared with the bot's account", ref)
	}
	return nil, fmt.Errorf("several collections are named %q: set VAULT_ORGANIZATION or use the collection id", ref)
}

// WriteTarget decides where an item goes: the call's collection, else
// VAULT_COLLECTION, else the personal vault.
func (c *Client) WriteTarget(override string) (Target, error) {
	ref, reason := override, "requested"
	if ref == "" {
		ref, reason = c.cfg.Collection, "VAULT_COLLECTION"
	}
	if ref == "" || strings.EqualFold(ref, "personal") {
		return Target{Reason: "default"}, nil
	}
	col, err := c.findCollection(ref, c.cfg.Organization)
	if err != nil {
		if names := c.collectionNames(); len(names) > 0 {
			err = fmt.Errorf("%w (available: %s)", err, strings.Join(names, ", "))
		}
		return Target{}, err
	}
	c.mu.RLock()
	org := c.orgs[col.OrgID]
	c.mu.RUnlock()
	return Target{Collection: col, OrgName: org, Reason: reason}, nil
}

func (c *Client) folderID(ctx context.Context) (string, error) {
	c.mu.RLock()
	for id, name := range c.folders {
		if strings.EqualFold(name, c.cfg.Folder) {
			c.mu.RUnlock()
			return id, nil
		}
	}
	c.mu.RUnlock()
	out, err := c.bw(ctx, nil, "create", "folder", encode(map[string]string{"name": c.cfg.Folder}))
	if err != nil {
		return "", err
	}
	var f struct{ ID string }
	json.Unmarshal([]byte(lastJSONLine(out)), &f)
	if f.ID != "" {
		c.mu.Lock()
		c.folders[f.ID] = c.cfg.Folder
		c.mu.Unlock()
	}
	return f.ID, nil
}

// explainWriteError turns the CLI's terse refusals into something a human
// (and the model) can act on.
func (c *Client) explainWriteError(err error, t Target) error {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "individual vault") || strings.Contains(msg, "enterprise policy") || strings.Contains(msg, "personal ownership"):
		hint := "an organization policy forbids items in the bot's personal vault: set VAULT_COLLECTION to a collection the bot can edit"
		if names := c.collectionNames(); len(names) > 0 {
			hint += " (shared with it: " + strings.Join(names, ", ") + "), or pass collection=… to vault_save"
		} else {
			hint += " — no collection is shared with its account yet"
		}
		return fmt.Errorf("%s. CLI said: %v", hint, err)
	case strings.Contains(msg, "permission") || strings.Contains(msg, "not allowed") || strings.Contains(msg, "read only") || strings.Contains(msg, "readonly") || strings.Contains(msg, "forbidden"):
		return fmt.Errorf("the bot's Vaultwarden account can read but not write the %s: give it \"Can edit\" (or \"Can manage\") on that collection in Vaultwarden. CLI said: %v", t, err)
	}
	return err
}

// Save creates or updates (by name, in the write target) an item.
func (c *Client) Save(ctx context.Context, in SaveInput) (Item, bool, error) {
	if strings.TrimSpace(in.Name) == "" {
		return Item{}, false, errors.New("a name is required")
	}
	if err := c.ensureOpen(ctx); err != nil {
		return Item{}, false, err
	}
	t, err := c.WriteTarget(in.Collection)
	if err != nil {
		return Item{}, false, err
	}
	item, updated, err := c.save(ctx, in, t)
	if isLockedError(err) {
		if oerr := c.Open(ctx); oerr != nil {
			return Item{}, false, oerr
		}
		item, updated, err = c.save(ctx, in, t)
	}
	if err != nil && t.Collection == nil && in.Collection == "" {
		// Personal vault refused (org policy)? If exactly one collection is
		// shared with the bot, that is the obvious place: use it.
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "individual vault") || strings.Contains(lower, "enterprise policy") || strings.Contains(lower, "personal ownership") {
			c.mu.RLock()
			only := len(c.colls) == 1
			var col Collection
			if only {
				col = c.colls[0]
			}
			org := c.orgs[col.OrgID]
			c.mu.RUnlock()
			if only {
				slog.Info("personal vault forbidden by policy, writing to the only shared collection", "collection", col.Name)
				return c.save(ctx, in, Target{Collection: &col, OrgName: org, Reason: "only collection shared with the bot"})
			}
		}
	}
	return item, updated, err
}

func (c *Client) save(ctx context.Context, in SaveInput, t Target) (Item, bool, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	fid, err := c.folderID(ctx)
	if err != nil {
		slog.Warn("vault folder unavailable, saving without folder", "err", err)
		fid = ""
	}
	inTarget := func(it Item) bool {
		if t.Collection != nil {
			return it.OrgID == t.Collection.OrgID && containsStr(it.CollIDs, t.Collection.ID)
		}
		return it.OrgID == "" && (fid == "" || it.FolderID == fid)
	}
	var existing *Item
	c.mu.RLock()
	for _, it := range c.items {
		if in.ID != "" && it.ID == in.ID {
			cp := it
			existing = &cp
			break
		}
		if in.ID == "" && strings.EqualFold(it.Name, in.Name) && inTarget(it) {
			cp := it
			existing = &cp
			break
		}
	}
	c.mu.RUnlock()
	if in.ID != "" && existing == nil {
		return Item{}, false, fmt.Errorf("no item with id %s in the vault", in.ID)
	}

	var raw map[string]any
	if existing != nil {
		out, err := c.bw(ctx, nil, "get", "item", existing.ID)
		if err != nil {
			return Item{}, false, err
		}
		json.Unmarshal([]byte(lastJSONLine(out)), &raw)
	} else {
		raw = map[string]any{"name": in.Name, "notes": "", "fields": []any{}, "reprompt": 0, "favorite": false}
		if fid != "" {
			raw["folderId"] = fid
		}
		if in.Kind == "note" {
			raw["type"] = 2
			raw["secureNote"] = map[string]any{"type": 0}
		} else {
			raw["type"] = 1
			raw["login"] = map[string]any{"username": "", "password": "", "uris": []any{}}
		}
		if t.Collection != nil {
			raw["organizationId"] = t.Collection.OrgID
			raw["collectionIds"] = []string{t.Collection.ID}
		}
	}
	if login, _ := raw["login"].(map[string]any); login != nil {
		if in.Username != "" {
			login["username"] = in.Username
		}
		if in.Password != "" {
			login["password"] = in.Password
		}
		if in.URI != "" {
			login["uris"] = []map[string]any{{"uri": in.URI, "match": nil}}
		}
	} else if in.Password != "" {
		// Secure note: the secret goes into a hidden field.
		if in.Fields == nil {
			in.Fields = map[string]string{}
		}
		in.Fields["secret"] = in.Password
	}
	if existing != nil && strings.TrimSpace(in.Name) != "" {
		raw["name"] = in.Name
	}
	if in.Notes != "" {
		raw["notes"] = in.Notes
	}
	if len(in.RemoveFields) > 0 {
		fields, _ := raw["fields"].([]any)
		kept := []any{}
		for _, f := range fields {
			if fm, ok := f.(map[string]any); ok && containsFold(in.RemoveFields, fmt.Sprint(fm["name"])) {
				continue
			}
			kept = append(kept, f)
		}
		raw["fields"] = kept
	}
	if len(in.Fields) > 0 {
		fields, _ := raw["fields"].([]any)
		names := make([]string, 0, len(in.Fields))
		for name := range in.Fields {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			value := in.Fields[name]
			replaced := false
			for _, f := range fields {
				if fm, ok := f.(map[string]any); ok && strings.EqualFold(fmt.Sprint(fm["name"]), name) {
					fm["value"], replaced = value, true
				}
			}
			if !replaced {
				fields = append(fields, map[string]any{"name": name, "value": value, "type": 1})
			}
		}
		raw["fields"] = fields
	}
	var out string
	if existing != nil {
		out, err = c.bw(ctx, nil, "edit", "item", existing.ID, encode(raw))
	} else if t.Collection != nil {
		out, err = c.bw(ctx, nil, "create", "item", encode(raw), "--organizationid", t.Collection.OrgID)
	} else {
		out, err = c.bw(ctx, nil, "create", "item", encode(raw))
	}
	if err != nil {
		return Item{}, false, c.explainWriteError(err, t)
	}
	var saved Item
	json.Unmarshal([]byte(lastJSONLine(out)), &saved)
	c.load(ctx, false)
	return saved, existing != nil, nil
}

func containsFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(v)) {
			return true
		}
	}
	return false
}

// Delete moves an item (by id) to the vault's trash.
func (c *Client) Delete(ctx context.Context, id string) (string, error) {
	if err := c.ensureOpen(ctx); err != nil {
		return "", err
	}
	c.mu.RLock()
	name := ""
	for _, it := range c.items {
		if it.ID == id {
			name = it.Name
		}
	}
	c.mu.RUnlock()
	if name == "" {
		return "", fmt.Errorf("no item with id %s in the vault", id)
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if _, err := c.bw(ctx, nil, "delete", "item", id); err != nil {
		return "", c.explainWriteError(err, Target{})
	}
	c.load(ctx, false)
	return name, nil
}

// Collections lists the collections shared with the account ("Org / Name").
func (c *Client) Collections() []map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := []map[string]string{}
	for _, col := range c.colls {
		out = append(out, map[string]string{"id": col.ID, "name": col.Name, "org": c.orgs[col.OrgID]})
	}
	return out
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// Generate makes a strong random password with the CLI's generator.
func (c *Client) Generate(ctx context.Context, length int) (string, error) {
	if length < 12 || length > 128 {
		length = 32
	}
	// The generator works offline and without a session.
	return c.bw(ctx, nil, "generate", "-ulns", "--length", fmt.Sprint(length))
}

// ---------- environment ----------

// Snapshot captures the raw environment values of the given keys before
// anything is resolved or scrubbed.
func Snapshot(keys []string) map[string]string {
	raw := map[string]string{}
	for _, k := range keys {
		raw[k] = strings.TrimSpace(os.Getenv(k))
	}
	return raw
}

// ResolveEnv computes configuration values from the vault:
//   - KEY=vault://Item/field → resolved;
//   - KEY unset → an item named KEY in the agent's folder, if any;
//   - anything else is a literal value and is left alone.
//
// It returns the resolved values and, per key, the reference they came from.
func (c *Client) ResolveEnv(ctx context.Context, raw map[string]string) (values, sources map[string]string, errs []string) {
	values, sources = map[string]string{}, map[string]string{}
	for k, cur := range raw {
		var ref string
		switch {
		case strings.HasPrefix(cur, "vault://"):
			ref = cur
		case cur == "":
			it, err := c.Find(k)
			if err != nil || !c.inFolderLocked(it) {
				continue
			}
			ref = "vault://" + it.Name
		default:
			continue
		}
		v, err := c.Resolve(ctx, ref)
		if err != nil {
			errs = append(errs, k+": "+err.Error())
			continue
		}
		values[k], sources[k] = v, ref
	}
	return values, sources, errs
}

func (c *Client) inFolderLocked(it Item) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.inFolder(it)
}
