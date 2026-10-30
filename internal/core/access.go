package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"blavingad/internal/store"
)

// Profile decides what a person can make the agent do. Tool tokens:
//
//	"*"            every tool
//	"cat:web"      every tool of a category
//	"web_search"   one tool
//	"-…"           exclusion (any of the forms above), wins over inclusions
type Profile struct {
	Name          string   `json:"name"`
	Label         string   `json:"label"`
	Emoji         string   `json:"emoji"`
	Description   string   `json:"description"`
	Tools         []string `json:"tools"`
	Memory        string   `json:"memory"`  // full | read | none
	Reply         bool     `json:"reply"`   // false = ignored
	Approve       string   `json:"approve"` // any | own | none
	MaxIterations int      `json:"max_iterations"`
	ExtraPrompt   string   `json:"extra_prompt"`
	Builtin       bool     `json:"builtin"`
	Members       int      `json:"members"`
}

var (
	memoryWriteTools = map[string]bool{"memory_save": true, "memory_update": true, "memory_forget": true, "person_note": true}
	memoryReadTools  = map[string]bool{"memory_search": true, "slack_search": true}
	reProfileName    = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
)

func matchToken(tok string, t *Tool) bool {
	switch {
	case tok == "*":
		return true
	case strings.HasPrefix(tok, "cat:"):
		return t.Category == tok[4:]
	default:
		return t.Name == tok
	}
}

// Allows applies the tool tokens and the memory mode.
func (p *Profile) Allows(t *Tool) bool {
	if p == nil {
		return true
	}
	in := false
	for _, tok := range p.Tools {
		if strings.HasPrefix(tok, "-") {
			if matchToken(tok[1:], t) {
				return false
			}
		} else if matchToken(tok, t) {
			in = true
		}
	}
	if !in {
		return false
	}
	if p.Memory != "full" && memoryWriteTools[t.Name] {
		return false
	}
	if p.Memory == "none" && memoryReadTools[t.Name] {
		return false
	}
	return true
}

// CanApprove reports whether someone with this profile may decide an
// approval requested by requesterID.
func (p *Profile) CanApprove(clickerID, requesterID string) bool {
	switch p.Approve {
	case "any":
		return true
	case "own":
		return requesterID != "" && clickerID == requesterID
	}
	return false
}

func (p *Profile) IsAdmin() bool { return p == nil || p.Approve == "any" }

// ---------- service ----------

type Access struct {
	st     *store.Store
	admins []string
}

func NewAccess(st *store.Store, admins []string) *Access { return &Access{st: st, admins: admins} }

const profileCols = `name, label, emoji, description, tools, memory, reply, approve, max_iterations, extra_prompt, builtin`

func scanProfile(row interface{ Scan(...any) error }) (Profile, error) {
	var p Profile
	var tools string
	err := row.Scan(&p.Name, &p.Label, &p.Emoji, &p.Description, &tools, &p.Memory, &p.Reply, &p.Approve, &p.MaxIterations, &p.ExtraPrompt, &p.Builtin)
	if err == nil {
		json.Unmarshal([]byte(tools), &p.Tools)
	}
	return p, err
}

func (a *Access) Profile(name string) (Profile, bool) {
	p, err := scanProfile(a.st.DB.QueryRow(`SELECT `+profileCols+` FROM access_profiles WHERE name = ?`, name))
	return p, err == nil
}

// Full is the unrestricted profile used for system runs (web, schedules
// created by admins, self-review).
func (a *Access) Full() Profile {
	if p, ok := a.Profile("admin"); ok {
		return p
	}
	return Profile{Name: "admin", Label: "Admin", Emoji: "👑", Tools: []string{"*"}, Memory: "full", Reply: true, Approve: "any"}
}

