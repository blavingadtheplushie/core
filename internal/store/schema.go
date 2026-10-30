package store

// migrations are applied in order and tracked with PRAGMA user_version.
// Never edit a shipped migration: append a new one.
var migrations = []string{
	`
CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE users (
  id TEXT PRIMARY KEY,
  sub TEXT UNIQUE NOT NULL,
  email TEXT,
  name TEXT,
  avatar_url TEXT,
  created_at TEXT NOT NULL,
  last_login_at TEXT
);

CREATE TABLE sessions (
  token_hash TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE oidc_states (
  state TEXT PRIMARY KEY,
  verifier TEXT NOT NULL,
  nonce TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE runs (
  id TEXT PRIMARY KEY,
  parent_id TEXT,
  source TEXT NOT NULL,
  channel TEXT,
  channel_name TEXT,
  thread_ts TEXT,
  message_ts TEXT,
  reply_ts TEXT,
  user_id TEXT,
  user_name TEXT,
  input TEXT NOT NULL,
  output TEXT,
  status TEXT NOT NULL,
  error TEXT,
  model TEXT,
  iterations INTEGER NOT NULL DEFAULT 0,
  tool_calls INTEGER NOT NULL DEFAULT 0,
  prompt_tokens INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  feedback INTEGER NOT NULL DEFAULT 0,
  started_at TEXT NOT NULL,
  finished_at TEXT
);
CREATE INDEX runs_started ON runs(started_at DESC);
CREATE INDEX runs_thread ON runs(channel, thread_ts);
CREATE INDEX runs_reply ON runs(channel, reply_ts);

CREATE TABLE run_steps (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  name TEXT,
  input TEXT,
  output TEXT,
  status TEXT NOT NULL,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  tokens INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE INDEX run_steps_run ON run_steps(run_id, id);

CREATE TABLE approvals (
  id TEXT PRIMARY KEY,
  run_id TEXT,
  tool TEXT NOT NULL,
  args TEXT,
  summary TEXT,
  status TEXT NOT NULL,
  requested_by TEXT,
  requester_id TEXT,
  decided_by TEXT,
  slack_channel TEXT,
  slack_ts TEXT,
  created_at TEXT NOT NULL,
  decided_at TEXT
);
CREATE INDEX approvals_status ON approvals(status, created_at DESC);

CREATE TABLE memories (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL,
  subject TEXT,
  content TEXT NOT NULL,
  tags TEXT,
  importance INTEGER NOT NULL DEFAULT 3,
  source TEXT,
  embedding BLOB,
  use_count INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_used_at TEXT
);
CREATE VIRTUAL TABLE memories_fts USING fts5(subject, content, tags, content='memories', content_rowid='id', tokenize='unicode61 remove_diacritics 2');
CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
  INSERT INTO memories_fts(rowid, subject, content, tags) VALUES (new.id, new.subject, new.content, new.tags);
END;
CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, subject, content, tags) VALUES('delete', old.id, old.subject, old.content, old.tags);
END;
CREATE TRIGGER memories_au AFTER UPDATE OF subject, content, tags ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, subject, content, tags) VALUES('delete', old.id, old.subject, old.content, old.tags);
  INSERT INTO memories_fts(rowid, subject, content, tags) VALUES (new.id, new.subject, new.content, new.tags);
END;

CREATE TABLE people (
  slack_id TEXT PRIMARY KEY,
  name TEXT,
  real_name TEXT,
  title TEXT,
  email TEXT,
  tz TEXT,
  notes TEXT NOT NULL DEFAULT '',
  message_count INTEGER NOT NULL DEFAULT 0,
  last_seen_at TEXT,
  updated_at TEXT NOT NULL
);

CREATE TABLE channels (
  id TEXT PRIMARY KEY,
  name TEXT,
  kind TEXT,
  topic TEXT,
  digest TEXT NOT NULL DEFAULT '',
  digest_until TEXT,
  message_count INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL
);

CREATE TABLE slack_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel TEXT NOT NULL,
  ts TEXT NOT NULL,
  thread_ts TEXT,
  user_id TEXT,
  user_name TEXT,
  text TEXT NOT NULL,
  is_bot INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  UNIQUE(channel, ts)
);
CREATE INDEX slack_messages_channel ON slack_messages(channel, ts DESC);
CREATE VIRTUAL TABLE slack_messages_fts USING fts5(text, user_name, content='slack_messages', content_rowid='id', tokenize='unicode61 remove_diacritics 2');
CREATE TRIGGER slack_messages_ai AFTER INSERT ON slack_messages BEGIN
  INSERT INTO slack_messages_fts(rowid, text, user_name) VALUES (new.id, new.text, new.user_name);
END;
CREATE TRIGGER slack_messages_ad AFTER DELETE ON slack_messages BEGIN
  INSERT INTO slack_messages_fts(slack_messages_fts, rowid, text, user_name) VALUES('delete', old.id, old.text, old.user_name);
END;
CREATE TRIGGER slack_messages_au AFTER UPDATE OF text ON slack_messages BEGIN
  INSERT INTO slack_messages_fts(slack_messages_fts, rowid, text, user_name) VALUES('delete', old.id, old.text, old.user_name);
  INSERT INTO slack_messages_fts(rowid, text, user_name) VALUES (new.id, new.text, new.user_name);
END;

CREATE TABLE threads (
  channel TEXT NOT NULL,
  thread_ts TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(channel, thread_ts)
);

CREATE TABLE emails (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  direction TEXT NOT NULL,
  uid INTEGER,
  message_id TEXT,
  in_reply_to TEXT,
  from_addr TEXT,
  to_addr TEXT,
  cc TEXT,
  subject TEXT,
  body TEXT,
  run_id TEXT,
  created_at TEXT NOT NULL
);
CREATE INDEX emails_created ON emails(created_at DESC);

CREATE TABLE skills (
  id TEXT PRIMARY KEY,
  name TEXT UNIQUE NOT NULL,
  description TEXT NOT NULL,
  content TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  author TEXT,
  uses INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE custom_tools (
  id TEXT PRIMARY KEY,
  name TEXT UNIQUE NOT NULL,
  description TEXT NOT NULL,
  parameters TEXT NOT NULL,
  runtime TEXT NOT NULL,
  code TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  author TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE persona_versions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  content TEXT NOT NULL,
  reason TEXT,
  author TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE schedules (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  cron TEXT,
  run_at TEXT,
  prompt TEXT NOT NULL,
  channel TEXT,
  thread_ts TEXT,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_by TEXT,
  last_run_at TEXT,
  last_status TEXT,
  next_run_at TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE deployments (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  name TEXT NOT NULL,
  url TEXT,
  status TEXT NOT NULL,
  details TEXT,
  run_id TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE evolutions (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  summary TEXT NOT NULL,
  details TEXT,
  commit_sha TEXT,
  status TEXT NOT NULL,
  run_id TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  actor TEXT,
  action TEXT NOT NULL,
  target TEXT,
  details TEXT,
  created_at TEXT NOT NULL
);
CREATE INDEX audit_created ON audit(created_at DESC);
`,
	// 2 — access profiles + whitelist.
	`
CREATE TABLE access_profiles (
  name TEXT PRIMARY KEY,
  label TEXT NOT NULL,
  emoji TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  tools TEXT NOT NULL,
  memory TEXT NOT NULL DEFAULT 'full',
  reply INTEGER NOT NULL DEFAULT 1,
  approve TEXT NOT NULL DEFAULT 'none',
  max_iterations INTEGER NOT NULL DEFAULT 0,
  extra_prompt TEXT NOT NULL DEFAULT '',
  builtin INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE access_members (
  slack_id TEXT PRIMARY KEY,
  profile TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  added_by TEXT,
  created_at TEXT NOT NULL
);

ALTER TABLE schedules ADD COLUMN profile TEXT;

INSERT INTO access_profiles(name, label, emoji, description, tools, memory, reply, approve, max_iterations, extra_prompt, builtin, created_at, updated_at) VALUES
 ('admin', 'Admin', '👑', 'Accès total : tous les tools, mémoire complète, peut tout valider et gérer les accès.', '["*"]', 'full', 1, 'any', 0, '', 1, strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('membre', 'Membre', '🙂', 'L''équipe : tout sauf la gestion des accès et l''auto-modification. Valide ses propres demandes.', '["*","-cat:access","-cat:self"]', 'full', 1, 'own', 0, '', 0, strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('invite', 'Invité', '👋', 'Hors liste blanche : recherche web seulement, aucune mémoire, rien de sensible.', '["cat:web","-http_request","slack_react"]', 'none', 1, 'none', 12, 'Tu parles à une personne qui n''est pas dans ta liste blanche. Sois aimable et utile pour des questions générales, mais ne partage AUCUNE information interne : projets, personnes, conversations, fichiers, infrastructure. Si elle a besoin de plus, dis-lui de demander à un admin de lui donner accès.', 0, strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 ('bloque', 'Bloqué', '🚫', 'Ignoré : l''agent ne lui répond pas (ses messages restent lus).', '[]', 'none', 0, 'none', 0, '', 0, strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now'));

INSERT INTO settings(key, value, updated_at) VALUES('access.default_profile', 'invite', strftime('%Y-%m-%dT%H:%M:%SZ','now'))
  ON CONFLICT(key) DO NOTHING;
`,
	// 3 — the vault is admin-only by default (untouched member profiles only).
	`
UPDATE access_profiles SET tools = '["*","-cat:access","-cat:self","-cat:vault"]',
  description = 'L''équipe : tout sauf la gestion des accès, le coffre et l''auto-modification. Valide ses propres demandes.'
  WHERE name = 'membre' AND tools = '["*","-cat:access","-cat:self"]';
`,
	// 4 — English: profile identifiers and the built-in texts (left alone if
	// an admin already customized them).
	`
UPDATE access_profiles SET name = 'member' WHERE name = 'membre';
UPDATE access_profiles SET name = 'guest' WHERE name = 'invite';
UPDATE access_profiles SET name = 'blocked' WHERE name = 'bloque';
UPDATE access_members SET profile = CASE profile WHEN 'membre' THEN 'member' WHEN 'invite' THEN 'guest' WHEN 'bloque' THEN 'blocked' ELSE profile END;
UPDATE schedules SET profile = CASE profile WHEN 'membre' THEN 'member' WHEN 'invite' THEN 'guest' WHEN 'bloque' THEN 'blocked' ELSE profile END WHERE profile IS NOT NULL;
UPDATE settings SET value = CASE value WHEN 'membre' THEN 'member' WHEN 'invite' THEN 'guest' WHEN 'bloque' THEN 'blocked' ELSE value END WHERE key = 'access.default_profile';

UPDATE access_profiles SET description = 'Full access: every tool, full memory, can approve anything and manage access.'
  WHERE name = 'admin' AND description LIKE 'Accès total%';
UPDATE access_profiles SET label = 'Member', description = 'The team: everything except access management, the vault and self-modification. Can approve their own requests.'
  WHERE name = 'member' AND label = 'Membre';
UPDATE access_profiles SET label = 'Guest', description = 'Outside the whitelist: web search only, no memory, nothing sensitive.',
  extra_prompt = 'You are talking to someone who is not on your whitelist. Be friendly and helpful with general questions, but share NO internal information: projects, people, conversations, files, infrastructure. If they need more, tell them to ask an admin for access.'
  WHERE name = 'guest' AND label = 'Invité';
UPDATE access_profiles SET tools = '["cat:web","-http_request"]'
  WHERE name = 'guest' AND tools = '["cat:web","-http_request","slack_react"]';
UPDATE access_profiles SET label = 'Blocked', description = 'Ignored: the agent does not reply (their messages are still read).'
  WHERE name = 'blocked' AND label = 'Bloqué';
`,
}