func (a *Access) Profiles() []Profile {
	rows, err := a.st.DB.Query(`SELECT ` + profileCols + `, (SELECT COUNT(*) FROM access_members m WHERE m.profile = p.name) FROM access_profiles p ORDER BY builtin DESC, created_at`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []Profile{}
	for rows.Next() {
		var p Profile
		var tools string
		if rows.Scan(&p.Name, &p.Label, &p.Emoji, &p.Description, &tools, &p.Memory, &p.Reply, &p.Approve, &p.MaxIterations, &p.ExtraPrompt, &p.Builtin, &p.Members) == nil {
			json.Unmarshal([]byte(tools), &p.Tools)
			out = append(out, p)
		}
	}
	return out
}

func (a *Access) DefaultProfile() string { return a.st.GetSetting("access.default_profile", "guest") }

func (a *Access) SetDefault(name string) error {
	if _, ok := a.Profile(name); !ok {
		return fmt.Errorf("unknown profile: %s", name)
	}
	return a.st.SetSetting("access.default_profile", name)
}

// Resolve returns the profile that applies to a Slack user and whether the
// user is explicitly whitelisted.
func (a *Access) Resolve(slackID string) (Profile, bool) {
	if slices.Contains(a.admins, slackID) {
		return a.Full(), true
	}
	var name string
	if slackID != "" && a.st.DB.QueryRow(`SELECT profile FROM access_members WHERE slack_id = ?`, slackID).Scan(&name) == nil {
		if p, ok := a.Profile(name); ok {
			return p, true
		}
	}
	if p, ok := a.Profile(a.DefaultProfile()); ok {
		return p, false
	}
	// Fail closed: a broken configuration must not grant full access.
	return Profile{Name: "guest", Label: "Guest", Emoji: "👋", Tools: []string{"cat:web", "-http_request"}, Memory: "none", Reply: true, Approve: "none", MaxIterations: 12}, false
}

func (a *Access) SaveProfile(p Profile) error {
	p.Name = strings.ToLower(strings.TrimSpace(p.Name))
	if !reProfileName.MatchString(p.Name) {
		return errors.New("invalid profile name (lowercase letters, digits, dashes)")
	}
	if p.Label == "" {
		p.Label = p.Name
	}
	if p.Memory != "full" && p.Memory != "read" && p.Memory != "none" {
		return errors.New("memory must be full, read or none")
	}
	if p.Approve != "any" && p.Approve != "own" && p.Approve != "none" {
		return errors.New("approve must be any, own or none")
	}
	if p.Tools == nil {
		p.Tools = []string{}
	}
	if p.Name == "admin" {
		// The admin profile can be renamed or decorated, never weakened.
		p.Tools, p.Memory, p.Reply, p.Approve, p.MaxIterations = []string{"*"}, "full", true, "any", 0
	}
	tools, _ := json.Marshal(p.Tools)
	now := store.Now()
	_, err := a.st.DB.Exec(`INSERT INTO access_profiles(`+profileCols+`, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,0,?,?)
		ON CONFLICT(name) DO UPDATE SET label=excluded.label, emoji=excluded.emoji, description=excluded.description, tools=excluded.tools,
		memory=excluded.memory, reply=excluded.reply, approve=excluded.approve, max_iterations=excluded.max_iterations,
		extra_prompt=excluded.extra_prompt, updated_at=excluded.updated_at`,
		p.Name, p.Label, p.Emoji, p.Description, string(tools), p.Memory, p.Reply, p.Approve, p.MaxIterations, p.ExtraPrompt, now, now)
	return err
}

func (a *Access) DeleteProfile(name string) error {
	p, ok := a.Profile(name)
	if !ok {
		return errors.New("profile not found")
	}
	if p.Builtin {
		return errors.New("the admin profile cannot be deleted")
	}
	if a.DefaultProfile() == name {
		return errors.New("this is the default profile: pick another default first")
	}
	tx, err := a.st.DB.Begin()
	if err != nil {
		return err
	}
	tx.Exec(`DELETE FROM access_members WHERE profile = ?`, name)
	tx.Exec(`DELETE FROM access_profiles WHERE name = ?`, name)
	return tx.Commit()
}

type Member struct {
	SlackID  string `json:"slack_id"`
	Profile  string `json:"profile"`
	Note     string `json:"note"`
	AddedBy  string `json:"added_by"`
	Name     string `json:"name"`
	Title    string `json:"title"`
	Created  string `json:"created_at"`
	EnvAdmin bool   `json:"env_admin"`
}

func (a *Access) Members() []Member {
	out := []Member{}
	for _, id := range a.admins {
		m := Member{SlackID: id, Profile: "admin", Note: "set by SLACK_ADMINS", EnvAdmin: true}
		a.st.DB.QueryRow(`SELECT COALESCE(NULLIF(real_name,''), name, ''), COALESCE(title,'') FROM people WHERE slack_id = ?`, id).Scan(&m.Name, &m.Title)
		out = append(out, m)
	}
	rows, err := a.st.DB.Query(`SELECT m.slack_id, m.profile, m.note, COALESCE(m.added_by,''), m.created_at,
		COALESCE(NULLIF(p.real_name,''), p.name, ''), COALESCE(p.title,'')
		FROM access_members m LEFT JOIN people p ON p.slack_id = m.slack_id ORDER BY m.created_at`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var m Member
		if rows.Scan(&m.SlackID, &m.Profile, &m.Note, &m.AddedBy, &m.Created, &m.Name, &m.Title) == nil && !slices.Contains(a.admins, m.SlackID) {
			out = append(out, m)
		}
	}
	return out
}

func NormalizeSlackID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "<@")
	id = strings.TrimSuffix(id, ">")
	if i := strings.Index(id, "|"); i > 0 {
		id = id[:i]
	}
	return strings.ToUpper(id)
}

var reSlackUser = regexp.MustCompile(`^[UW][A-Z0-9]{5,}$`)

func (a *Access) SetMember(slackID, profile, note, by string) error {
	slackID = NormalizeSlackID(slackID)
	if !reSlackUser.MatchString(slackID) {
		return errors.New("invalid Slack ID (e.g. U0123ABCD)")
	}
	if slices.Contains(a.admins, slackID) {
		return errors.New("this person is an admin through SLACK_ADMINS: their access cannot be changed here")
	}
	if _, ok := a.Profile(profile); !ok {
		return fmt.Errorf("unknown profile: %s", profile)
	}
	_, err := a.st.DB.Exec(`INSERT INTO access_members(slack_id, profile, note, added_by, created_at) VALUES(?,?,?,?,?)
		ON CONFLICT(slack_id) DO UPDATE SET profile = excluded.profile, note = CASE WHEN excluded.note != '' THEN excluded.note ELSE access_members.note END`,
		slackID, profile, note, by, store.Now())
	return err
}

func (a *Access) RemoveMember(slackID string) error {
	res, err := a.st.DB.Exec(`DELETE FROM access_members WHERE slack_id = ?`, NormalizeSlackID(slackID))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// AllowedTools lists the tool names a profile resolves to.
func (a *Access) AllowedTools(reg *Registry, p *Profile) []string {
	out := []string{}
	for _, t := range reg.All() {
		if p.Allows(t) {
			out = append(out, t.Name)
		}
	}
	return out
}
