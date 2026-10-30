/* BLAVINGAD — admin workspace (same design system as Fold) */
const app = document.getElementById('app');
const modalRoot = document.getElementById('modal-root');
const toastRoot = document.getElementById('toast-root');

const state = {
  status: null,
  user: null,
  overview: null,
  route: '/',
  routeSeq: 0,
  pending: 0,
  es: null,
  chat: { thread: '', items: [], threads: [] },
  live: {},
  runsPage: 1,
  runsSearch: '',
  runsSource: '',
  runsStatus: '',
  memTab: 'memories',
  memKind: '',
  memSearch: '',
  memSemantic: false,
  memPage: 1,
  mailDir: '',
  mailPage: 1,
  auditPage: 1,
  auditSearch: '',
  searchTimer: null,
  refreshTimer: null,
  confirmHandler: null,
  cache: {},
};

const ICONS = {
  dashboard: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
  chat: '<path d="M21 12a8 8 0 0 1-11.6 7.1L4 20l1-4.4A8 8 0 1 1 21 12Z"/><path d="M8.5 12h.01M12 12h.01M15.5 12h.01"/>',
  runs: '<path d="M13 2 4 14h7l-1 8 9-12h-7Z"/>',
  hand: '<path d="M18 11V6a2 2 0 0 0-4 0v5M14 10V4a2 2 0 0 0-4 0v6M10 10.5V6a2 2 0 0 0-4 0v8"/><path d="M18 8a2 2 0 1 1 4 0v6a8 8 0 0 1-8 8h-2c-2.8 0-4.5-.86-5.99-2.34l-3.6-3.6a2 2 0 0 1 2.83-2.82L7 15"/>',
  brain: '<path d="M12 5a3 3 0 0 0-5.6-1.5A3 3 0 0 0 3.6 8a3 3 0 0 0 .4 5.6A3.5 3.5 0 0 0 8 19a3 3 0 0 0 4 1"/><path d="M12 5a3 3 0 0 1 5.6-1.5A3 3 0 0 1 20.4 8a3 3 0 0 1-.4 5.6A3.5 3.5 0 0 1 16 19a3 3 0 0 1-4 1"/><path d="M12 5v15"/>',
  tool: '<path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76Z"/>',
  book: '<path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2Z"/><path d="M9 7h7M9 11h5"/>',
  dna: '<path d="M7 3c0 5 10 6 10 11s-10 6-10 7"/><path d="M17 3c0 5-10 6-10 11s10 6 10 7"/><path d="M8 7h8M7.5 17h9M10 12h4"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  rocket: '<path d="M4.5 16.5c-1.5 1.26-2 5-2 5s3.74-.5 5-2c.71-.84.7-2.13-.09-2.91a2.18 2.18 0 0 0-2.91-.09Z"/><path d="m12 15-3-3a22 22 0 0 1 2-3.95A12.88 12.88 0 0 1 22 2c0 2.72-.78 7.5-6 11a22.35 22.35 0 0 1-4 2Z"/><path d="M9 12H4s.55-3.03 2-4c1.62-1.08 5 0 5 0M12 15v5s3.03-.55 4-2c1.08-1.62 0-5 0-5"/>',
  mail: '<rect x="3" y="5" width="18" height="14" rx="2"/><path d="m3 7 9 6 9-6"/>',
  audit: '<path d="M9 4h6"/><rect x="5" y="3" width="14" height="18" rx="2"/><path d="M9 9h6M9 13h6M9 17h4"/>',
  settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.7 1.7 0 0 0-1.88-.34 1.7 1.7 0 0 0-1 1.55V21a2 2 0 1 1-4 0v-.09a1.7 1.7 0 0 0-1.1-1.55 1.7 1.7 0 0 0-1.88.34l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-1.55-1H3a2 2 0 1 1 0-4h.09a1.7 1.7 0 0 0 1.55-1.1 1.7 1.7 0 0 0-.34-1.88l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.7 1.7 0 0 0 9 4.6 1.7 1.7 0 0 0 10 3.05V3a2 2 0 1 1 4 0v.09a1.7 1.7 0 0 0 1 1.55 1.7 1.7 0 0 0 1.88-.34l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.7 1.7 0 0 0-.34 1.88V10a1.7 1.7 0 0 0 1.55 1H21a2 2 0 1 1 0 4h-.09a1.7 1.7 0 0 0-1.51 1Z"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>',
  copy: '<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
  edit: '<path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z"/>',
  trash: '<path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6M10 11v5M14 11v5"/>',
  external: '<path d="M15 3h6v6M10 14 21 3M21 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5"/>',
  menu: '<path d="M4 7h16M4 12h16M4 17h16"/>',
  x: '<path d="m6 6 12 12M18 6 6 18"/>',
  logout: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/>',
  arrowRight: '<path d="M5 12h14M13 5l7 7-7 7"/>',
  arrowLeft: '<path d="M19 12H5M11 19l-7-7 7-7"/>',
  check: '<path d="m4 12 5 5L20 6"/>',
  shield: '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z"/>',
  spark: '<path d="m12 3 1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8Z"/>',
  refresh: '<path d="M20 11a8 8 0 1 0-2.3 6.3M20 4v7h-7"/>',
  send: '<path d="m22 2-7 20-4-9-9-4Z"/><path d="M22 2 11 13"/>',
  stop: '<rect x="6" y="6" width="12" height="12" rx="2"/>',
  play: '<path d="m7 4 13 8-13 8Z"/>',
  user: '<circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/>',
  hash: '<path d="M4 9h16M4 15h16M10 3 8 21M16 3l-2 18"/>',
  code: '<path d="m8 17-5-5 5-5M16 7l5 5-5 5M14 4l-4 16"/>',
  undo: '<path d="M9 14 4 9l5-5"/><path d="M4 9h11a5 5 0 0 1 0 10h-4"/>',
  eye: '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12Z"/><circle cx="12" cy="12" r="3"/>',
  chart: '<path d="M3 3v18h18"/><path d="m7 15 4-5 4 3 4-6"/>',
  history: '<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5M12 7v5l4 2"/>',
};

const LOGO = '<svg viewBox="0 0 32 32" fill="none" aria-hidden="true"><rect width="32" height="32" rx="9" fill="currentColor"/><circle cx="11.5" cy="14" r="2.5" fill="var(--acc-ink)"/><circle cx="20.5" cy="14" r="2.5" fill="var(--acc-ink)"/><path d="M11 20.2c1.4 1.6 3.1 2.4 5 2.4s3.6-.8 5-2.4" fill="none" stroke="var(--acc-ink)" stroke-width="2.4" stroke-linecap="round"/><path d="M16 4.5v3" stroke="var(--acc-ink)" stroke-width="2.2" stroke-linecap="round"/></svg>';
const FACE = '<svg viewBox="0 0 32 32" fill="none" aria-hidden="true"><circle cx="11" cy="14" r="2.8" fill="currentColor"/><circle cx="21" cy="14" r="2.8" fill="currentColor"/><path d="M10 20.5c1.6 1.8 3.6 2.7 6 2.7s4.4-.9 6-2.7" stroke="currentColor" stroke-width="2.6" stroke-linecap="round"/></svg>';

function icon(name, cls = '') {
  const body = ICONS[name] || ICONS.spark;
  return `<svg class="${cls}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${body}</svg>`;
}

const esc = (value) => String(value ?? '')
  .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  .replace(/"/g, '&quot;').replace(/'/g, '&#39;');

const fmt = new Intl.NumberFormat('en-US');
const fmtNum = (value) => fmt.format(Number(value) || 0);
const fmtCompact = (value) => new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 }).format(Number(value) || 0);
const toDate = (value) => {
  if (!value) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
};
const fmtDate = (value) => {
  const date = toDate(value);
  if (!date) return '—';
  return date.toLocaleDateString('en-US', { day: 'numeric', month: 'short', ...(date.getFullYear() !== new Date().getFullYear() ? { year: 'numeric' } : {}) });
};
const fmtDateTime = (value) => {
  const date = toDate(value);
  if (!date) return '—';
  return date.toLocaleString('en-US', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });
};
const fmtTime = (value) => {
  const date = toDate(value);
  return date ? date.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', second: '2-digit' }) : '';
};
const relative = (value) => {
  const date = toDate(value);
  if (!date) return 'never';
  const diff = Date.now() - date.getTime();
  if (diff < 0) {
    const ahead = -diff;
    if (ahead < 3_600_000) return `in ${Math.max(1, Math.round(ahead / 60_000))}m`;
    if (ahead < 86_400_000) return `in ${Math.round(ahead / 3_600_000)}h`;
    return `on ${fmtDateTime(value)}`;
  }
  if (diff < 60_000) return 'just now';
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`;
  if (diff < 30 * 86_400_000) return `${Math.floor(diff / 86_400_000)}d ago`;
  return fmtDate(value);
};
const duration = (start, end) => {
  const a = toDate(start); const b = end ? toDate(end) : new Date();
  if (!a || !b) return '—';
  const ms = b - a;
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  return `${Math.floor(ms / 60_000)} min ${Math.round((ms % 60_000) / 1000)} s`;
};
const fmtMs = (ms) => (ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`);
const initials = (name, email) => {
  const source = (name || email || '?').trim();
  return source.split(/\s+/).map((part) => part[0]).join('').slice(0, 2).toUpperCase();
};
const truncate = (value, n) => { const s = String(value ?? ''); return s.length > n ? `${s.slice(0, n)}…` : s; };
const prettyJSON = (value) => { try { return JSON.stringify(JSON.parse(value), null, 2); } catch { return String(value ?? ''); } };

/* Markdown → safe HTML. Everything is escaped first; only a small set of
   constructs is turned back into tags, links are http(s) only. */
function md(src) {
  const blocks = [];
  let text = String(src ?? '').replace(/```[\w-]*\n?([\s\S]*?)```/g, (_, code) => {
    blocks.push(`<pre><code>${esc(code.replace(/\n$/, ''))}</code></pre>`);
    return `\n\u0000${blocks.length - 1}\u0000\n`;
  });
  text = esc(text);
  const inline = [];
  text = text.replace(/`([^`\n]+)`/g, (_, code) => { inline.push(`<code>${code}</code>`); return `\u0001${inline.length - 1}\u0001`; });
  text = text
    .replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g, '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>')
    .replace(/&lt;(https?:\/\/[^|\s]+?)\|([^\n]+?)&gt;/g, '<a href="$1" target="_blank" rel="noopener noreferrer">$2</a>')
    .replace(/&lt;(https?:\/\/[^\s]+?)&gt;/g, '<a href="$1" target="_blank" rel="noopener noreferrer">$1</a>')
    .replace(/(^|[\s(])(https?:\/\/[^\s<)]+)/g, '$1<a href="$2" target="_blank" rel="noopener noreferrer">$2</a>')
    .replace(/&lt;@([UW][A-Z0-9]+)&gt;/g, '<span class="badge acc no-dot">@$1</span>')
    .replace(/&lt;#([A-Z0-9]+)\|?([^&]*)&gt;/g, (_, id, name) => `<span class="badge no-dot">#${name || id}</span>`)
    .replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>')
    .replace(/(^|[^\w*])\*([^*\s][^*\n]*?)\*(?!\w)/g, '$1<strong>$2</strong>')
    .replace(/(^|\s)_([^_\n]+)_(?=\s|$|[.,!?;:])/g, '$1<em>$2</em>')
    .replace(/~~?([^~\n]+)~~?/g, '<del>$1</del>');
  let html = '';
  let list = null;
  let para = [];
  const flushPara = () => { if (para.length) { html += `<p>${para.join('<br>')}</p>`; para = []; } };
  const flushList = () => { if (list) { html += `</${list}>`; list = null; } };
  for (const line of text.split('\n')) {
    let m;
    if (/^\u0000\d+\u0000$/.test(line.trim())) { flushPara(); flushList(); html += line.trim(); continue; }
    if ((m = line.match(/^#{1,6}\s+(.*)$/))) { flushPara(); flushList(); html += `<h4>${m[1]}</h4>`; continue; }
    if ((m = line.match(/^\s*(?:[-*•]|\d+[.)])\s+(.*)$/))) {
      flushPara();
      const type = /^\s*\d/.test(line) ? 'ol' : 'ul';
      if (list !== type) { flushList(); html += `<${type}>`; list = type; }
      html += `<li>${m[1]}</li>`;
      continue;
    }
    if ((m = line.match(/^&gt;\s?(.*)$/))) { flushPara(); flushList(); html += `<blockquote>${m[1]}</blockquote>`; continue; }
    if (!line.trim()) { flushPara(); flushList(); continue; }
    flushList();
    para.push(line);
  }
  flushPara(); flushList();
  return html.replace(/\u0001(\d+)\u0001/g, (_, i) => inline[i]).replace(/\u0000(\d+)\u0000/g, (_, i) => blocks[i]);
}

class ApiError extends Error {
  constructor(message, status) { super(message); this.status = status; }
}

async function api(path, options = {}) {
  const headers = { ...(options.headers || {}) };
  if (options.body && typeof options.body !== 'string') {
    headers['Content-Type'] = 'application/json';
    options.body = JSON.stringify(options.body);
  }
  const response = await fetch(`/api${path}`, { credentials: 'same-origin', ...options, headers });
  const type = response.headers.get('content-type') || '';
  const data = type.includes('application/json') ? await response.json() : await response.text();
  if (!response.ok) {
    if (response.status === 401 && state.user) {
      state.user = null;
      state.status.authenticated = false;
      renderLogin('Your session expired. Please sign in again.');
    }
    throw new ApiError(data?.error || data || `Request failed (${response.status})`, response.status);
  }
  return data;
}

function toast(message, type = 'ok') {
  const node = document.createElement('div');
  node.className = `toast ${type}`;
  node.textContent = message;
  toastRoot.appendChild(node);
  setTimeout(() => { node.style.opacity = '0'; node.style.transform = 'translateY(4px)'; node.style.transition = 'opacity .2s, transform .2s'; setTimeout(() => node.remove(), 220); }, 3600);
}

async function copyText(text, message = 'Copied to clipboard') {
  try {
    await navigator.clipboard.writeText(text);
    toast(message);
  } catch {
    toast('Could not copy — select the text manually.', 'err');
  }
}

function showModal(html, size = '') {
  modalRoot.innerHTML = `<div class="modal-backdrop" data-action="close-modal"><section class="modal ${size}" role="dialog" aria-modal="true">${html}</section></div>`;
  document.body.style.overflow = 'hidden';
  const focusTarget = modalRoot.querySelector('[autofocus], input, textarea, button');
  if (focusTarget) setTimeout(() => focusTarget.focus({ preventScroll: true }), 40);
}

function closeModal() {
  modalRoot.innerHTML = '';
  document.body.style.overflow = '';
  state.confirmHandler = null;
}

function modalHead(title, sub = '') {
  return `<div class="modal-head"><div><h2>${esc(title)}</h2>${sub ? `<p>${esc(sub)}</p>` : ''}</div><button class="icon-btn" data-action="close-modal" aria-label="Close" title="Close">${icon('x')}</button></div>`;
}

function confirmDialog({ title, message, confirm = 'Confirm', danger = true, onConfirm }) {
  state.confirmHandler = onConfirm;
  showModal(`${modalHead(title, message)}
    <div class="modal-foot"><button class="btn" data-action="close-modal">Cancel</button><button class="btn ${danger ? 'danger' : 'primary'}" data-action="confirm-run">${esc(confirm)}</button></div>`);
}

function errorState(message) {
  return `<div class="card empty"><div class="empty-art error-art">${icon('shield')}</div><h3>Something went wrong</h3><p>${esc(message)}</p><button class="btn" data-action="reload">Try again</button></div>`;
}

function skeletonPage() {
  return `<div class="page-head"><div><div class="skeleton skeleton-line" style="width:110px;margin-bottom:12px"></div><div class="skeleton" style="width:280px;height:36px"></div></div></div>
  <div class="stat-grid">${Array.from({ length: 4 }).map(() => '<div class="stat"><div class="skeleton skeleton-line" style="width:70px;margin-bottom:18px"></div><div class="skeleton" style="width:100px;height:32px"></div></div>').join('')}</div>
  <div class="grid-2"><div class="card"><div class="card-body" style="padding:20px"><div class="skeleton" style="height:250px"></div></div></div><div class="card"><div class="card-body" style="padding:20px"><div class="skeleton" style="height:250px"></div></div></div></div>`;
}

function pageHead(title, subtitle, actions = '', eyebrow = '') {
  return `<div class="page-head"><div>${eyebrow ? `<div class="eyebrow">${esc(eyebrow)}</div>` : ''}<h1>${title}</h1>${subtitle ? `<p class="page-sub">${subtitle}</p>` : ''}</div>${actions ? `<div class="head-actions">${actions}</div>` : ''}</div>`;
}

function badge(text, className = '', dot = true) {
  return `<span class="badge ${className} ${dot ? '' : 'no-dot'}">${esc(text)}</span>`;
}

function emptyState(title, text, action = '') {
  return `<div class="empty"><div class="empty-art"><svg viewBox="0 0 120 90" fill="none"><rect x="16" y="18" width="74" height="54" rx="11" fill="var(--paper)" stroke="var(--line)"/><path d="M26 34h38M26 44h26M26 54h17" stroke="var(--line)" stroke-width="2.5" stroke-linecap="round"/><circle cx="86" cy="58" r="19" fill="var(--acc-soft)" stroke="var(--acc)" stroke-width="1.2"/><circle cx="80" cy="56" r="2.2" fill="var(--acc)"/><circle cx="92" cy="56" r="2.2" fill="var(--acc)"/><path d="M79.5 62.5c1.8 2 3.9 3 6.5 3s4.7-1 6.5-3" stroke="var(--acc)" stroke-width="2" stroke-linecap="round"/></svg></div><h3>${esc(title)}</h3><p>${esc(text)}</p>${action}</div>`;
}

const SOURCES = {
  slack: ['💬', 'Slack'], web: ['🖥️', 'Web'], email: ['📬', 'Email'], schedule: ['⏰', 'Task'], self: ['🧬', 'Self-review'], subagent: ['🧑‍🚀', 'Sub-agent'],
};
function sourceChip(source) {
  const [emoji, label] = SOURCES[source] || ['•', source || '—'];
  return `<span class="src-chip"><span class="emoji">${emoji}</span>${esc(label)}</span>`;
}

function runStatus(status) {
  const map = {
    running: ['Running', 'acc'], done: ['Done', 'ok'], error: ['Error', 'bad'], cancelled: ['Stopped', 'warn'], interrupted: ['Interrupted', 'warn'],
  };
  const [label, cls] = map[status] || [status || '—', ''];
  return badge(label, cls);
}

function approvalStatus(status) {
  const map = { pending: ['Pending', 'warn'], approved: ['Approved', 'ok'], denied: ['Denied', 'bad'], expired: ['Expired', ''] };
  const [label, cls] = map[status] || [status, ''];
  return badge(label, cls);
}

/* ---------- Charts ---------- */
function lineChart(series, key = 'runs', width = 760, height = 230) {
  const pad = { top: 16, right: 12, bottom: 26, left: 34 };
  const values = series.map((item) => Number(item[key]) || 0);
  const max = Math.max(1, ...values);
  const n = values.length;
  const innerW = width - pad.left - pad.right;
  const innerH = height - pad.top - pad.bottom;
  const x = (index) => pad.left + (n <= 1 ? innerW / 2 : (index / (n - 1)) * innerW);
  const y = (value) => pad.top + innerH - (value / max) * innerH;
  const points = values.map((value, index) => [x(index), y(value)]);
  const line = points.map(([px, py], index) => `${index === 0 ? 'M' : 'L'}${px.toFixed(1)},${py.toFixed(1)}`).join(' ');
  const area = `${line} L${x(n - 1).toFixed(1)},${(pad.top + innerH).toFixed(1)} L${x(0).toFixed(1)},${(pad.top + innerH).toFixed(1)} Z`;
  const grid = [0, 0.5, 1].map((ratio) => {
    const gy = pad.top + innerH - ratio * innerH;
    return `<line class="grid-line" x1="${pad.left}" y1="${gy}" x2="${width - pad.right}" y2="${gy}"/><text x="${pad.left - 8}" y="${gy + 3}" text-anchor="end">${Math.round(max * ratio)}</text>`;
  }).join('');
  const labelIndexes = [...new Set(width < 430 ? [0, Math.floor((n - 1) / 2), n - 1] : [0, Math.floor((n - 1) / 3), Math.floor((2 * (n - 1)) / 3), n - 1])];
  const xLabels = labelIndexes.filter((index) => index >= 0).map((index) => {
    const day = series[index]?.day || '';
    const label = day ? new Date(`${day}T00:00:00Z`).toLocaleDateString('en-US', { day: 'numeric', month: 'short', timeZone: 'UTC' }) : '';
    return `<text x="${x(index)}" y="${height - 6}" text-anchor="${index === 0 ? 'start' : index === n - 1 ? 'end' : 'middle'}">${esc(label)}</text>`;
  }).join('');
  const dots = points.map(([px, py], index) => `<circle class="dot" cx="${px}" cy="${py}" r="3.5"><title>${esc(series[index]?.day || '')} · ${values[index]}</title></circle>`).join('');
  return `<svg class="chart" viewBox="0 0 ${width} ${height}" role="img" aria-label="Activity per day">
    <defs><linearGradient id="chart-fill" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="var(--acc)" stop-opacity=".26"/><stop offset="1" stop-color="var(--acc)" stop-opacity="0"/></linearGradient></defs>
    ${grid}<path class="area" d="${area}"/><path class="line" d="${line}"/>${dots}${xLabels}
  </svg>`;
}

function sparkline(values) {
  const vals = values.map((v) => Number(v) || 0);
  const max = Math.max(1, ...vals);
  const width = 72; const height = 28;
  const points = vals.map((value, index) => `${(index / Math.max(1, vals.length - 1)) * width},${height - (value / max) * height}`).join(' ');
  return `<svg viewBox="0 0 ${width} ${height}" fill="none" aria-hidden="true"><polyline points="${points}" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>`;
}

let chartFrame = 0;
function fitCharts() {
  cancelAnimationFrame(chartFrame);
  chartFrame = requestAnimationFrame(() => {
    document.querySelectorAll('.chart-wrap[data-chart]').forEach((wrap) => {
      const width = Math.round(wrap.clientWidth - 16);
      if (width < 240) return;
      try { wrap.innerHTML = lineChart(JSON.parse(wrap.dataset.chart || '[]'), wrap.dataset.key || 'runs', width); } catch { /* keep */ }
    });
  });
}

function breakdownCard(title, items, opts = {}) {
  const rows = (items || []).slice(0, opts.limit || 7);
  const max = Math.max(1, ...rows.map((item) => Number(item.value) || 0));
  const body = rows.length ? `<div class="bar-list">${rows.map((item, index) => {
    const value = Number(item.value) || 0;
    const pct = Math.round((value / max) * 100);
    return `<div class="bar-row"><div class="bar-meta"><span>${item.html || esc(item.label)}</span><span>${esc(item.meta || fmtNum(value))}</span></div><div class="bar-track"><div class="bar-fill ${item.red ? 'red' : index === 0 ? 'green' : index === 1 ? 'alt' : ''}" style="width:${Math.max(2, pct)}%"></div></div></div>`;
  }).join('')}</div>` : `<div class="hint" style="padding:12px 0">${esc(opts.empty || 'No data yet.')}</div>`;
  return `<section class="card"><div class="card-head"><div><h3>${esc(title)}</h3>${opts.sub ? `<p>${esc(opts.sub)}</p>` : ''}</div>${opts.action || ''}</div><div class="card-body">${body}</div></section>`;
}

/* ---------- Authentication ---------- */
function renderLogin(error = '') {
  const status = state.status || {};
  const name = status.agentName || 'BLAVINGAD';
  const urlError = new URLSearchParams(location.search).get('auth_error') || '';
  const message = error || urlError;
  app.innerHTML = `
  <div class="auth">
    <section class="auth-show">
      <div class="auth-grid"></div>
      <a class="brand" href="/"><span class="logo-mark">${LOGO}</span><span>${esc(name)}</span></a>
      <div class="auth-copy">
        <div class="eyebrow">Autonomous AI agent for Slack</div>
        <h1>It reads, it searches,<br><em>it ships.</em></h1>
        <p>The teammate that never sleeps: web search, code, deployments, email, and a memory that gets better with every conversation.</p>
        <div class="auth-bento">
          <div class="bento tall">
            <div class="bento-label">#product</div>
            <div class="bento-chat">
              <div class="bento-msg">@${esc(name)} can you put the landing page live?</div>
              <div class="bento-step"><span class="live-dot"></span>🏗️ Building the site</div>
              <div class="bento-step"><span class="live-dot"></span>🚀 Cloudflare Pages</div>
              <div class="bento-step"><span class="live-dot run"></span>🌐 Checking the URL</div>
              <div class="bento-msg bot">It’s live ✨ landing.pages.dev</div>
            </div>
          </div>
          <div class="bento">
            <div class="bento-label">Memory</div>
            <div class="bento-num">1,284</div>
            <div class="bento-sub">memories, merged and recalled</div>
          </div>
          <div class="bento">
            <div class="bento-label">Evolution</div>
            <div class="row" style="margin-top:7px">${badge('🧬 v42 stable', 'acc')}</div>
            <div class="bento-sub" style="margin-top:7px">improved itself overnight</div>
          </div>
        </div>
      </div>
    </section>
    <section class="auth-panel">
      <div class="auth-card">
        <div class="eyebrow">Dashboard</div>
        <h2>Welcome back 👋</h2>
        <p>Sign in to run ${esc(name)}: conversations, memory, tools and approvals.</p>
        ${message ? `<div class="auth-error">${esc(message)}</div>` : ''}
        ${status.oidcEnabled ? `<a class="btn primary wide" href="/api/auth/login">${icon('shield', 'oidc-mark')} Continue with SSO</a>` : ''}
        ${status.passwordEnabled && status.oidcEnabled ? '<div class="auth-divider">or</div>' : ''}
        ${status.passwordEnabled ? `
          <form id="login-form">
            <div class="field"><label class="label" for="login-password">Admin password</label><input class="input" id="login-password" name="password" type="password" autocomplete="current-password" required placeholder="Your password"></div>
            <button class="btn ${status.oidcEnabled ? '' : 'primary'} wide" type="submit">Sign in ${icon('arrowRight', 'btn-icon')}</button>
          </form>` : ''}
        ${!status.passwordEnabled && !status.oidcEnabled ? `
          <div class="advanced-note" style="margin-top:16px">
            <strong>No sign-in method configured.</strong><br>
            Set the <code>OIDC_*</code> variables (or <code>ADMIN_PASSWORD</code> locally), then reload the page.
          </div>` : ''}
        <div class="auth-footer"><span>${esc(name)}</span><span>Protected by OIDC</span></div>
      </div>
    </section>
  </div>`;
  if (urlError) history.replaceState({}, '', location.pathname + location.hash);
}

/* ---------- Shell ---------- */
const NAV = [
  ['Workshop', [['dashboard', '#/', 'dashboard', 'Overview'], ['chat', '#/chat', 'chat', 'Chat'], ['runs', '#/runs', 'runs', 'Runs'], ['approvals', '#/approvals', 'hand', 'Approvals']]],
  ['Brain', [['memory', '#/memory', 'brain', 'Memory'], ['tools', '#/tools', 'tool', 'Tools'], ['skills', '#/skills', 'book', 'Persona & skills'], ['evolution', '#/evolution', 'dna', 'Evolution']]],
  ['Actions', [['schedules', '#/schedules', 'clock', 'Tasks'], ['deployments', '#/deployments', 'rocket', 'Deployments'], ['mail', '#/mail', 'mail', 'Mailbox']]],
  ['System', [['access', '#/access', 'shield', 'Access'], ['audit', '#/audit', 'audit', 'Audit log'], ['settings', '#/settings', 'settings', 'Settings']]],
];

function renderShell() {
  const user = state.user || {};
  const name = state.status?.agentName || 'BLAVINGAD';
  app.innerHTML = `
  <div class="app-shell">
    <div class="nav-overlay" data-action="close-nav"></div>
    <aside class="sidebar">
      <a class="brand" href="#/"><span class="logo-mark">${LOGO}</span><span>${esc(name)}</span><small>agent</small></a>
      <div style="overflow-y:auto;min-height:0;flex:1;margin:0 -4px;padding:0 4px">
      ${NAV.map(([label, links]) => `<div class="side-label">${esc(label)}</div><nav class="nav">${links.map(([key, href, ico, text]) => `<a class="nav-link" data-nav="${key}" href="${href}">${icon(ico, 'nav-icon')} ${esc(text)}${key === 'approvals' ? '<span class="nav-badge" id="pending-badge" hidden></span>' : ''}</a>`).join('')}</nav>`).join('')}
      </div>
      <div class="side-note" id="side-status">
        <strong>Connecting…</strong>
        <p>Checking integrations.</p>
      </div>
      <div class="side-foot">
        <div class="user-chip">
          <div class="avatar">${user.avatar_url ? `<img src="${esc(user.avatar_url)}" alt="">` : esc(initials(user.name, user.email))}</div>
          <div class="user-meta"><strong>${esc(user.name || user.email || 'Admin')}</strong><span>${esc(user.email || 'admin')}</span></div>
          <div class="user-actions"><button class="icon-btn" data-action="logout" title="Sign out" aria-label="Sign out">${icon('logout')}</button></div>
        </div>
      </div>
    </aside>
    <div class="main">
      <header class="topbar">
        <button class="icon-btn mobile-menu" data-action="toggle-nav" aria-label="Open navigation" title="Navigation">${icon('menu')}</button>
        <div class="crumbs"><span>${esc(name)}</span><span class="crumb-sep">/</span><strong id="crumb-current">Overview</strong></div>
        <div class="topbar-right">
          <span class="env-pill" id="env-pill">online</span>
          <a class="btn sm primary" href="#/chat/new">${icon('chat', 'btn-icon')} Talk to it</a>
        </div>
      </header>
      <main class="page" id="view"></main>
    </div>
  </div>`;
}

function setCrumbs(label) {
  const node = document.getElementById('crumb-current');
  if (node) node.textContent = label;
  document.title = `${state.status?.agentName || 'BLAVINGAD'} — ${label}`;
}

function setNav(name) {
  document.querySelectorAll('[data-nav]').forEach((link) => link.classList.toggle('active', link.dataset.nav === name));
}

function updatePendingBadge() {
  const node = document.getElementById('pending-badge');
  if (!node) return;
  node.hidden = !state.pending;
  node.textContent = state.pending;
}

function updateSideStatus(overview) {
  const node = document.getElementById('side-status');
  const pill = document.getElementById('env-pill');
  if (!overview) return;
  const integ = Object.fromEntries((overview.integrations || []).map((i) => [i.key, i]));
  const slack = integ.slack || {};
  const llmOk = integ.llm?.ok;
  if (node) {
    node.innerHTML = `<strong><span class="row" style="gap:7px"><span class="live-dot ${slack.ok ? '' : slack.configured ? 'bad' : 'off'}"></span>${slack.ok ? 'Connected to Slack' : slack.configured ? 'Slack disconnected' : 'Slack not configured'}</span></strong>
      <p>${esc(integ.llm?.detail || '')}</p><p>${overview.stats.active_runs ? `⚡ ${overview.stats.active_runs} run${overview.stats.active_runs > 1 ? 's' : ''} in progress` : `v${esc(overview.version)} · ${esc(overview.uptime)}`}</p>`;
  }
  if (pill) {
    pill.classList.toggle('off', !llmOk);
    pill.textContent = llmOk ? (overview.stats.active_runs ? 'working' : 'online') : 'no model';
  }
  state.pending = overview.stats.pending_approvals || 0;
  updatePendingBadge();
}

async function refreshOverview() {
  try {
    state.overview = await api('/overview');
    updateSideStatus(state.overview);
  } catch { /* shown on pages */ }
  return state.overview;
}

/* ---------- Live events (SSE) ---------- */
function connectEvents() {
  if (state.es) state.es.close();
  const es = new EventSource('/api/events');
  state.es = es;
  es.onmessage = (event) => {
    let ev;
    try { ev = JSON.parse(event.data); } catch { return; }
    handleEvent(ev);
  };
}

function scheduleRefresh(fn, delay = 700) {
  clearTimeout(state.refreshTimer);
  state.refreshTimer = setTimeout(fn, delay);
}

function handleEvent(ev) {
  const data = ev.data || {};
  const parts = location.hash.replace(/^#\/?/, '').split('/');
  switch (ev.type) {
    case 'approval.created':
      state.pending += 1;
      updatePendingBadge();
      toast(`✋ Approval needed: ${data.summary || data.tool}`);
      if (parts[0] === 'approvals') scheduleRefresh(renderApprovals, 200);
      break;
    case 'approval.decided':
      state.pending = Math.max(0, state.pending - 1);
      updatePendingBadge();
      if (parts[0] === 'approvals') scheduleRefresh(renderApprovals, 200);
      break;
    case 'run.progress': {
      const p = data.progress || {};
      const steps = state.live[data.run_id] || (state.live[data.run_id] = []);
      if (p.kind === 'tool_start') steps.push({ key: `${p.tool}|${p.label}`, emoji: p.emoji, label: p.label, status: 'run' });
      else if (p.kind === 'tool_denied') steps.push({ emoji: p.emoji, label: `${p.label} — not allowed for your access profile (${p.text})`, status: 'denied' });
      else if (p.kind === 'tool_end') {
        const step = [...steps].reverse().find((s) => s.key === `${p.tool}|${p.label}` && s.status === 'run');
        if (step) step.status = p.ok ? 'ok' : 'err';
      } else if (p.kind === 'note') steps.push({ emoji: '·', label: p.label, status: 'note' });
      else if (p.kind === 'thinking') steps.thinking = p.iteration;
      const liveNode = document.getElementById(`live-${data.run_id}`);
      if (liveNode) liveNode.innerHTML = liveSteps(data.run_id);
      break;
    }
        case 'run.step':
      if (parts[0] === 'runs' && parts[1] === data.run_id) scheduleRefresh(() => renderRun(data.run_id, true), 600);
      break;
    case 'run.started':
    case 'run.finished':
      if (ev.type === 'run.finished') {
        if (parts[0] === 'chat' && state.chat.items.some((item) => item.id === data.id)) loadChatThread(state.chat.thread);
        if (parts[0] === 'runs' && parts[1] === data.id) scheduleRefresh(() => renderRun(data.id, true), 300);
      }
      if (parts[0] === 'runs' && !parts[1]) scheduleRefresh(loadRuns, 800);
      if (!parts[0]) scheduleRefresh(renderDashboard, 1500);
      else scheduleRefresh(refreshOverview, 1500);
      break;
    case 'update': {
      state.updateLog = state.updateLog || [];
      state.updateLog.push(data.message);
      state.updateStatus = { progress: 'Running…', uptodate: 'Nothing to do', failed: 'Failed', staged: 'Restarting on the new version' }[data.status] || '';
      const card = document.getElementById('update-card');
      if (card) {
        card.hidden = false;
        document.getElementById('update-log').textContent = state.updateLog.join('\n');
        document.getElementById('update-status').textContent = state.updateStatus;
      }
      if (data.status === 'uptodate') toast('✅ Already up to date');
      else if (data.status === 'failed') toast('❌ Update failed — see the log on the Evolution page', 'err');
      else if (data.status === 'staged') toast('🚀 New version built, restarting…');
      break;
    }
    case 'evolution':
      if (data.status === 'live' && data.summary) toast(`🧬 ${data.summary}`);
      if (parts[0] === 'evolution') scheduleRefresh(renderEvolution, 500);
      break;
    case 'deployment':
      toast(`${data.status === 'live' ? '🚀' : '⚠️'} ${data.name} ${data.status === 'live' ? 'deployed' : 'deployment failed'}`, data.status === 'live' ? 'ok' : 'err');
      if (parts[0] === 'deployments') scheduleRefresh(renderDeployments, 300);
      break;
    case 'email.received':
      toast(`📬 Mail from ${data.from || '?'}`);
      if (parts[0] === 'mail') scheduleRefresh(renderMail, 300);
      break;
    case 'access':
      if (parts[0] === 'access' && !modalRoot.children.length) scheduleRefresh(renderAccess, 300);
      break;
    case 'slack.status':
      scheduleRefresh(refreshOverview, 300);
      break;
    default:
      break;
  }
}

/* ---------- Dashboard ---------- */
async function renderDashboard() {
  const seq = ++state.routeSeq;
  setNav('dashboard');
  setCrumbs('Overview');
  const view = document.getElementById('view');
  if (!state.overview) view.innerHTML = skeletonPage();
  let data;
  try { data = await refreshOverview(); if (!data) throw new Error('API unreachable'); } catch (error) { if (seq === state.routeSeq) view.innerHTML = errorState(error.message); return; }
  if (seq !== state.routeSeq) return;
  const s = data.stats;
  const name = state.status?.agentName || 'BLAVINGAD';
  const daily = data.daily || [];
  const hour = new Date().getHours();
  const hello = hour < 6 ? 'Still up? 🌙' : hour < 12 ? 'Good morning ☀️' : hour < 18 ? 'Good afternoon 👋' : 'Good evening 🌆';
  const missing = (data.integrations || []).filter((i) => !i.ok && i.key !== 'oidc');
  view.innerHTML = `
    ${pageHead(`${esc(hello)}`, `${esc(name)} handled <strong>${fmtNum(s.runs_today)}</strong> request${s.runs_today === 1 ? '' : 's'} in the last 24h and knows <strong>${fmtNum(s.memories)}</strong> memories.`, `<a class="btn" href="#/runs">${icon('runs', 'btn-icon')} Runs</a><a class="btn primary" href="#/chat/new">${icon('chat', 'btn-icon')} Talk to it</a>`, 'Overview')}
    ${s.pending_approvals ? `<a class="hero-strip" href="#/approvals" style="text-decoration:none;border-color:color-mix(in srgb, var(--warn) 35%, var(--line))"><span class="emoji" style="font-size:18px">✋</span><span><strong>${fmtNum(s.pending_approvals)} action${s.pending_approvals > 1 ? 's' : ''} awaiting your approval</strong> — ${esc(name)} is waiting for your go-ahead.</span><span class="spacer"></span>${icon('arrowRight', 'btn-icon')}</a>` : ''}
    <div class="stat-grid">
      <div class="stat lead"><div class="stat-label">${icon('chat')} Conversations · 24h</div><div class="stat-value">${fmtNum(s.runs_today)}</div><div class="stat-sub">${fmtNum(s.runs_week)} over 7 days</div><div class="spark">${sparkline(daily.map((d) => d.runs))}</div></div>
      <div class="stat"><div class="stat-label">${icon('tool')} Tool calls · 24h</div><div class="stat-value">${fmtNum(s.tool_calls_today)}</div><div class="stat-sub">${fmtCompact(s.tokens_today)} tokens</div><div class="spark">${sparkline(daily.map((d) => d.tools))}</div></div>
      <div class="stat"><div class="stat-label">${icon('brain')} Memory</div><div class="stat-value">${fmtNum(s.memories)}</div><div class="stat-sub">${fmtNum(s.people)} people · ${fmtCompact(s.messages_indexed)} messages read</div></div>
      <div class="stat"><div class="stat-label">${icon('dna')} Evolution · 7d</div><div class="stat-value">${fmtNum(s.evolutions_week)}</div><div class="stat-sub">${fmtNum(s.custom_tools)} tools created · ${fmtNum(s.skills)} skills</div></div>
    </div>
    <div class="grid-2" style="margin-bottom:16px">
      <section class="card"><div class="card-head"><div><h3>Activity</h3><p>Conversations per day, last 14 days.</p></div><span class="row tiny muted">👍 ${fmtNum(s.feedback_up)} · 👎 ${fmtNum(s.feedback_down)} this week</span></div><div class="chart-wrap" data-chart="${esc(JSON.stringify(daily))}" data-key="runs">${lineChart(daily)}</div></section>
      <section class="card"><div class="card-head"><div><h3>Integrations</h3><p>${missing.length ? `${missing.length} to configure` : 'Everything is plugged in ✨'}</p></div><a class="btn xs ghost" href="#/settings">Settings</a></div><div class="card-body"><div class="integ-list">${(data.integrations || []).map((i) => `
        <div class="integ-row"><div class="integ-ico emoji">${i.emoji}</div><div class="integ-main"><strong>${esc(i.label)}</strong><span title="${esc(i.detail)}">${esc(i.detail)}</span></div><span class="live-dot ${i.ok ? '' : i.configured ? 'bad' : 'off'}" title="${i.ok ? 'OK' : 'inactive'}" aria-label="${i.ok ? 'OK' : 'inactive'}"></span></div>`).join('')}</div></div></section>
    </div>
    <div class="grid-2">
      <section class="card"><div class="card-head"><div><h3>Latest runs</h3><p>What ${esc(name)} just did.</p></div><a class="btn xs ghost" href="#/runs">See all</a></div>
        <div class="card-body flush">${(data.recent || []).length ? runsTable(data.recent, true) : emptyState('No conversations yet', 'Mention it in Slack or open the web chat.', '<a class="btn primary" href="#/chat/new">Talk to it</a>')}</div></section>
      <div class="stack">
        ${breakdownCard('Most used tools', (data.top_tools || []).map((t) => ({ label: t.name, html: `<code>${esc(t.name)}</code>`, value: t.calls, meta: `${fmtNum(t.calls)}${t.errors ? ` · ${t.errors} ⚠️` : ''} · ${fmtMs(t.avg_ms || 0)}` })), { sub: 'Last 7 days', empty: 'No tools called this week.' })}
        <section class="card"><div class="card-head"><div><h3>Recent evolutions</h3><p>How it improves itself.</p></div><a class="btn xs ghost" href="#/evolution">History</a></div><div class="card-body">${(data.evolutions || []).length ? data.evolutions.map((e) => `<div class="top-link-row"><span class="emoji">${EVO_KINDS[e.kind]?.[0] || '🧬'}</span><div class="top-link-main"><strong>${esc(e.summary)}</strong><span>${esc(relative(e.created_at))}</span></div>${evoStatus(e.status)}</div>`).join('') : '<div class="hint">No evolutions yet.</div>'}</div></section>
      </div>
    </div>`;
  fitCharts();
}

function runsTable(rows, compact = false) {
  const desktop = `<div class="table-wrap desktop-table"><table class="data"><thead><tr><th>When</th><th>Source</th><th>Request</th><th>Status</th><th class="num">Tools</th>${compact ? '' : '<th class="num">Tokens</th>'}<th class="num">Duration</th></tr></thead><tbody>${rows.map((r) => `
    <tr class="clickable" data-href="#/runs/${esc(r.id)}">
      <td class="nowrap"><span title="${esc(fmtDateTime(r.started_at))}">${esc(relative(r.started_at))}</span></td>
      <td>${sourceChip(r.source)}${r.channel_name && r.source === 'slack' ? `<div class="tiny muted">#${esc(r.channel_name)}</div>` : ''}</td>
      <td><div class="run-input" title="${esc(r.input)}">${esc(stripSpeaker(r.input))}</div><div class="run-sub">${esc(r.user_name || '')}${r.feedback > 0 ? ' · 👍' : r.feedback < 0 ? ' · 👎' : ''}</div></td>
      <td>${runStatus(r.status)}</td>
      <td class="num">${fmtNum(r.tool_calls)}</td>
      ${compact ? '' : `<td class="num">${fmtCompact((r.prompt_tokens || 0) + (r.completion_tokens || 0))}</td>`}
      <td class="num nowrap">${esc(r.finished_at || r.status === 'running' ? duration(r.started_at, r.finished_at) : '—')}</td>
    </tr>`).join('')}</tbody></table></div>`;
  const mobile = `<div class="mobile-links">${rows.map((r) => `<a class="run-card" href="#/runs/${esc(r.id)}"><div class="row between">${sourceChip(r.source)}${runStatus(r.status)}</div><div class="run-input" style="white-space:normal">${esc(truncate(stripSpeaker(r.input), 140))}</div><div class="tiny muted">${esc(relative(r.started_at))} · ${fmtNum(r.tool_calls)} tools · ${esc(duration(r.started_at, r.finished_at))}</div></a>`).join('')}</div>`;
  return desktop + mobile;
}

// Slack inputs are stored as "Name (<@U…>) : text"; the list shows the text.
function stripSpeaker(input) {
  return String(input || '').replace(/^[^\n]{1,80}? \(<@[UW][A-Z0-9]+>\) ?: /, '');
}

/* ---------- Chat ---------- */
const PROMPTS = [
  '🔎 Give me a roundup of what’s new in Go this week',
  '🧠 What do you know about me?',
  '🚀 Build a stylish “hello world” page and deploy it',
  '🛠️ Create yourself a tool that gives the weather for a city',
];

async function renderChat(threadId) {
  const seq = ++state.routeSeq;
  setNav('chat');
  setCrumbs('Chat');
  const view = document.getElementById('view');
  if (threadId === 'new') threadId = '';
  state.chat.thread = threadId || '';
  state.chat.items = [];
  try {
    const threads = await api('/chat/threads');
    state.chat.threads = threads.items || [];
  } catch (error) { view.innerHTML = errorState(error.message); return; }
  if (seq !== state.routeSeq) return;
  view.innerHTML = `
    <div class="chat-shell" id="chat-shell">
      <section class="card chat-threads">
        <div class="card-head"><div><h3>Chats</h3><p>From the dashboard.</p></div><a class="btn xs" href="#/chat/new">${icon('plus', 'btn-icon')} New</a></div>
        <div class="card-body">${state.chat.threads.length ? state.chat.threads.map((t) => `<a class="thread-link ${t.id === state.chat.thread ? 'active' : ''}" href="#/chat/${esc(t.id)}"><strong>${esc(t.title || 'Chat')}</strong><span>${esc(relative(t.updated_at))} · ${fmtNum(t.turns)} turn${t.turns === 1 ? '' : 's'}</span></a>`).join('') : '<div class="hint" style="padding:10px">No chats yet.</div>'}</div>
      </section>
      <section class="card chat-main">
        <div class="card-head" style="border-bottom:1px solid var(--line);padding-bottom:13px">
          <div class="row"><button class="icon-btn mobile-menu" data-action="chat-threads" aria-label="Chats" title="Chats">${icon('menu')}</button><div><h3>${esc(state.chat.threads.find((t) => t.id === state.chat.thread)?.title ? truncate(state.chat.threads.find((t) => t.id === state.chat.thread).title, 60) : 'New chat')}</h3><p>Same brain as in Slack: memory, tools, approvals.</p></div></div>
        </div>
        <div class="chat-log" id="chat-log"></div>
        <form class="composer" id="chat-form">
          <textarea class="textarea" id="chat-input" name="message" rows="1" placeholder="Message ${esc(state.status?.agentName || 'BLAVINGAD')}… (Enter to send, Shift+Enter for a new line)" aria-label="Message"></textarea>
          <button class="btn primary" type="submit" aria-label="Send" title="Send">${icon('send', 'btn-icon')}</button>
        </form>
      </section>
    </div>`;
  if (!state.cache.tools) api('/tools').then((t) => { state.cache.tools = t.items; }).catch(() => {});
  if (state.chat.thread) await loadChatThread(state.chat.thread);
  else renderChatLog();
  document.getElementById('chat-input')?.focus();
}

async function loadChatThread(thread) {
  try {
    const data = await api(`/chat/threads/${encodeURIComponent(thread)}`);
    if (state.chat.thread !== thread) return;
    state.chat.items = data.items || [];
    renderChatLog();
  } catch (error) { toast(error.message, 'err'); }
}

function liveSteps(runId) {
  const steps = state.live[runId] || [];
  const thinking = steps.thinking ? `<div class="step"><span class="live-dot run" style="margin:0 4px"></span><span>Thinking… <span class="tiny">step ${steps.thinking}</span></span></div>` : `<div class="step"><span class="live-dot run" style="margin:0 4px"></span><span>Thinking…</span></div>`;
  return `<div class="live-steps">${steps.slice(-12).map((s) => `<div class="step"><span class="emoji">${s.status === 'run' ? '⏳' : s.status === 'ok' ? '✅' : s.status === 'err' ? '⚠️' : s.status === 'denied' ? '🚫' : ''}</span><span>${s.status === 'note' ? '' : `<span class="emoji">${esc(s.emoji)}</span> `}${esc(s.label)}</span></div>`).join('')}${thinking}</div>`;
}

function botAvatar() {
  return `<div class="avatar">${FACE}</div>`;
}

// The chronological thought process above an answer (collapsed by default):
// recalled memories, thinking steps, tool calls with their arguments and
// results, then the reflection — mirroring the Slack plan block.
function thoughtProcess(item) {
  const steps = item.steps || [];
  if (!steps.length) return '';
  const tools = Object.fromEntries((state.cache.tools || []).map((t) => [t.name, t.emoji]));
  let nTools = 0; let nThoughts = 0;
  const rows = steps.map((st) => {
    if (st.kind === 'thought') {
      nThoughts += 1;
      return `<div class="tp-step"><span class="emoji">💭</span><div class="tp-main"><em>${esc(truncate(st.output, 600))}</em></div></div>`;
    }
    if (st.kind === 'recall') {
      const n = String(st.output || '').split(',').filter(Boolean).length;
      return `<div class="tp-step"><span class="emoji">🧠</span><div class="tp-main">Recalled ${n} memor${n > 1 ? 'ies' : 'y'}</div></div>`;
    }
    if (st.kind === 'reflect') {
      const lines = String(st.output || '').split('\n').filter(Boolean);
      return `<div class="tp-step"><span class="emoji">💭</span><div class="tp-main"><strong>Reflection</strong> — remembered ${lines.length} thing${lines.length > 1 ? 's' : ''}<div class="tp-out">${esc(lines.join('\n'))}</div></div></div>`;
    }
    nTools += 1;
    const icon = st.status === 'ok' ? '✅' : st.status === 'denied' ? '🚫' : '⚠️';
    const args = (() => { try { const v = JSON.parse(st.input || '{}'); return Object.keys(v).length ? JSON.stringify(v) : ''; } catch { return st.input || ''; } })();
    return `<div class="tp-step"><span class="emoji">${icon}</span><div class="tp-main"><span class="emoji">${esc(tools[st.name] || '🔧')}</span> <code>${esc(st.name)}</code>${args ? ` <span class="tp-args">${esc(truncate(args, 160))}</span>` : ''}${st.output ? `<div class="tp-out">${esc(truncate(st.output, 500))}</div>` : ''}</div></div>`;
  });
  const parts = [nThoughts ? `${nThoughts} thought${nThoughts > 1 ? 's' : ''}` : '', nTools ? `${nTools} tool call${nTools > 1 ? 's' : ''}` : ''].filter(Boolean);
  return `<details class="tp"><summary>💭 Thought process${parts.length ? ` · ${parts.join(' · ')}` : ''}</summary><div class="tp-list">${rows.join('')}</div></details>`;
}

function renderChatLog() {
  const log = document.getElementById('chat-log');
  if (!log) return;
  const name = state.status?.agentName || 'BLAVINGAD';
  const user = state.user || {};
  if (!state.chat.items.length) {
    log.innerHTML = `<div class="chat-empty"><span class="logo-mark">${LOGO}</span><h2>Hi, I’m ${esc(name)} 👋</h2><p>Ask me anything: I search, I code, I deploy, I remember.</p><div class="prompt-chips">${PROMPTS.map((p) => `<button class="preset-chip" data-action="chat-prompt" data-prompt="${esc(p.replace(/^\S+\s/, ''))}">${esc(p)}</button>`).join('')}</div></div>`;
    return;
  }
  log.innerHTML = state.chat.items.map((item) => `
    <div class="msg me"><div class="avatar">${user.avatar_url ? `<img src="${esc(user.avatar_url)}" alt="">` : esc(initials(user.name, user.email))}</div><div class="bubble">${esc(item.input)}</div></div>
    <div class="msg bot">${botAvatar()}<div class="bubble">
      ${item.status === 'running'
        ? `<div id="live-${esc(item.id)}">${liveSteps(item.id)}</div><div class="bubble-foot"><button class="btn xs" data-action="cancel-run" data-id="${esc(item.id)}">${icon('stop', 'btn-icon')} Stop</button><a href="#/runs/${esc(item.id)}">watch the live trace</a></div>`
        : `${thoughtProcess(item)}<div class="md">${md(item.output || '…')}</div><div class="bubble-foot">${item.status !== 'done' ? runStatus(item.status) : ''}<span>🔧 ${fmtNum(item.tool_calls)} tool${item.tool_calls > 1 ? 's' : ''}</span><span>⏱️ ${esc(duration(item.started_at, item.finished_at))}</span><a href="#/runs/${esc(item.id)}">trace</a><button class="btn xs ghost" data-action="copy-text" data-text="${esc(item.output || '')}" aria-label="Copy the answer" title="Copy">${icon('copy', 'btn-icon')}</button></div>`}
    </div></div>`).join('');
  log.scrollTop = log.scrollHeight;
}

async function sendChat(message) {
  message = message.trim();
  if (!message) return;
  const input = document.getElementById('chat-input');
  if (input) { input.value = ''; input.style.height = ''; }
  try {
    const res = await api('/chat', { method: 'POST', body: { thread: state.chat.thread, message } });
    state.live[res.run_id] = [];
    state.chat.items.push({ id: res.run_id, input: message, status: 'running', started_at: new Date().toISOString(), tool_calls: 0 });
    if (!state.chat.thread) {
      state.chat.thread = res.thread;
      history.replaceState({}, '', `#/chat/${res.thread}`);
    }
    renderChatLog();
  } catch (error) {
    toast(error.message, 'err');
    if (input) input.value = message;
  }
}

/* ---------- Runs ---------- */
async function renderRuns() {
  ++state.routeSeq;
  setNav('runs');
  setCrumbs('Runs');
  const view = document.getElementById('view');
  view.innerHTML = `${pageHead('Runs', 'Every conversation goes through the harness: prompt, model calls, tools, approvals, reflection. Click one to see the full trace.', '', 'Harness')}
    <div class="filter-bar">
      <div class="search-box">${icon('search')}<input class="input" id="runs-search" placeholder="Search requests and answers…" value="${esc(state.runsSearch)}" aria-label="Search"></div>
      <select class="select mini" id="runs-source" aria-label="Source"><option value="">All sources</option>${Object.entries(SOURCES).filter(([k]) => k !== 'subagent').map(([k, [e, l]]) => `<option value="${k}" ${state.runsSource === k ? 'selected' : ''}>${e} ${l}</option>`).join('')}</select>
      <select class="select mini" id="runs-status" aria-label="Status"><option value="">All statuses</option>${['running', 'done', 'error', 'cancelled', 'interrupted'].map((k) => `<option value="${k}" ${state.runsStatus === k ? 'selected' : ''}>${({ running: 'Running', done: 'Done', error: 'Error', cancelled: 'Stopped', interrupted: 'Interrupted' })[k]}</option>`).join('')}</select>
      <span class="result-count" id="runs-count"></span>
    </div>
    <section class="card" id="runs-card"><div class="card-body" style="padding:20px"><div class="skeleton" style="height:200px"></div></div></section>`;
  await loadRuns();
}

async function loadRuns() {
  const card = document.getElementById('runs-card');
  if (!card) return;
  const params = new URLSearchParams({ page: state.runsPage, q: state.runsSearch, source: state.runsSource, status: state.runsStatus });
  try {
    const data = await api(`/runs?${params}`);
    const count = document.getElementById('runs-count');
    if (count) count.textContent = `${fmtNum(data.total)} run${data.total === 1 ? '' : 's'}`;
    const pages = Math.max(1, Math.ceil(data.total / data.page_size));
    card.innerHTML = data.items.length ? `${runsTable(data.items)}
      <div class="pagination"><span>Page ${data.page} of ${pages}</span><div class="pagination-controls"><button class="btn sm" data-action="runs-page" data-page="${data.page - 1}" ${data.page <= 1 ? 'disabled' : ''}>Previous</button><button class="btn sm" data-action="runs-page" data-page="${data.page + 1}" ${data.page >= pages ? 'disabled' : ''}>Next</button></div></div>`
      : emptyState('No runs', state.runsSearch || state.runsSource || state.runsStatus ? 'Nothing matches these filters.' : 'Conversations will show up here in real time.');
  } catch (error) { card.innerHTML = errorState(error.message); }
}

const STEP_META = {
  llm: ['spark', 'llm', 'Model call'],
  tool: ['tool', '', 'Tool'],
  approval: ['hand', 'pending', 'Approval request'],
  recall: ['brain', 'memory', 'Recalled memories'],
  reflect: ['dna', 'memory', 'Reflection · memorizing'],
  compact: ['history', 'llm', 'Context compaction'],
  access: ['shield', 'memory', 'Access profile'],
  thought: ['spark', 'memory', 'Thinking'],
};

function stepItem(step, toolsByName) {
  const [ico, cls, title] = STEP_META[step.kind] || ['spark', '', step.kind];
  const tool = toolsByName[step.name];
  const statusCls = step.kind === 'tool' ? (step.status === 'ok' ? 'ok' : step.status)
    : step.kind === 'approval' ? (step.status === 'approved' ? 'ok' : 'denied') : cls;
  const dot = step.kind === 'tool' ? `<span class="emoji">${esc(tool?.emoji || '🔧')}</span>` : icon(ico);
  let label = '';
  let text = '';
  let detail = '';
  if (step.kind === 'llm') {
    label = `<span>${esc(title)}</span> <code class="muted">${esc(step.name)}</code>`;
    text = step.input ? `→ calls ${esc(step.input)}` : (step.status === 'error' ? `<span style="color:var(--blk)">${esc(truncate(step.output, 300))}</span>` : 'Final answer');
    if (step.output && step.status !== 'error') detail = `<div class="io-label">Model text</div><div class="pre sm">${esc(step.output)}</div>`;
  } else if (step.kind === 'tool') {
    label = `<code>${esc(step.name)}</code>${step.status === 'error' ? badge('error', 'bad') : step.status === 'denied' ? badge('denied', 'bad') : ''}`;
    text = esc(truncate(String(step.output || '').split('\n')[0], 160));
    detail = `<div class="io-label">Arguments</div><div class="pre sm">${esc(prettyJSON(step.input))}</div><div class="io-label">Result</div><div class="pre">${esc(step.output)}</div>`;
  } else if (step.kind === 'approval') {
    label = `${esc(title)} · <code>${esc(step.name)}</code>${step.status === 'approved' ? badge('approved', 'ok') : badge('denied', 'bad')}`;
    text = step.status === 'approved' ? `Human go-ahead received after ${fmtMs(step.duration_ms || 0)}.` : esc(step.output || 'Denied.');
  } else if (step.kind === 'thought') {
    label = esc(title);
    text = `<em>${esc(truncate(step.output, 400))}</em>`;
    if (String(step.output || '').length > 400) detail = `<div class="pre sm">${esc(step.output)}</div>`;
  } else if (step.kind === 'access') {
    label = `${esc(title)} · ${esc(step.input)}`;
    text = `Restricted conversation: ${esc(step.output)}`;
  } else {
    label = esc(title);
    const lines = String(step.output || '').split('\n').filter(Boolean);
    text = step.kind === 'recall' ? `${lines.length} memor${lines.length === 1 ? 'y' : 'ies'} injected into the prompt` : step.kind === 'compact' ? esc(step.input) : `${lines.length} item${lines.length === 1 ? '' : 's'} retained`;
    if (step.output) detail = `<div class="pre sm">${esc(step.output)}</div>`;
  }
  const meta = [step.duration_ms ? fmtMs(step.duration_ms) : '', step.tokens ? `${fmtCompact(step.tokens)} tok` : '', fmtTime(step.created_at)].filter(Boolean).join(' · ');
  return `<div class="tl-item"><div class="tl-dot ${statusCls}">${dot}</div><div class="tl-body"><div class="tl-title">${label}<span class="tl-time">${esc(meta)}</span></div>${text ? `<div class="tl-text">${text}</div>` : ''}${detail ? `<details class="tl-details"><summary>Details</summary>${detail}</details>` : ''}</div></div>`;
}

async function renderRun(id, silent = false) {
  const seq = silent ? state.routeSeq : ++state.routeSeq;
  setNav('runs');
  setCrumbs('Trace');
  const view = document.getElementById('view');
  if (!silent) view.innerHTML = skeletonPage();
  let data;
  try {
    [data] = await Promise.all([api(`/runs/${encodeURIComponent(id)}`), state.cache.tools ? null : api('/tools').then((t) => { state.cache.tools = t.items; })]);
  } catch (error) { if (seq === state.routeSeq) view.innerHTML = errorState(error.message); return; }
  if (seq !== state.routeSeq) return;
  const run = data.run;
  const toolsByName = Object.fromEntries((state.cache.tools || []).map((t) => [t.name, t]));
  const tools = data.steps.filter((s) => s.kind === 'tool');
  const llmCalls = data.steps.filter((s) => s.kind === 'llm').length;
  const tokens = (run.prompt_tokens || 0) + (run.completion_tokens || 0);
  const openDetails = [...view.querySelectorAll('details[open]')].map((d) => d.dataset.key);
  view.innerHTML = `
    <a class="back-link" href="#/runs">${icon('arrowLeft', 'btn-icon')} Runs</a>
    <section class="card" style="margin-bottom:16px">
      <div class="trace-head">
        <div class="big-icon emoji">${(SOURCES[run.source] || ['⚡'])[0]}</div>
        <div style="flex:1;min-width:0">
          <h2>${esc(truncate(stripSpeaker(run.input), 280))}</h2>
          <div class="trace-meta">${runStatus(run.status)}${sourceChip(run.source)}${run.user_name ? `<span class="tiny muted">by ${esc(run.user_name)}</span>` : ''}${run.channel_name && run.source === 'slack' ? `<span class="tiny muted">in #${esc(run.channel_name)}</span>` : ''}<span class="tiny muted">${esc(fmtDateTime(run.started_at))}</span>${run.parent_id ? `<a class="tiny" href="#/runs/${esc(run.parent_id)}">↖ parent run</a>` : ''}${run.feedback > 0 ? badge('👍 liked', 'ok', false) : run.feedback < 0 ? badge('👎 needs work', 'bad', false) : ''}</div>
        </div>
        <div class="analytics-header-actions">${run.status === 'running' ? `<button class="btn danger" data-action="cancel-run" data-id="${esc(run.id)}">${icon('stop', 'btn-icon')} Stop</button>` : ''}<button class="btn" data-action="copy-text" data-text="${esc(run.id)}">${icon('copy', 'btn-icon')} ID</button></div>
      </div>
      <div class="card-foot" style="justify-content:flex-start;gap:26px;flex-wrap:wrap">
        <div class="metric-inline">
          <div class="metric"><strong>${esc(duration(run.started_at, run.finished_at))}</strong><span>Duration</span></div>
          <div class="metric"><strong>${fmtNum(llmCalls)}</strong><span>Model calls</span></div>
          <div class="metric"><strong>${fmtNum(tools.length)}</strong><span>Tools</span></div>
          <div class="metric"><strong>${fmtCompact(tokens)}</strong><span>Tokens</span></div>
          <div class="metric"><strong style="font-size:15px">${esc(run.model || '—')}</strong><span>Model</span></div>
        </div>
      </div>
    </section>
    <div class="grid-2 flip">
      <section class="card"><div class="card-head"><div><h3>Harness trace</h3><p>${fmtNum(data.steps.length)} step${data.steps.length === 1 ? '' : 's'}${run.status === 'running' ? ' · live' : ''}</p></div>${run.status === 'running' ? '<span class="live-dot run"></span>' : ''}</div>
        <div class="timeline">${data.steps.length ? data.steps.map((s) => stepItem(s, toolsByName)).join('') : '<div class="hint">No steps recorded.</div>'}</div></section>
      <div class="stack" style="align-content:start">
        <section class="card"><div class="card-head"><div><h3>Answer</h3><p>${run.status === 'running' ? 'Writing…' : 'What the agent replied.'}</p></div>${run.output ? `<button class="icon-btn" data-action="copy-text" data-text="${esc(run.output)}" aria-label="Copy" title="Copy">${icon('copy')}</button>` : ''}</div>
          <div class="card-body">${run.output ? `<div class="md">${md(run.output)}</div>` : '<div class="hint">—</div>'}${run.error ? `<div class="auth-error" style="margin:12px 0 0">${esc(run.error)}</div>` : ''}</div></section>
        ${data.children.length ? `<section class="card"><div class="card-head"><div><h3>🧑‍🚀 Sub-agents</h3><p>Tasks delegated in parallel.</p></div></div><div class="card-body">${data.children.map((c) => `<a class="top-link-row" href="#/runs/${esc(c.id)}" style="text-decoration:none;color:inherit"><div class="top-link-main"><strong>${esc(c.input)}</strong><span>${fmtNum(c.tool_calls)} tools · ${esc(duration(c.started_at, c.finished_at))}</span></div>${runStatus(c.status)}</a>`).join('')}</div></section>` : ''}
        ${data.approvals.length ? `<section class="card"><div class="card-head"><div><h3>✋ Approvals</h3></div></div><div class="card-body">${data.approvals.map((a) => `<div class="top-link-row"><div class="top-link-main"><strong>${esc(a.summary)}</strong><span>${esc(a.decided_by ? `by ${a.decided_by}` : relative(a.created_at))}</span></div>${approvalStatus(a.status)}</div>`).join('')}</div></section>` : ''}
        <section class="card"><div class="card-head"><div><h3>Full request</h3></div></div><div class="card-body"><div class="pre">${esc(run.input)}</div></div></section>
      </div>
    </div>`;
  view.querySelectorAll('details.tl-details').forEach((d, i) => { d.dataset.key = String(i); if (openDetails.includes(String(i))) d.open = true; });
}

/* ---------- Approvals ---------- */
async function renderApprovals() {
  const seq = ++state.routeSeq;
  setNav('approvals');
  setCrumbs('Approvals');
  const view = document.getElementById('view');
  let data;
  try { data = await api('/approvals?status=all'); } catch (error) { view.innerHTML = errorState(error.message); return; }
  if (seq !== state.routeSeq) return;
  const pending = data.items.filter((a) => a.status === 'pending');
  const done = data.items.filter((a) => a.status !== 'pending');
  state.pending = pending.length;
  updatePendingBadge();
  view.innerHTML = `${pageHead('Approvals', 'Sensitive actions (shell, deployments, emails, GitHub writes…) wait for a human go-ahead. Approve here or with the buttons in Slack. Set per-tool policies in <a href="#/tools">Tools</a>.', '', 'Guardrails')}
    ${pending.length ? `<div class="stack" style="margin-bottom:18px">${pending.map((a) => `
      <div class="approval-card">
        <div class="approval-top"><div class="approval-ico emoji">✋</div><div style="min-width:0;flex:1"><h3>${esc(a.summary)}</h3><div class="trace-meta"><code class="small">${esc(a.tool)}</code><span class="tiny muted">requested by ${esc(a.requested_by || '—')} · ${esc(relative(a.created_at))}</span>${a.run_id ? `<a class="tiny" href="#/runs/${esc(a.run_id)}">view run</a>` : ''}</div></div></div>
        ${a.run_input ? `<div class="tiny muted">Context: “${esc(truncate(stripSpeaker(a.run_input), 200))}”</div>` : ''}
        <div class="pre sm">${esc(prettyJSON(a.args))}</div>
        <div class="approval-actions"><button class="btn primary" data-action="decide" data-id="${esc(a.id)}" data-decision="approve">${icon('check', 'btn-icon')} Approve</button><button class="btn danger" data-action="decide" data-id="${esc(a.id)}" data-decision="deny">${icon('x', 'btn-icon')} Deny</button><button class="btn ghost" data-action="decide" data-id="${esc(a.id)}" data-decision="always">♾️ Always allow <code>${esc(a.tool)}</code></button></div>
      </div>`).join('')}</div>` : `<section class="card" style="margin-bottom:18px">${emptyState('Nothing to approve 🎉', 'When the agent wants to do something sensitive, the request will show up here in real time.')}</section>`}
    <section class="card"><div class="card-head"><div><h3>History</h3><p>The last 100 decisions.</p></div></div>
      <div class="card-body flush">${done.length ? `<div class="table-wrap"><table class="data"><thead><tr><th>When</th><th>Action</th><th>Tool</th><th>Decision</th><th>By</th></tr></thead><tbody>${done.map((a) => `<tr${a.run_id ? ` class="clickable" data-href="#/runs/${esc(a.run_id)}"` : ''}><td class="nowrap">${esc(relative(a.created_at))}</td><td><div class="truncate-cell" title="${esc(a.summary)}">${esc(a.summary)}</div></td><td><code class="small">${esc(a.tool)}</code></td><td>${approvalStatus(a.status)}</td><td class="small">${esc(a.decided_by || '—')}</td></tr>`).join('')}</tbody></table></div>` : '<div class="hint" style="padding:0 20px 20px">No decisions yet.</div>'}</div></section>`;
}

/* ---------- Memory ---------- */
const MEM_KINDS = {
  fact: ['📌', 'Fact'], preference: ['💜', 'Preference'], person: ['👤', 'Person'], project: ['🗂️', 'Project'],
  lesson: ['🎓', 'Lesson'], procedure: ['📋', 'Procedure'], event: ['📅', 'Event'], note: ['📝', 'Note'],
};

function memTabs() {
  const tabs = [['memories', 'Memories'], ['people', 'People'], ['channels', 'Channels']];
  return `<div class="segmented" role="tablist" style="margin-bottom:16px">${tabs.map(([k, l]) => `<button class="${state.memTab === k ? 'active' : ''}" data-action="mem-tab" data-tab="${k}" role="tab" aria-selected="${state.memTab === k}">${l}</button>`).join('')}</div>`;
}

async function renderMemory() {
  ++state.routeSeq;
  setNav('memory');
  setCrumbs('Memory');
  const view = document.getElementById('view');
  const actions = state.memTab === 'memories' ? `<button class="btn primary" data-action="new-memory">${icon('plus', 'btn-icon')} Add a memory</button>` : '';
  view.innerHTML = `${pageHead('Memory', 'What the agent knows long-term: typed memories with hybrid search (keywords + semantic), notes on people and digests of the channels it reads. Everything is editable.', actions, 'Brain')}${memTabs()}<div id="mem-body"><section class="card"><div class="card-body" style="padding:20px"><div class="skeleton" style="height:220px"></div></div></section></div>`;
  if (state.memTab === 'people') return loadPeople();
  if (state.memTab === 'channels') return loadChannels();
  return loadMemories();
}

async function loadMemories() {
  const body = document.getElementById('mem-body');
  if (!body) return;
  const params = new URLSearchParams({ q: state.memSearch, kind: state.memKind, page: state.memPage, semantic: state.memSemantic ? '1' : '' });
  let data;
  try { data = await api(`/memories?${params}`); } catch (error) { body.innerHTML = errorState(error.message); return; }
  const kinds = data.kinds || {};
  const totalAll = Object.values(kinds).reduce((a, b) => a + b, 0);
  const pages = Math.max(1, Math.ceil(data.total / data.page_size));
  const searchFocused = document.activeElement?.id === 'mem-search';
  body.innerHTML = `
    <div class="filter-bar">
      <div class="search-box">${icon('search')}<input class="input" id="mem-search" placeholder="${state.memSemantic ? 'Search by meaning…' : 'Search keywords…'}" value="${esc(state.memSearch)}" aria-label="Search"></div>
      <label class="check-row" title="${data.vectors ? '' : 'Set EMBED_MODEL to enable semantic search'}"><input type="checkbox" id="mem-semantic" ${state.memSemantic ? 'checked' : ''} ${data.vectors ? '' : 'disabled'}> Semantic search ${data.vectors ? '' : '(EMBED_MODEL required)'}</label>
      <span class="result-count">${fmtNum(data.total)} memor${data.total === 1 ? 'y' : 'ies'}</span>
    </div>
    <div class="kind-chips"><button class="preset-chip ${!state.memKind ? 'active' : ''}" data-action="mem-kind" data-kind="">All <span class="count">${fmtNum(totalAll)}</span></button>${Object.entries(MEM_KINDS).map(([k, [e, l]]) => `<button class="preset-chip ${state.memKind === k ? 'active' : ''}" data-action="mem-kind" data-kind="${k}"><span class="emoji">${e}</span> ${l} <span class="count">${fmtNum(kinds[k] || 0)}</span></button>`).join('')}</div>
    <section class="card">${data.items.length ? `<div class="mem-list">${data.items.map((m) => `
      <div class="mem-row">
        <div class="mem-ico emoji" title="${esc((MEM_KINDS[m.kind] || ['', m.kind])[1])}">${(MEM_KINDS[m.kind] || ['🧠'])[0]}</div>
        <div style="min-width:0"><div class="mem-content">${esc(m.content)}</div>
          <div class="mem-meta">${m.subject ? badge(m.subject, 'acc', false) : ''}<span class="stars" title="Importance ${m.importance}/5" aria-label="Importance ${m.importance} of 5">${'★'.repeat(m.importance)}${'☆'.repeat(5 - m.importance)}</span><span>#${m.id}</span><span>updated ${esc(relative(m.updated_at))}</span>${m.use_count ? `<span>recalled ${fmtNum(m.use_count)}×</span>` : ''}${m.tags ? `<span>🏷️ ${esc(m.tags)}</span>` : ''}${m.source ? `<span title="${esc(m.source)}">${esc(m.source.startsWith('reflection') ? '🪄 auto reflection' : m.source.split(':')[0])}</span>` : ''}${m.score ? `<span>score ${m.score.toFixed(3)}</span>` : ''}</div></div>
        <div class="action-row"><button class="icon-btn" data-action="edit-memory" data-id="${m.id}" aria-label="Edit" title="Edit">${icon('edit')}</button><button class="icon-btn danger" data-action="delete-memory" data-id="${m.id}" aria-label="Forget" title="Forget">${icon('trash')}</button></div>
      </div>`).join('')}</div>
      ${!state.memSemantic && data.total > data.page_size ? `<div class="pagination"><span>Page ${data.page} of ${pages}</span><div class="pagination-controls"><button class="btn sm" data-action="mem-page" data-page="${data.page - 1}" ${data.page <= 1 ? 'disabled' : ''}>Previous</button><button class="btn sm" data-action="mem-page" data-page="${data.page + 1}" ${data.page >= pages ? 'disabled' : ''}>Next</button></div></div>` : ''}`
    : emptyState(state.memSearch || state.memKind ? 'Nothing found' : 'Blank memory', state.memSearch || state.memKind ? 'Try other words or another type.' : 'Memories build up on their own as conversations happen (automatic reflection), or add them by hand.')}</section>`;
  state.cache.memories = data.items;
  if (searchFocused) { const input = document.getElementById('mem-search'); input?.focus(); input?.setSelectionRange(input.value.length, input.value.length); }
}

function memoryModal(m = null) {
  showModal(`${modalHead(m ? `Memory #${m.id}` : 'New memory', 'One precise, self-contained sentence: who, what, when.')}
    <form id="memory-form" data-id="${m ? m.id : ''}"><div class="modal-body"><div class="form-grid">
      <div class="field span-2"><label class="label" for="mem-content">Content</label><textarea class="textarea" id="mem-content" name="content" required autofocus>${esc(m?.content || '')}</textarea></div>
      <div class="field"><label class="label" for="mem-kind">Type</label><select class="select" id="mem-kind" name="kind">${Object.entries(MEM_KINDS).map(([k, [e, l]]) => `<option value="${k}" ${(m?.kind || 'fact') === k ? 'selected' : ''}>${e} ${l}</option>`).join('')}</select></div>
      <div class="field"><label class="label" for="mem-importance">Importance</label><select class="select" id="mem-importance" name="importance">${[1, 2, 3, 4, 5].map((n) => `<option value="${n}" ${(m?.importance || 3) === n ? 'selected' : ''}>${'★'.repeat(n)} ${['trivial', 'useful', 'normal', 'important', 'critical'][n - 1]}</option>`).join('')}</select></div>
      <div class="field"><label class="label" for="mem-subject">Subject</label><input class="input" id="mem-subject" name="subject" value="${esc(m?.subject || '')}" placeholder="e.g. Fold project"></div>
      <div class="field"><label class="label" for="mem-tags">Tags</label><input class="input" id="mem-tags" name="tags" value="${esc(m?.tags || '')}" placeholder="infra, cloudflare"></div>
    </div></div>
    <div class="modal-foot"><button class="btn" type="button" data-action="close-modal">Cancel</button><button class="btn primary" type="submit">${m ? 'Save' : 'Remember'}</button></div></form>`);
}

async function loadPeople() {
  const body = document.getElementById('mem-body');
  let data;
  try { data = await api('/people'); } catch (error) { body.innerHTML = errorState(error.message); return; }
  state.cache.people = data.items;
  body.innerHTML = `<section class="card">${data.items.length ? `<div class="table-wrap"><table class="data"><thead><tr><th>Person</th><th>Access</th><th>What the agent knows</th><th class="num">Messages</th><th>Seen</th><th></th></tr></thead><tbody>${data.items.map((p) => `
    <tr><td><div class="link-ident"><div class="avatar">${esc(initials(p.real_name || p.name))}</div><div><div class="link-title">${esc(p.real_name || p.name || p.slack_id)}</div><div class="link-target">${esc([p.title, p.tz].filter(Boolean).join(' · ') || p.slack_id)}</div></div></div></td>
    <td class="nowrap"><a href="#/access" title="${p.listed ? 'On the allowlist' : 'Default profile (not on the allowlist)'}">${badge(`${p.profile_emoji} ${p.profile_label}`, p.listed ? 'acc' : '', false)}</a></td>
    <td style="max-width:420px"><div class="person-notes">${p.notes ? esc(p.notes) : '<span class="muted">— no notes yet</span>'}</div></td>
    <td class="num">${fmtNum(p.message_count)}</td><td class="nowrap small">${esc(relative(p.last_seen_at))}</td>
    <td class="actions"><button class="icon-btn" data-action="edit-person" data-id="${esc(p.slack_id)}" aria-label="Edit notes" title="Edit notes">${icon('edit')}</button></td></tr>`).join('')}</tbody></table></div>`
    : emptyState('Nobody yet', 'Profiles fill in as soon as people talk in channels where the agent is present.')}</section>`;
}

async function loadChannels() {
  const body = document.getElementById('mem-body');
  let data;
  try { data = await api('/channels'); } catch (error) { body.innerHTML = errorState(error.message); return; }
  const channels = data.items.filter((c) => c.kind !== 'im');
  body.innerHTML = channels.length ? `<div class="deploy-grid">${channels.map((c) => `
    <section class="card digest-card">
      <div class="row between"><h3 style="font-size:15px">${c.kind === 'private' ? '🔒' : '#'}${esc(c.name)}</h3><span class="tiny muted">${fmtNum(c.messages)} messages</span></div>
      ${c.topic ? `<div class="tiny muted">${esc(c.topic)}</div>` : ''}
      <div class="digest-text">${c.digest ? esc(c.digest) : '<span class="muted">No digest yet (one is created automatically every ~25 messages).</span>'}</div>
      <div><button class="btn xs" data-action="digest" data-id="${esc(c.id)}">${icon('refresh', 'btn-icon')} Update the digest</button></div>
    </section>`).join('')}</div>` : `<section class="card">${emptyState('No channels', 'Invite the agent to a Slack channel (/invite): it will read the history and write a digest.')}</section>`;
}

/* ---------- Tools ---------- */
const CATEGORIES = {
  web: ['🌐', 'Web'], slack: ['💬', 'Slack'], memory: ['🧠', 'Memory'], code: ['💻', 'Code & files'], github: ['🐙', 'GitHub'],
  email: ['📬', 'Email'], deploy: ['🚀', 'Deployment'], self: ['🧬', 'Self-improvement'], system: ['⚙️', 'System'], access: ['🛂', 'Access'], vault: ['🔐', 'Vault'], custom: ['🧩', 'Agent-created'],
};
const POLICIES = [['auto', 'Auto'], ['approve', 'Approve'], ['off', 'Off']];

async function renderTools() {
  const seq = ++state.routeSeq;
  setNav('tools');
  setCrumbs('Tools');
  const view = document.getElementById('view');
  let data;
  try { data = await api('/tools'); } catch (error) { view.innerHTML = errorState(error.message); return; }
  if (seq !== state.routeSeq) return;
  state.cache.tools = data.items;
  const groups = {};
  for (const t of data.items) (groups[t.category] ||= []).push(t);
  const order = Object.keys(CATEGORIES).filter((k) => groups[k]).concat(Object.keys(groups).filter((k) => !CATEGORIES[k]));
  const available = data.items.filter((t) => t.available).length;
  view.innerHTML = `${pageHead('Tools', `${fmtNum(available)} of ${fmtNum(data.items.length)} tools active. Pick one per tool: <strong>Auto</strong> (free), <strong>Approve</strong> (button in Slack) or <strong>Off</strong>. Greyed-out tools are waiting for their integration.`, '', 'Brain')}
    ${order.map((cat) => `<section class="card tool-group"><div class="card-head"><div><h3><span class="emoji">${(CATEGORIES[cat] || ['🔧'])[0]}</span> ${esc((CATEGORIES[cat] || ['', cat])[1])}</h3></div><span class="tiny muted">${groups[cat].length} tool${groups[cat].length > 1 ? 's' : ''}</span></div>
      ${groups[cat].map((t) => `
      <div class="tool-row ${!t.available || t.policy === 'off' ? 'is-off' : ''}">
        <div class="mem-ico emoji">${esc(t.emoji)}</div>
        <div style="min-width:0"><div class="tool-name"><code>${esc(t.name)}</code>${!t.available ? badge('not configured', '', false) : ''}${t.sensitive ? badge('sensitive', 'warn') : ''}${t.custom ? badge('agent-created', 'acc') : ''}${t.conditional && t.policy === 'approve' ? `<span class="tiny muted" title="Read-only calls go through without approval">read-only = auto</span>` : ''}</div><div class="tool-desc" title="${esc(t.description)}">${esc(t.description)}</div></div>
        <div class="tool-side">
          <div class="tool-usage">${t.calls_7d ? `${fmtNum(t.calls_7d)} calls / 7d${t.errors_7d ? `<br><span style="color:var(--blk)">${t.errors_7d} error${t.errors_7d > 1 ? 's' : ''}</span>` : ''}` : '—'}</div>
          ${t.custom ? `<button class="icon-btn" data-action="view-tool" data-name="${esc(t.name)}" aria-label="View code" title="View code">${icon('code')}</button>` : ''}
          <div class="segmented" role="group" aria-label="Policy for ${esc(t.name)}">${POLICIES.map(([p, l]) => `<button class="p-${p} ${t.policy === p ? 'active' : ''}" data-action="tool-policy" data-name="${esc(t.name)}" data-policy="${p}" title="${p === t.default_policy ? 'Default value' : ''}">${l}</button>`).join('')}</div>
        </div>
      </div>`).join('')}</section>`).join('')}`;
}

function toolCodeModal(name) {
  const t = (state.cache.tools || []).find((x) => x.name === name);
  if (!t) return;
  showModal(`${modalHead(`🧩 ${t.name}`, `Created by ${t.author || 'the agent'} · ${t.runtime} · updated ${relative(t.updated_at)}`)}
    <div class="modal-body"><p class="small" style="margin-bottom:12px">${esc(t.description)}</p><div class="io-label">Parameters</div><div class="pre sm">${esc(JSON.stringify(t.params, null, 2))}</div><div class="io-label">Code</div><div class="pre">${esc(t.code || '')}</div></div>
    <div class="modal-foot"><button class="btn danger left" data-action="delete-tool" data-name="${esc(t.name)}">${icon('trash', 'btn-icon')} Delete</button><button class="btn" data-action="close-modal">Close</button></div>`, 'lg');
}

/* ---------- Persona & skills ---------- */
async function renderSkills() {
  const seq = ++state.routeSeq;
  setNav('skills');
  setCrumbs('Persona & skills');
  const view = document.getElementById('view');
  let persona; let skills;
  try { [persona, skills] = await Promise.all([api('/persona'), api('/skills')]); } catch (error) { view.innerHTML = errorState(error.message); return; }
  if (seq !== state.routeSeq) return;
  state.cache.persona = persona;
  state.cache.skills = skills.items;
  view.innerHTML = `${pageHead('Persona & skills', 'The persona defines its personality (it can rewrite it itself). Skills are procedures it has captured and reloads when useful.', `<button class="btn primary" data-action="new-skill">${icon('plus', 'btn-icon')} New skill</button>`, 'Brain')}
    <div class="grid-2">
      <section class="card"><div class="card-head"><div><h3>🎭 Persona</h3><p>${persona.versions.length ? `Version ${persona.versions.length} · ${esc(persona.versions[0].reason)} — ${esc(persona.versions[0].author)}, ${esc(relative(persona.versions[0].created_at))}` : 'Original persona'} · <code>{{NAME}}</code> = ${esc(persona.name)}</p></div><button class="btn xs ghost" data-action="persona-history" ${persona.versions.length ? '' : 'disabled'}>${icon('history', 'btn-icon')} Versions</button></div>
        <form class="card-body" id="persona-form"><textarea class="textarea code" name="content" id="persona-content" style="min-height:380px" aria-label="Persona">${esc(persona.current)}</textarea>
          <div class="row wrap" style="margin-top:12px"><input class="input sm" name="reason" placeholder="Why this change? (optional)" style="flex:1;min-width:200px" aria-label="Reason"><button class="btn sm" type="button" data-action="persona-reset">Restore the original</button><button class="btn sm primary" type="submit">Save</button></div>
        </form></section>
      <section class="card"><div class="card-head"><div><h3>📚 Skills</h3><p>${fmtNum(skills.items.length)} reusable procedure${skills.items.length === 1 ? '' : 's'}.</p></div></div>
        <div class="card-body flush">${skills.items.length ? skills.items.map((s) => `
          <div class="tool-row ${s.enabled ? '' : 'is-off'}"><div class="mem-ico emoji">📘</div><div style="min-width:0"><div class="tool-name"><code>${esc(s.name)}</code>${s.uses ? `<span class="tiny muted">used ${fmtNum(s.uses)}×</span>` : ''}</div><div class="tool-desc">${esc(s.description)}</div><div class="tiny muted" style="margin-top:3px">${esc(s.author || '')} · ${esc(relative(s.updated_at))}</div></div>
            <div class="tool-side"><label class="switch" title="${s.enabled ? 'Disable' : 'Enable'}"><input type="checkbox" data-skill-toggle="${esc(s.name)}" ${s.enabled ? 'checked' : ''} aria-label="Enable ${esc(s.name)}"><span></span></label><button class="icon-btn" data-action="edit-skill" data-name="${esc(s.name)}" aria-label="Edit" title="Edit">${icon('edit')}</button><button class="icon-btn danger" data-action="delete-skill" data-name="${esc(s.name)}" aria-label="Delete" title="Delete">${icon('trash')}</button></div></div>`).join('')
        : emptyState('No skills', 'It creates them on its own when it solves a problem likely to come back. You can also write some for it.')}</div></section>
    </div>`;
}

function skillModal(s = null) {
  showModal(`${modalHead(s ? `Skill “${s.name}”` : 'New skill', 'A markdown procedure: steps, commands, pitfalls to avoid.')}
    <form id="skill-form"><div class="modal-body"><div class="form-grid">
      <div class="field"><label class="label" for="skill-name">Name</label><input class="input mono" id="skill-name" name="name" required value="${esc(s?.name || '')}" placeholder="deploy-nextjs" ${s ? 'readonly' : 'autofocus'}></div>
      <div class="field"><label class="label" for="skill-desc">When to use it</label><input class="input" id="skill-desc" name="description" required value="${esc(s?.description || '')}" placeholder="To deploy a Next.js app on Cloudflare"></div>
      <div class="field span-2"><label class="label" for="skill-content">Content</label><textarea class="textarea code" id="skill-content" name="content" required style="min-height:320px">${esc(s?.content || '')}</textarea></div>
    </div></div><div class="modal-foot"><button class="btn" type="button" data-action="close-modal">Cancel</button><button class="btn primary" type="submit">Save</button></div></form>`, 'lg');
}

function personaHistoryModal() {
  const versions = state.cache.persona?.versions || [];
  showModal(`${modalHead('Persona versions', 'Most recent first.')}
    <div class="modal-body"><div class="stack">${versions.map((v, i) => `<details class="card" ${i === 0 ? 'open' : ''}><summary style="padding:13px 16px;cursor:pointer;font-size:12.5px"><strong>v${versions.length - i}</strong> · ${esc(v.reason)} <span class="muted">— ${esc(v.author)}, ${esc(fmtDateTime(v.created_at))}</span></summary><div style="padding:0 16px 14px"><div class="pre">${esc(v.content)}</div><button class="btn xs" style="margin-top:8px" data-action="persona-restore" data-id="${v.id}">Restore this version</button></div></details>`).join('')}</div></div>`, 'lg');
}

/* ---------- Evolution ---------- */
const EVO_KINDS = { update: ['⬆️', 'Update'], code: ['🧬', 'Code'], persona: ['🎭', 'Persona'], skill: ['📚', 'Skill'], tool: ['🛠️', 'Tool'], rollback: ['⏪', 'Rollback'] };
function evoStatus(status) {
  const map = { live: ['Live', 'ok'], staged: ['Deploying', 'acc'], rolled_back: ['Rolled back', 'bad'] };
  const [label, cls] = map[status] || [status, ''];
  return badge(label, cls);
}

async function renderEvolution() {
  const seq = ++state.routeSeq;
  setNav('evolution');
  setCrumbs('Evolution');
  const view = document.getElementById('view');
  let data;
  try { data = await api('/evolution'); } catch (error) { view.innerHTML = errorState(error.message); return; }
  if (seq !== state.routeSeq) return;
  const info = data.info || {};
  const s = data.settings || {};
  view.innerHTML = `${pageHead('Evolution', 'It improves itself: lessons, skills, new tools, persona… and its own Go code (build + tests, commit, push, restart with automatic rollback if it crashes).', `<button class="btn" data-action="rollback" ${info.has_previous ? '' : 'disabled'} title="${info.has_previous ? '' : 'No previous version'}">${icon('undo', 'btn-icon')} Previous version</button>${info.self_repo ? `<button class="btn" data-action="self-update" data-pull="1" title="git pull ${esc(info.self_repo)}, then rebuild and restart">${icon('refresh', 'btn-icon')} Update from GitHub</button>` : ''}<button class="btn" data-action="self-update" title="Rebuild from the source in ${esc(info.source_dir || 'the container')} and restart — no image rebuild">${icon('code', 'btn-icon')} Rebuild from source</button><button class="btn primary" data-action="self-review">${icon('dna', 'btn-icon')} Run a self-review</button>`, 'Brain')}
    <section class="card" id="update-card" style="margin-bottom:16px" ${state.updateLog?.length ? '' : 'hidden'}><div class="card-head"><div><h3>⬆️ Update</h3><p id="update-status">${esc(state.updateStatus || '')}</p></div></div><div class="card-body"><div class="pre" id="update-log">${esc((state.updateLog || []).join('\n'))}</div></div></section>
    <div class="grid-2">
      <section class="card"><div class="card-head"><div><h3>Evolution log</h3><p>${fmtNum(data.items.length)} change${data.items.length === 1 ? '' : 's'}.</p></div></div>
        <div class="card-body flush">${data.items.length ? data.items.map((e) => `
          <div class="evo-row"><div class="mem-ico emoji">${(EVO_KINDS[e.kind] || ['🧬'])[0]}</div><div style="min-width:0">
            <div class="tool-name"><strong style="font-size:13px">${esc(e.summary)}</strong>${evoStatus(e.status)}</div>
            <div class="mem-meta">${badge((EVO_KINDS[e.kind] || ['', e.kind])[1], '', false)}<span>${esc(fmtDateTime(e.created_at))}</span>${e.commit_sha ? `<code>${esc(e.commit_sha)}</code>` : ''}${e.run_id ? `<a href="#/runs/${esc(e.run_id)}">run</a>` : ''}</div>
            ${e.details ? `<details class="tl-details"><summary>Details</summary><div class="pre" style="margin-top:6px">${esc(e.details)}</div></details>` : ''}
          </div></div>`).join('') : emptyState('No evolutions yet', 'Run a self-review or let it work: every improvement will be logged here.')}</div></section>
      <div class="stack" style="align-content:start">
        <section class="card"><div class="card-head"><div><h3>Its code</h3><p>A git repo it edits itself.</p></div>${info.self_built ? badge('self-built binary', 'acc') : badge('original image', '', false)}</div>
          <div class="card-body"><dl class="kv">
            <dt>Version</dt><dd><code>${esc(info.version || '—')}</code></dd>
            <dt>Commit</dt><dd><code>${esc(info.head || '—')}</code> on <code>${esc(info.branch || '—')}</code></dd>
            <dt>Repo</dt><dd>${info.self_repo ? `<a href="https://github.com/${esc(info.self_repo)}" target="_blank" rel="noopener noreferrer">${esc(info.self_repo)}</a>` : '<span class="muted">local only (SELF_REPO not set)</span>'}</dd>
            <dt>Source</dt><dd><code>${esc(info.source_dir || '')}</code></dd>
            <dt>Uncommitted changes</dt><dd>${info.dirty?.length ? `<div class="pre sm">${esc(info.dirty.join('\n'))}</div>` : '<span class="muted">none</span>'}</dd>
          </dl></div></section>
        <section class="card"><div class="card-head"><div><h3>Daily self-review</h3><p>It analyzes its errors, the 👎 and the denials, then improves.</p></div>${s.self_review ? badge('active', 'ok') : badge('disabled', '', false)}</div>
          <div class="card-body small muted">Schedule: <code>${esc(s.self_review_cron)}</code> · <a href="#/settings">edit</a></div></section>
        <section class="card"><div class="card-head"><div><h3>Git history</h3></div></div><div class="card-body">${(info.log || []).length ? info.log.map((c) => `<div class="commit-row"><code>${esc(c.sha)}</code><span title="${esc(c.subject)}">${esc(c.subject)}</span><span class="tiny muted nowrap">${esc(relative(c.date))}</span></div>`).join('') : '<div class="hint">No git repo found.</div>'}</div></section>
      </div>
    </div>`;
}

/* ---------- Schedules ---------- */
async function renderSchedules() {
  const seq = ++state.routeSeq;
  setNav('schedules');
  setCrumbs('Tasks');
  const view = document.getElementById('view');
  let data;
  try { data = await api('/schedules'); } catch (error) { view.innerHTML = errorState(error.message); return; }
  if (seq !== state.routeSeq) return;
  view.innerHTML = `${pageHead('Scheduled tasks', `Reminders and routines it runs on its own with all its tools (timezone ${esc(data.timezone)}). It also creates them when asked “remind me…” or “every Monday…”.`, `<button class="btn primary" data-action="new-schedule">${icon('plus', 'btn-icon')} New task</button>`, 'Actions')}
    <section class="card">${data.items.length ? `<div class="table-wrap"><table class="data"><thead><tr><th>Task</th><th>When</th><th>Next</th><th>Last</th><th>Where</th><th>Active</th><th></th></tr></thead><tbody>${data.items.map((t) => `
      <tr><td><div class="link-title">${esc(t.name)}</div><div class="link-target" title="${esc(t.prompt)}">${esc(t.prompt)}</div></td>
      <td>${t.cron ? `<code class="small">${esc(t.cron)}</code>` : `<span class="small">${esc(fmtDateTime(t.run_at))}</span>`}</td>
      <td class="nowrap small">${t.enabled && t.next_run_at ? esc(relative(t.next_run_at)) : '—'}</td>
      <td class="nowrap small">${t.last_run_at ? esc(relative(t.last_run_at)) : 'never'}</td>
      <td class="small">${t.channel_name ? `#${esc(t.channel_name)}` : t.channel === 'web' || !t.channel ? '<span class="muted">home channel</span>' : esc(t.channel)}</td>
      <td><label class="switch"><input type="checkbox" data-schedule-toggle="${esc(t.id)}" ${t.enabled ? 'checked' : ''} aria-label="Enable ${esc(t.name)}"><span></span></label></td>
      <td class="actions"><div class="action-row"><button class="icon-btn" data-action="run-schedule" data-id="${esc(t.id)}" aria-label="Run now" title="Run now">${icon('play')}</button><button class="icon-btn danger" data-action="delete-schedule" data-id="${esc(t.id)}" aria-label="Delete" title="Delete">${icon('trash')}</button></div></td></tr>`).join('')}</tbody></table></div>`
    : emptyState('No tasks', 'Ask it for example “every morning at 9am, give me a digest of #general”.', `<button class="btn primary" data-action="new-schedule">Create a task</button>`)}</section>
    <p class="hint" style="margin-top:12px">🧬 The daily self-review runs separately (<code>${esc(data.self_review)}</code>) — see Settings.</p>`;
}

function scheduleModal() {
  showModal(`${modalHead('New task', 'Write the prompt as if you were talking to the agent.')}
    <form id="schedule-form"><div class="modal-body"><div class="form-grid">
      <div class="field span-2"><label class="label" for="sch-name">Name</label><input class="input" id="sch-name" name="name" required placeholder="Morning digest" autofocus></div>
      <div class="field span-2"><label class="label" for="sch-prompt">Prompt</label><textarea class="textarea" id="sch-prompt" name="prompt" required placeholder="Summarize yesterday’s messages in #general and post it with 3 key points."></textarea></div>
      <div class="field"><label class="label" for="sch-cron">Recurrence (cron) <span class="hint-inline">min h day month dow</span></label><input class="input mono" id="sch-cron" name="cron" placeholder="0 9 * * 1-5"></div>
      <div class="field"><label class="label" for="sch-at">…or just once</label><input class="input" id="sch-at" name="at" type="datetime-local"></div>
      <div class="field span-2"><label class="label" for="sch-channel">Slack channel (ID) <span class="hint-inline">empty = home channel</span></label><input class="input mono" id="sch-channel" name="channel" placeholder="C0123ABCD"></div>
    </div></div><div class="modal-foot"><button class="btn" type="button" data-action="close-modal">Cancel</button><button class="btn primary" type="submit">Schedule</button></div></form>`);
}

/* ---------- Deployments ---------- */
const DEPLOY_KINDS = { cloudflare_pages: ['☁️', 'Cloudflare Pages'], cloudflare_worker: ['⚡', 'Cloudflare Worker'], docker: ['🐳', 'Docker'], github_pages: ['🐙', 'GitHub Pages'], tunnel: ['🚇', 'Cloudflare Tunnel'] };
function deployStatus(status) {
  const map = { live: ['Live', 'ok'], failed: ['Failed', 'bad'], removed: ['Removed', ''] };
  const [label, cls] = map[status] || [status, ''];
  return badge(label, cls);
}

// The Cloudflare Tunnel: health, public routes, and a form to add one.
function tunnelCard(t) {
  if (!t || !t.configured) {
    return `<section class="card" style="margin-bottom:16px"><div class="card-head"><div><h3>🚇 Cloudflare Tunnel</h3><p>${t?.error ? esc(t.error) : 'Set <code>CLOUDFLARE_API_TOKEN</code> (Tunnel: Edit, DNS: Edit, Zone: Read) and <code>CLOUDFLARE_ACCOUNT_ID</code> to publish local apps on your domains.'}</p></div></div></section>`;
  }
  if (!t.id) {
    return `<section class="card" style="margin-bottom:16px"><div class="card-head"><div><h3>🚇 Cloudflare Tunnel</h3><p>No tunnel yet. Ask it <em>“set up the Cloudflare tunnel”</em> (tunnel_setup), or set <code>CLOUDFLARE_TUNNEL_TOKEN</code>.</p></div></div></section>`;
  }
  const health = { healthy: ['healthy', 'ok'], degraded: ['degraded', 'warn'], down: ['down', 'bad'], inactive: ['no connector', 'bad'] }[t.status] || [t.status || 'unknown', ''];
  const routes = t.routes || [];
  return `<section class="card" style="margin-bottom:16px"><div class="card-head"><div><h3>🚇 Cloudflare Tunnel</h3><p>${esc(t.name || '')} <span class="mono tiny">${esc(t.id)}</span>${t.connections ? ` · ${fmtNum(t.connections)} connection${t.connections > 1 ? 's' : ''}` : ''}</p></div><div class="row">${badge(health[0], health[1])}</div></div>
    ${t.error ? `<div class="card-body"><p class="small" style="color:var(--blk)">${esc(t.error)}</p></div>` : ''}
    <form class="card-body" id="tunnel-form" style="padding-bottom:14px">
      <div class="row wrap member-add">
        <input class="input sm mono" name="hostname" placeholder="app.example.com" required style="flex:1;min-width:170px" aria-label="Public hostname">
        <input class="input sm mono" name="service" placeholder="http://localhost:3000" required style="flex:1;min-width:170px" aria-label="Local service">
        <input class="input sm mono" name="path" placeholder="path (optional)" style="width:130px" aria-label="Path">
        <button class="btn sm primary" type="submit">${icon('plus', 'btn-icon')} Publish</button>
      </div>
    </form>
    <div class="card-body flush">${routes.length ? `<div class="table-wrap"><table class="data"><thead><tr><th>Public URL</th><th>Service</th><th></th></tr></thead><tbody>${routes.map((r) => `
      <tr><td class="truncate-cell"><a href="https://${esc(r.hostname)}" target="_blank" rel="noopener noreferrer">https://${esc(r.hostname)}${esc(r.path || '')}</a></td><td class="mono small">${esc(r.service)}</td>
      <td class="actions"><button class="icon-btn danger" data-action="tunnel-remove" data-id="${esc(r.hostname)}" aria-label="Remove" title="Unpublish">${icon('trash')}</button></td></tr>`).join('')}</tbody></table></div>`
      : emptyState('No public route', 'Publish a local service above, or ask it “put my app on app.example.com”.')}</div></section>`;
}

async function renderDeployments() {
  const seq = ++state.routeSeq;
  setNav('deployments');
  setCrumbs('Deployments');
  const view = document.getElementById('view');
  let data;
  let tunnel;
  try { [data, tunnel] = await Promise.all([api('/deployments'), api('/tunnel').catch((error) => ({ error: error.message }))]); } catch (error) { view.innerHTML = errorState(error.message); return; }
  if (seq !== state.routeSeq) return;
  const items = data.items || [];
  const historyRows = data.history || [];
  view.innerHTML = `${pageHead('Deployments', 'Everything it has put live: Cloudflare Pages & Workers, Docker containers, GitHub Pages, Cloudflare Tunnel routes.', '', 'Actions')}
    ${tunnelCard(tunnel)}
    ${items.length ? `<div class="deploy-grid" style="margin-bottom:16px">${items.map((d) => `
      <section class="card deploy-card">
        <div class="top"><div class="big emoji">${(DEPLOY_KINDS[d.kind] || ['🚀'])[0]}</div><div style="min-width:0;flex:1"><h3>${esc(d.name)}</h3><div class="tiny muted">${esc((DEPLOY_KINDS[d.kind] || ['', d.kind])[1])}</div></div>${deployStatus(d.status)}</div>
        ${d.url ? `<a class="deploy-url" href="${esc(d.url)}" target="_blank" rel="noopener noreferrer">${esc(d.url)} ↗</a>` : '<span class="tiny muted">no URL</span>'}
        <div class="row between"><span class="tiny muted">${esc(relative(d.created_at))}</span>${d.run_id ? `<a class="tiny" href="#/runs/${esc(d.run_id)}">view run</a>` : ''}</div>
      </section>`).join('')}</div>` : `<section class="card" style="margin-bottom:16px">${emptyState('Nothing live yet', 'Ask it “make me a landing page and put it live” 🚀')}</section>`}
    ${historyRows.length ? `<section class="card"><div class="card-head"><div><h3>History</h3><p>Previous deployments.</p></div></div><div class="table-wrap"><table class="data"><thead><tr><th>When</th><th>Type</th><th>Name</th><th>URL</th><th>Status</th></tr></thead><tbody>${historyRows.map((d) => `<tr><td class="nowrap small">${esc(fmtDateTime(d.created_at))}</td><td class="small">${esc((DEPLOY_KINDS[d.kind] || ['', d.kind])[1])}</td><td>${esc(d.name)}</td><td class="truncate-cell small">${d.url ? `<a href="${esc(d.url)}" target="_blank" rel="noopener noreferrer">${esc(d.url)}</a>` : '—'}</td><td>${deployStatus(d.status)}</td></tr>`).join('')}</tbody></table></div></section>` : ''}`;
}

/* ---------- Mail ---------- */
async function renderMail() {
  const seq = ++state.routeSeq;
  setNav('mail');
  setCrumbs('Mailbox');
  const view = document.getElementById('view');
  let data;
  try { data = await api(`/emails?${new URLSearchParams({ dir: state.mailDir, page: state.mailPage })}`); } catch (error) { view.innerHTML = errorState(error.message); return; }
  if (seq !== state.routeSeq) return;
  const pages = Math.max(1, Math.ceil(data.total / data.page_size));
  view.innerHTML = `${pageHead('Mailbox', data.address ? `Its address: <strong>${esc(data.address)}</strong>${data.last_poll ? ` · checked ${esc(relative(data.last_poll))}` : ''}` : 'Set <code>IMAP_*</code> and <code>SMTP_*</code> to give it an address.', data.imap ? `<button class="btn" data-action="mail-refresh">${icon('refresh', 'btn-icon')} Check mail</button>` : '', 'Actions')}
    ${data.last_error ? `<div class="auth-error">⚠️ ${esc(data.last_error)}</div>` : ''}
    <div class="filter-bar"><div class="segmented" role="group" aria-label="Filter">${[['', 'All'], ['in', '📥 Received'], ['out', '📤 Sent']].map(([k, l]) => `<button class="${state.mailDir === k ? 'active' : ''}" data-action="mail-dir" data-dir="${k}">${l}</button>`).join('')}</div><span class="result-count">${fmtNum(data.total)} email${data.total > 1 ? 's' : ''}</span></div>
    <section class="card">${data.items.length ? data.items.map((e) => `
      <div class="mail-row" data-action="open-mail" data-id="${e.id}" role="button" tabindex="0"><span class="emoji" style="font-size:16px;padding-top:2px">${e.direction === 'in' ? '📥' : '📤'}</span>
        <div style="min-width:0"><strong>${esc(e.subject || '(no subject)')}</strong><div class="tiny muted">${esc(e.direction === 'in' ? `from ${e.from_addr}` : `to ${e.to_addr}`)}</div><div class="preview">${esc(e.preview)}</div></div>
        <span class="tiny muted nowrap">${esc(relative(e.created_at))}</span></div>`).join('')
      + (data.total > data.page_size ? `<div class="pagination"><span>Page ${data.page} of ${pages}</span><div class="pagination-controls"><button class="btn sm" data-action="mail-page" data-page="${data.page - 1}" ${data.page <= 1 ? 'disabled' : ''}>Previous</button><button class="btn sm" data-action="mail-page" data-page="${data.page + 1}" ${data.page >= pages ? 'disabled' : ''}>Next</button></div></div>` : '')
    : emptyState('Empty mailbox', data.imap ? 'Received mail will show up here (and it pings you in Slack).' : 'No mailbox configured.')}</section>`;
}

async function openMail(id) {
  try {
    const e = await api(`/emails/${id}`);
    showModal(`${modalHead(e.subject || '(no subject)', `${e.direction === 'in' ? `From ${e.from}` : `To ${e.to}`} · ${fmtDateTime(e.created_at)}`)}
      <div class="modal-body">${e.cc ? `<p class="tiny muted" style="margin-bottom:10px">Cc: ${esc(e.cc)}</p>` : ''}<div class="mail-body">${esc(e.body)}</div></div>
      <div class="modal-foot">${e.run_id ? `<a class="btn left" href="#/runs/${esc(e.run_id)}" data-action="close-modal">See what it did with it</a>` : ''}<button class="btn" data-action="close-modal">Close</button></div>`, 'lg');
  } catch (error) { toast(error.message, 'err'); }
}

/* ---------- Audit ---------- */
const AUDIT_ICONS = { auth: '🔑', memory: '🧠', person: '👤', tool: '🛠️', policy: '🛡️', skill: '📚', persona: '🎭', self: '🧬', schedule: '⏰', deploy: '🚀', email: '📤', github: '🐙', approval: '✋', run: '⚡', settings: '⚙️', access: '🛂', vault: '🔐' };

async function renderAudit() {
  ++state.routeSeq;
  setNav('audit');
  setCrumbs('Audit log');
  const view = document.getElementById('view');
  view.innerHTML = `${pageHead('Audit log', 'Who did what: sign-ins, decisions, deployments, emails, changes to the agent by itself or by a human.', '', 'System')}
    <div class="filter-bar"><div class="search-box">${icon('search')}<input class="input" id="audit-search" placeholder="Filter by action, actor, target…" value="${esc(state.auditSearch)}" aria-label="Filter"></div><span class="result-count" id="audit-count"></span></div>
    <section class="card" id="audit-card"><div class="card-body" style="padding:20px"><div class="skeleton" style="height:200px"></div></div></section>`;
  await loadAudit();
}

async function loadAudit() {
  const card = document.getElementById('audit-card');
  if (!card) return;
  try {
    const data = await api(`/audit?${new URLSearchParams({ page: state.auditPage, q: state.auditSearch })}`);
    const count = document.getElementById('audit-count');
    if (count) count.textContent = `${fmtNum(data.total)} entr${data.total === 1 ? 'y' : 'ies'}`;
    const pages = Math.max(1, Math.ceil(data.total / data.page_size));
    card.innerHTML = data.items.length ? data.items.map((a) => {
      const group = a.action.split('.')[0];
      const bad = /denied|delete|forget|rollback|cancel/.test(a.action);
      return `<div class="audit-row"><div class="audit-icon ${bad ? 'red' : /approved|login|deploy/.test(a.action) ? 'green' : ''}"><span class="emoji" style="font-size:13px">${AUDIT_ICONS[group] || '•'}</span></div><div class="audit-main"><div class="audit-title"><code>${esc(a.action)}</code> ${a.target ? esc(truncate(a.target, 80)) : ''}</div><div class="audit-meta"><span>${esc(a.actor || '—')}</span><span class="sep">·</span><span title="${esc(fmtDateTime(a.created_at))}">${esc(relative(a.created_at))}</span></div>${a.details && a.details !== 'null' ? `<div class="audit-details">${esc(truncate(a.details, 400))}</div>` : ''}</div></div>`;
    }).join('') + `<div class="pagination"><span>Page ${data.page} of ${pages}</span><div class="pagination-controls"><button class="btn sm" data-action="audit-page" data-page="${data.page - 1}" ${data.page <= 1 ? 'disabled' : ''}>Previous</button><button class="btn sm" data-action="audit-page" data-page="${data.page + 1}" ${data.page >= pages ? 'disabled' : ''}>Next</button></div></div>`
      : emptyState('Empty log', 'Actions will show up here.');
  } catch (error) { card.innerHTML = errorState(error.message); }
}

/* ---------- Settings ---------- */
function switchRow(name, title, text, checked) {
  return `<div class="switch-row"><div class="switch-copy"><strong>${esc(title)}</strong><span>${esc(text)}</span></div><label class="switch"><input type="checkbox" name="${name}" ${checked ? 'checked' : ''} aria-label="${esc(title)}"><span></span></label></div>`;
}

async function renderSettings() {
  const seq = ++state.routeSeq;
  setNav('settings');
  setCrumbs('Settings');
  const view = document.getElementById('view');
  let data; let vault;
  try { [data, vault] = await Promise.all([api('/settings'), api('/vault')]); } catch (error) { view.innerHTML = errorState(error.message); return; }
  if (seq !== state.routeSeq) return;
  const s = data.settings;
  const e = data.env;
  const vs = vault.status || {};
  const sources = Object.entries(vault.sources || {}).sort();
  view.innerHTML = `${pageHead('Settings', 'The settings below apply immediately. Secrets and integrations are configured through the container’s environment variables.', '', 'System')}
    <form data-settings-form="1" class="stack">
      <div class="grid-2">
        <section class="card"><div class="card-head"><div><h3>🧠 Model</h3><p>${s.api_style === 'anthropic' ? 'Anthropic' : 'OpenAI'}-compatible API · <code>${esc(s.base_url || e.llm_base_url)}</code></p></div><button class="btn sm" type="button" data-action="llm-test">${icon('spark', 'btn-icon')} Test connection</button></div><div class="card-body"><div class="form-grid">
          <div class="field span-2"><span class="label">API format</span><div class="choice-row">
            <label class="choice"><input type="radio" name="api_style" value="openai" ${s.api_style !== 'anthropic' ? 'checked' : ''}> OpenAI-like <code class="tiny muted">/chat/completions</code></label>
            <label class="choice"><input type="radio" name="api_style" value="anthropic" ${s.api_style === 'anthropic' ? 'checked' : ''}> Anthropic-like <code class="tiny muted">/messages</code></label>
          </div><span class="field-note">OpenAI-like: OpenAI, OpenRouter, Ollama, vLLM, LiteLLM… Anthropic-like: Anthropic and gateways speaking the Messages API. Embeddings always use the OpenAI format.</span></div>
          <div class="field span-2"><label class="label" for="set-base">Base URL <span class="hint-inline">empty = LLM_BASE_URL</span></label><input class="input mono" id="set-base" name="base_url" value="${esc(s.base_url)}" placeholder="${esc(e.llm_base_url)}"></div>
          <div class="span-2" id="llm-test-result" hidden></div>
          <div class="field"><label class="label" for="set-model">Main model</label><input class="input mono" id="set-model" name="model" value="${esc(s.model)}" required list="model-list" autocomplete="off"></div>
          <div class="field"><label class="label" for="set-fast">Fast model <span class="hint-inline">reflection, digests</span></label><input class="input mono" id="set-fast" name="fast_model" value="${esc(s.fast_model)}" required list="model-list" autocomplete="off"></div>
          <datalist id="model-list"></datalist>
          <div class="span-2 hint" id="model-hint">Looking up the models this endpoint offers…</div>
          <div class="field"><label class="label" for="set-temp">Temperature</label><input class="input" id="set-temp" name="temperature" type="number" min="0" max="2" step="0.05" value="${esc(s.temperature)}"></div>
          <div class="field"><label class="label" for="set-iter">Max steps per request</label><input class="input" id="set-iter" name="max_iterations" type="number" min="1" max="100" value="${esc(s.max_iterations)}"></div>
          <div class="field span-2"><label class="label" for="set-extra">Extra instructions <span class="hint-inline">added to its prompt</span></label><textarea class="textarea" id="set-extra" name="extra_prompt" placeholder="e.g. Always answer in under 5 lines in #general.">${esc(s.extra_prompt)}</textarea></div>
        </div></div></section>
        <section class="card"><div class="card-head"><div><h3>⚙️ Behavior</h3><p>What it does on its own.</p></div></div><div class="card-body">
          ${switchRow('reflection', 'Reflect after each conversation', 'Automatically extracts lasting memories and updates people notes.', s.reflection)}
          ${switchRow('follow_threads', 'Follow threads', 'Replies in threads it has already spoken in, without being mentioned again.', s.follow_threads)}
          ${switchRow('channel_digest', 'Channel digests', 'Continuously summarizes the channels it reads to keep their context.', s.channel_digest)}
          ${switchRow('email_auto', 'Handle emails on its own', 'Reads every incoming email and answers it directly, with the sender’s access profile below. Auto-replies, mailing lists and no-reply senders are skipped; max 3 replies per sender per day.', s.email_auto)}
          ${switchRow('self_review', 'Daily self-review', 'Analyzes its errors and feedback, then improves autonomously.', s.self_review)}
          <div class="form-grid" style="margin-top:12px">
            <div class="field"><label class="label" for="set-cron">Self-review schedule (cron)</label><input class="input mono" id="set-cron" name="self_review_cron" value="${esc(s.self_review_cron)}"></div>
            <div class="field"><label class="label" for="set-email-profile">Email senders’ profile</label><input class="input mono" id="set-email-profile" name="email_profile" value="${esc(s.email_profile || 'guest')}" placeholder="guest"><span class="field-note">Access profile applied to incoming emails (see Access).</span></div>
            <div class="field"><label class="label" for="set-botlimit">Bot replies per thread</label><input class="input" id="set-botlimit" name="bot_reply_limit" type="number" min="0" max="200" value="${esc(s.bot_reply_limit ?? 10)}"><span class="field-note">Other bots get an answer only if whitelisted and mentioning it; at most this many in a row per thread until a human writes (0 = ignore bots).</span></div>
            <div class="field"><label class="label" for="set-home">Home channel (Slack ID)</label><input class="input mono" id="set-home" name="home_channel" value="${esc(s.home_channel)}" placeholder="C0123ABCD"><span class="field-note">Announcements, reports, approvals from outside Slack.</span></div>
          </div>
        </div></section>
      </div>
      <div class="row" style="justify-content:flex-end"><button class="btn primary" type="submit">${icon('check', 'btn-icon')} Save settings</button></div>
    </form>
    <section class="card" style="margin-top:16px"><div class="card-head"><div><h3>🔐 Vaultwarden vault</h3><p>${vs.enabled ? `${esc(vs.url)} · folder <code>${esc(vs.folder)}</code> · ${esc(vs.auth)}` : 'Not configured: set <code>VAULT_URL</code>, <code>VAULT_CLIENT_ID</code>, <code>VAULT_CLIENT_SECRET</code> and <code>VAULT_MASTER_PASSWORD</code>.'}</p></div>${vs.enabled ? `<div class="row">${vs.unlocked ? badge('unlocked', 'ok') : badge('unavailable', 'bad')}<button class="btn sm" type="button" data-action="vault-sync">${icon('refresh', 'btn-icon')} Sync</button></div>` : ''}</div>
      ${vs.enabled ? `<div class="card-body">
        ${vs.error ? `<div class="auth-error">⚠️ ${esc(vs.error)}</div>` : ''}
        <dl class="kv" style="margin-bottom:14px"><dt>Items</dt><dd>${fmtNum(vs.items)}</dd><dt>New items go to</dt><dd>${esc(vs.writes || '—')}</dd><dt>Last sync</dt><dd>${esc(vs.last_sync ? relative(vs.last_sync) : '—')} <span class="muted tiny">(auto every 15 min)</span></dd><dt>Masked secrets</dt><dd>${fmtNum(vault.masked)} value${vault.masked === 1 ? '' : 's'} replaced with •••••• everywhere</dd></dl>
        <div class="io-label">Settings provided by the vault</div>
        ${sources.length ? `<div class="table-wrap"><table class="data"><thead><tr><th>Variable</th><th>Reference</th></tr></thead><tbody>${sources.map(([k, ref]) => `<tr><td><code class="small">${esc(k)}</code></td><td><code class="small">${esc(ref)}</code></td></tr>`).join('')}</tbody></table></div>` : `<p class="hint">None yet. Create an item in the <code>${esc(vs.folder)}</code> folder named after the variable (e.g. <code>CLOUDFLARE_API_TOKEN</code>), or put <code>CLOUDFLARE_API_TOKEN=vault://Cloudflare/api_token</code> in the <code>.env</code>.</p>`}
        <p class="hint" style="margin-top:10px">The agent never sees the values: it passes <code>vault://…</code> references that the harness substitutes, and every use requires approval.</p>
      </div>` : ''}</section>
    <section class="card" style="margin-top:16px"><div class="card-head"><div><h3>🐳 Environment</h3><p>Read-only — set by the container’s variables.</p></div></div><div class="card-body"><dl class="kv">
      <dt>Name</dt><dd>${esc(e.agent_name)} · ${esc(e.language)} · ${esc(e.timezone)}</dd>
      <dt>Public URL</dt><dd><code>${esc(e.public_url)}</code></dd>
      <dt>Model context</dt><dd>${fmtNum(e.llm_context)} tokens · ${fmtNum(e.llm_max_tokens)} max output</dd>
      <dt>Embeddings</dt><dd>${e.embed_model ? `<code>${esc(e.embed_model)}</code>` : '<span class="muted">disabled — keyword-only memory</span>'}</dd>
      <dt>Slack admins</dt><dd>${e.slack_admins?.length ? e.slack_admins.map((a) => `<code>${esc(a)}</code>`).join(' ') : '<span class="muted">none — manage access on the Access page</span>'}</dd>
      <dt>Email</dt><dd>${esc(e.email || '—')}</dd>
      <dt>Its repo</dt><dd>${esc(e.self_repo || '—')}</dd>
      <dt>Apps domain</dt><dd>${esc(e.deploy_domain || '—')}</dd>
      <dt>Directories</dt><dd><code>${esc(e.workspace)}</code> · <code>${esc(e.source_dir)}</code> · <code>${esc(e.data_dir)}</code></dd>
      <dt>Version</dt><dd><code>${esc(e.version)}</code></dd>
    </dl></div></section>`;
  loadModels();
}

async function saveSettings(form, quiet = false) {
  const fd = new FormData(form);
  const body = {};
  for (const key of ['api_style', 'base_url', 'model', 'fast_model', 'temperature', 'max_iterations', 'extra_prompt', 'self_review_cron', 'email_profile', 'home_channel', 'bot_reply_limit']) body[key] = String(fd.get(key) ?? '').trim();
  for (const key of ['reflection', 'follow_threads', 'channel_digest', 'email_auto', 'self_review']) body[key] = form.querySelector(`[name="${key}"]`).checked;
  try {
    await api('/settings', { method: 'PATCH', body });
    if (!quiet) toast('Settings saved ✨');
    refreshOverview();
    loadModels();
    return true;
  } catch (error) { toast(error.message, 'err'); return false; }
}

// Fills the model inputs' suggestions with what the endpoint offers, and
// warns when the configured model is not among them.
async function loadModels() {
  const hint = document.getElementById('model-hint');
  const list = document.getElementById('model-list');
  if (!hint || !list) return;
  try {
    const data = await api('/llm/models');
    const ids = data.items || [];
    list.innerHTML = ids.map((id) => `<option value="${esc(id)}"></option>`).join('');
    const current = document.getElementById('set-model')?.value.trim();
    if (!ids.length) { hint.textContent = 'This endpoint does not list its models — type the exact model name.'; return; }
    const known = ids.includes(current);
    hint.innerHTML = `${known ? '✅' : '⚠️'} ${fmtNum(ids.length)} model${ids.length > 1 ? 's' : ''} available on this endpoint${known ? '' : ` — <strong>${esc(current)}</strong> is not one of them`}. Start typing in a model field to pick one.`;
  } catch (error) {
    hint.textContent = `Couldn't list models (${error.message}). Type the exact model name, then use Test connection.`;
  }
}

async function testModel(button) {
  const form = button.closest('form');
  const out = document.getElementById('llm-test-result');
  button.disabled = true;
  const label = button.innerHTML;
  button.textContent = 'Testing…';
  try {
    if (!(await saveSettings(form, true))) return;
    const res = await api('/llm/test', { method: 'POST' });
    if (out) {
      out.hidden = false;
      out.innerHTML = res.ok
        ? `<div class="advanced-note">✅ <strong>${esc(res.model)}</strong> answered “${esc(res.reply)}” in ${fmtMs(res.latency_ms)} (${esc(res.api_style)} API). Settings saved.</div>`
        : `<div class="auth-error" style="margin:0">❌ <strong>${esc(res.model)}</strong> (${esc(res.api_style)} API): ${esc(res.error)}</div>`;
    }
  } catch (error) {
    toast(error.message, 'err');
  } finally {
    button.disabled = false;
    button.innerHTML = label;
  }
}

/* ---------- Access ---------- */
const MEMORY_MODES = { full: ['🧠', 'Full', 'reads and remembers'], read: ['👀', 'Read-only', 'relies on memory without remembering anything'], none: ['🫥', 'None', 'no recall, no memorizing'] };
const APPROVE_MODES = { any: 'Approves anything', own: 'Approves own requests', none: 'Approves nothing' };

async function renderAccess() {
  const seq = ++state.routeSeq;
  setNav('access');
  setCrumbs('Access');
  const view = document.getElementById('view');
  let data;
  try { data = await api('/access'); } catch (error) { view.innerHTML = errorState(error.message); return; }
  if (seq !== state.routeSeq) return;
  state.cache.access = data;
  const name = state.status?.agentName || 'BLAVINGAD';
  const profiles = data.profiles.map((p) => ({ ...p.profile, resolved: p.resolved }));
  const byName = Object.fromEntries(profiles.map((p) => [p.name, p]));
  const def = byName[data.default];
  const profileOptions = (selected, allowAdmin = true) => profiles.filter((p) => allowAdmin || p.name !== 'admin').map((p) => `<option value="${esc(p.name)}" ${p.name === selected ? 'selected' : ''}>${esc(p.emoji)} ${esc(p.label)}</option>`).join('');
  view.innerHTML = `${pageHead('Access', `Who can talk to ${esc(name)}, and with what powers. People not on the allowlist get the default profile. You can also just ask it: <em>“give @alice the member profile”</em>.`, `<button class="btn primary" data-action="new-profile">${icon('plus', 'btn-icon')} New profile</button>`, 'System')}
    <div class="grid-2" style="margin-bottom:16px">
      <section class="card"><div class="card-head"><div><h3>✅ Allowlist</h3><p>${fmtNum(data.members.length)} ${data.members.length === 1 ? 'person' : 'people'} with explicit access.</p></div></div>
        <form class="card-body" id="member-form" style="padding-bottom:14px">
          <div class="row wrap member-add">
            <input class="input sm mono" name="user" list="people-list" placeholder="Slack ID or name" required style="flex:1;min-width:170px" aria-label="Person">
            <datalist id="people-list">${(data.people || []).map((p) => `<option value="${esc(p.slack_id)}">${esc(p.name)}</option>`).join('')}</datalist>
            <select class="select sm" name="profile" style="width:auto" aria-label="Profile">${profileOptions('member')}</select>
            <input class="input sm" name="note" placeholder="Note (optional)" style="flex:1;min-width:120px" aria-label="Note">
            <button class="btn sm primary" type="submit">${icon('plus', 'btn-icon')} Add</button>
          </div>
        </form>
        <div class="card-body flush">${data.members.length ? `<div class="table-wrap"><table class="data"><thead><tr><th>Person</th><th>Profile</th><th>Note</th><th></th></tr></thead><tbody>${data.members.map((m) => `
          <tr><td><div class="link-ident"><div class="avatar">${esc(initials(m.name || m.slack_id))}</div><div><div class="link-title">${esc(m.name || m.slack_id)}</div><div class="link-target mono">${esc(m.slack_id)}${m.title ? ` · ${esc(m.title)}` : ''}</div></div></div></td>
          <td>${m.env_admin ? badge('👑 Admin', 'acc', false) : `<select class="select sm" data-member-profile="${esc(m.slack_id)}" style="width:auto;min-width:130px" aria-label="Profile of ${esc(m.name || m.slack_id)}">${profileOptions(m.profile)}</select>`}</td>
          <td class="small muted"><div class="truncate-cell">${esc(m.note || '—')}</div>${m.added_by ? `<div class="tiny">by ${esc(m.added_by)}</div>` : ''}</td>
          <td class="actions">${m.env_admin ? `<span class="tiny muted" title="Set by the SLACK_ADMINS variable">🔒 env</span>` : `<button class="icon-btn danger" data-action="remove-member" data-id="${esc(m.slack_id)}" aria-label="Remove" title="Remove from the allowlist">${icon('trash')}</button>`}</td></tr>`).join('')}</tbody></table></div>`
          : emptyState('Empty allowlist', 'Everyone gets the default profile. Add your team members above.')}</div></section>
      <section class="card"><div class="card-head"><div><h3>🚪 Everyone else</h3><p>The profile applied to people not on the allowlist.</p></div></div>
        <div class="card-body">
          <div class="field"><label class="label" for="access-default">Default profile</label><select class="select" id="access-default">${profileOptions(data.default, false)}</select></div>
          ${def ? `<div class="advanced-note" style="margin-top:12px"><strong>${esc(def.emoji)} ${esc(def.label)}</strong> — ${esc(def.description)}<br>${def.reply ? `${fmtNum(def.resolved.length)} tools · memory: ${esc(MEMORY_MODES[def.memory]?.[1] || def.memory).toLowerCase()} · ${esc(APPROVE_MODES[def.approve] || '').toLowerCase()}` : '🙈 These people are ignored: it reads their messages but doesn’t reply.'}</div>` : ''}
          <p class="hint" style="margin-top:12px">💡 Pick <strong>🚫 Blocked</strong> so it only replies to the allowlist. Admins in <code>SLACK_ADMINS</code> are always 👑 Admin.</p>
        </div></section>
    </div>
    <div class="deploy-grid">${profiles.map((p) => `
      <section class="card deploy-card">
        <div class="top"><div class="big emoji">${esc(p.emoji || '•')}</div><div style="min-width:0;flex:1"><h3>${esc(p.label)}</h3><div class="tiny muted mono">${esc(p.name)}${p.name === data.default ? ' · default' : ''}</div></div>${badge(`${fmtNum(p.members)} member${p.members === 1 ? '' : 's'}`, '', false)}</div>
        <p class="small muted">${esc(p.description || '—')}</p>
        <div class="row wrap" style="gap:5px">${p.reply ? `${badge(`${MEMORY_MODES[p.memory]?.[0] || ''} memory: ${(MEMORY_MODES[p.memory]?.[1] || p.memory).toLowerCase()}`, p.memory === 'full' ? 'ok' : p.memory === 'read' ? 'info' : '', false)}${badge(APPROVE_MODES[p.approve] || p.approve, p.approve === 'any' ? 'acc' : '', false)}${p.max_iterations ? badge(`≤ ${p.max_iterations} steps`, '', false) : ''}` : badge('🙈 ignored', 'bad', false)}</div>
        ${p.reply ? `<div class="tiny muted">${p.resolved.length === state.cache.access.tools.length ? 'All tools' : `${fmtNum(p.resolved.length)} tool${p.resolved.length === 1 ? '' : 's'}: ${esc(p.resolved.slice(0, 8).join(', '))}${p.resolved.length > 8 ? ` +${p.resolved.length - 8}` : ''}`}</div>` : ''}
        <div class="row" style="margin-top:2px"><button class="btn xs" data-action="edit-profile" data-name="${esc(p.name)}">${icon('edit', 'btn-icon')} Edit</button>${p.builtin ? '<span class="tiny muted">🔒 built-in</span>' : `<button class="btn xs ghost" data-action="delete-profile" data-name="${esc(p.name)}">${icon('trash', 'btn-icon')} Delete</button>`}</div>
      </section>`).join('')}</div>`;
}

function profileModal(name = '') {
  const data = state.cache.access;
  const p = data.profiles.map((x) => ({ ...x.profile, resolved: x.resolved })).find((x) => x.name === name)
    || { name: '', label: '', emoji: '🙂', description: '', tools: ['cat:web'], memory: 'none', reply: true, approve: 'none', max_iterations: 0, extra_prompt: '', resolved: data.tools.filter((t) => t.category === 'web').map((t) => t.name) };
  const isAdmin = p.name === 'admin';
  const allowed = new Set(p.resolved);
  const groups = {};
  for (const t of data.tools) (groups[t.category] ||= []).push(t);
  const cats = Object.keys(CATEGORIES).filter((k) => groups[k]).concat(Object.keys(groups).filter((k) => !CATEGORIES[k]));
  showModal(`${modalHead(name ? `Profile ${p.emoji} ${p.label}` : 'New access profile', 'What people with this profile can do.')}
    <form id="profile-form" data-editing="${esc(name)}"><div class="modal-body"><div class="form-grid">
      <div class="field"><label class="label" for="pf-name">Identifier</label><input class="input mono" id="pf-name" name="name" required pattern="[a-z][a-z0-9-]{1,30}" value="${esc(p.name)}" placeholder="client" ${name ? 'readonly' : 'autofocus'}></div>
      <div class="field"><label class="label" for="pf-label">Display name</label><div class="row"><input class="input" name="emoji" value="${esc(p.emoji)}" style="width:58px;text-align:center" aria-label="Emoji"><input class="input" id="pf-label" name="label" required value="${esc(p.label)}" placeholder="Client" ${name ? 'autofocus' : ''}></div></div>
      <div class="field span-2"><label class="label" for="pf-desc">Description</label><input class="input" id="pf-desc" name="description" value="${esc(p.description)}" placeholder="Our customers: product questions, no internal access."></div>
      ${isAdmin ? '<div class="span-2 advanced-note">🔒 The admin profile always keeps every tool, full memory and the right to approve anything. Only its name, emoji, description and instructions can be edited.</div>' : `
      <div class="field span-2"><span class="label">Memory</span><div class="choice-row">${Object.entries(MEMORY_MODES).map(([k, [e, l, h]]) => `<label class="choice" title="${esc(h)}"><input type="radio" name="memory" value="${k}" ${p.memory === k ? 'checked' : ''}> ${e} ${l}</label>`).join('')}</div><span class="field-note">Unless “full”, memory-writing tools are removed automatically (and reading too with “none”).</span></div>
      <div class="field"><label class="label" for="pf-approve">Approvals</label><select class="select" id="pf-approve" name="approve">${Object.entries(APPROVE_MODES).map(([k, l]) => `<option value="${k}" ${p.approve === k ? 'selected' : ''}>${l}</option>`).join('')}</select></div>
      <div class="field"><label class="label" for="pf-iter">Max steps <span class="hint-inline">0 = global setting</span></label><input class="input" id="pf-iter" name="max_iterations" type="number" min="0" max="100" value="${esc(p.max_iterations)}"></div>
      <div class="span-2">${switchRow('reply', 'Reply to them', 'Off: these people are ignored (their messages are still read).', p.reply)}</div>
      <div class="field span-2"><div class="label">Allowed tools <span class="hint-inline" id="pf-count"></span></div>
        <div class="stack-sm">${cats.map((c) => {
          const list = groups[c];
          const all = list.every((t) => allowed.has(t.name));
          return `<div class="rule-card" style="gap:8px"><label class="check-row" style="font-weight:600"><input type="checkbox" data-cat-toggle="${esc(c)}" ${all ? 'checked' : ''}> <span class="emoji">${(CATEGORIES[c] || ['🔧'])[0]}</span> ${esc((CATEGORIES[c] || ['', c])[1])} <span class="tiny muted" style="font-weight:400">— whole category</span></label>
            <div class="row wrap" style="gap:4px 14px;padding-left:23px">${list.map((t) => `<label class="check-row" title="${t.available ? '' : 'Integration not configured'}"><input type="checkbox" name="tool" value="${esc(t.name)}" data-cat="${esc(c)}" ${allowed.has(t.name) ? 'checked' : ''}> <code class="small${t.available ? '' : ' muted'}">${esc(t.name)}</code>${t.sensitive ? ' <span title="Sensitive: approval required by default">⚠️</span>' : ''}</label>`).join('')}</div></div>`;
        }).join('')}</div></div>`}
      <div class="field span-2"><label class="label" for="pf-extra">Instructions for this profile <span class="hint-inline">added to its prompt</span></label><textarea class="textarea" id="pf-extra" name="extra_prompt" placeholder="e.g. Only answer questions about our public product.">${esc(p.extra_prompt)}</textarea></div>
    </div></div>
    <div class="modal-foot"><button class="btn" type="button" data-action="close-modal">Cancel</button><button class="btn primary" type="submit">Save</button></div></form>`, 'lg');
  updateToolCount();
}

function updateToolCount() {
  const form = document.getElementById('profile-form');
  const node = document.getElementById('pf-count');
  if (!form || !node) return;
  const boxes = [...form.querySelectorAll('input[name="tool"]')];
  node.textContent = `${boxes.filter((b) => b.checked).length} / ${boxes.length}`;
  form.querySelectorAll('[data-cat-toggle]').forEach((cat) => {
    const inCat = boxes.filter((b) => b.dataset.cat === cat.dataset.catToggle);
    const on = inCat.filter((b) => b.checked).length;
    cat.checked = on === inCat.length;
    cat.indeterminate = on > 0 && on < inCat.length;
  });
}

// Compact the checkbox state into tool tokens: "*", "cat:x" or tool names.
function toolTokens(form) {
  const boxes = [...form.querySelectorAll('input[name="tool"]')];
  if (boxes.every((b) => b.checked)) return ['*'];
  const tokens = [];
  const cats = [...new Set(boxes.map((b) => b.dataset.cat))];
  for (const c of cats) {
    const inCat = boxes.filter((b) => b.dataset.cat === c);
    if (inCat.every((b) => b.checked)) tokens.push(`cat:${c}`);
    else inCat.filter((b) => b.checked).forEach((b) => tokens.push(b.value));
  }
  return tokens;
}

async function saveProfile(form) {
  const fd = new FormData(form);
  const editing = form.dataset.editing;
  const body = {
    name: String(fd.get('name') || '').trim(),
    label: String(fd.get('label') || '').trim(),
    emoji: String(fd.get('emoji') || '').trim(),
    description: String(fd.get('description') || '').trim(),
    extra_prompt: String(fd.get('extra_prompt') || ''),
  };
  if (editing === 'admin') Object.assign(body, { tools: ['*'], memory: 'full', reply: true, approve: 'any', max_iterations: 0 });
  else Object.assign(body, {
    tools: toolTokens(form), memory: fd.get('memory') || 'none', reply: form.querySelector('[name="reply"]').checked,
    approve: fd.get('approve') || 'none', max_iterations: Number(fd.get('max_iterations')) || 0,
  });
  if (!editing && state.cache.access.profiles.some((x) => x.profile.name === body.name)) throw new Error('This profile name already exists.');
  await api('/access/profiles', { method: 'POST', body });
  closeModal();
  toast(`${body.emoji} Profile “${body.label}” saved`);
  await renderAccess();
}

/* ---------- Events ---------- */
function personModal(id) {
  const p = (state.cache.people || []).find((x) => x.slack_id === id);
  if (!p) return;
  showModal(`${modalHead(p.real_name || p.name || p.slack_id, 'Their notes are recalled automatically when this person talks to it.')}
    <form id="person-form" data-id="${esc(p.slack_id)}"><div class="modal-body"><div class="field"><label class="label" for="person-notes">Notes</label><textarea class="textarea" id="person-notes" name="notes" style="min-height:220px" autofocus>${esc(p.notes)}</textarea></div></div>
    <div class="modal-foot"><button class="btn" type="button" data-action="close-modal">Cancel</button><button class="btn primary" type="submit">Save</button></div></form>`);
}

document.addEventListener('click', async (event) => {
  const row = event.target.closest('tr[data-href]');
  if (row && !event.target.closest('a, button, input, label')) { location.hash = row.dataset.href; return; }
  const target = event.target.closest('[data-action]');
  if (!target) return;
  const action = target.dataset.action;
  if (action === 'close-modal' && target.classList.contains('modal-backdrop') && event.target !== target) return;
  const { id, name } = target.dataset;
  try {
    switch (action) {
      case 'close-modal': closeModal(); break;
      case 'confirm-run': { const handler = state.confirmHandler; closeModal(); if (handler) await handler(); break; }
      case 'reload': route(); break;
      case 'reload-page': location.reload(); break;
      case 'logout': await api('/auth/logout', { method: 'POST' }); location.href = '/'; break;
      case 'toggle-nav': document.body.classList.toggle('nav-open'); break;
      case 'close-nav': document.body.classList.remove('nav-open'); break;
      case 'copy-text': await copyText(target.dataset.text || ''); break;
      case 'chat-threads': document.getElementById('chat-shell')?.classList.toggle('show-threads'); break;
      case 'chat-prompt': await sendChat(target.dataset.prompt || ''); break;
      case 'cancel-run': await api(`/runs/${encodeURIComponent(id)}/cancel`, { method: 'POST' }); toast('🛑 Stop requested'); break;
      case 'runs-page': state.runsPage = Number(target.dataset.page) || 1; await loadRuns(); window.scrollTo({ top: 0 }); break;
      case 'decide':
        target.disabled = true;
        await api(`/approvals/${encodeURIComponent(id)}`, { method: 'POST', body: { decision: target.dataset.decision } });
        toast(target.dataset.decision === 'deny' ? '❌ Denied' : '✅ Approved');
        await renderApprovals();
        break;
      case 'mem-tab': state.memTab = target.dataset.tab; await renderMemory(); break;
      case 'mem-kind': state.memKind = target.dataset.kind; state.memPage = 1; await loadMemories(); break;
      case 'mem-page': state.memPage = Number(target.dataset.page) || 1; await loadMemories(); window.scrollTo({ top: 0 }); break;
      case 'new-memory': memoryModal(); break;
      case 'edit-memory': memoryModal((state.cache.memories || []).find((m) => String(m.id) === id)); break;
      case 'delete-memory':
        confirmDialog({ title: `Forget memory #${id}?`, message: 'It won’t remember it at all anymore.', confirm: 'Forget', onConfirm: async () => { await api(`/memories/${id}`, { method: 'DELETE' }); toast('🫥 Memory forgotten'); await loadMemories(); } });
        break;
      case 'edit-person': personModal(id); break;
      case 'digest':
        target.disabled = true; target.textContent = 'Summarizing…';
        await api(`/channels/${encodeURIComponent(id)}/digest`, { method: 'POST' });
        toast('Digest updated'); await loadChannels();
        break;
      case 'tool-policy':
        await api(`/tools/${encodeURIComponent(name)}`, { method: 'PATCH', body: { policy: target.dataset.policy } });
        toast(`${name} → ${({ auto: 'auto', approve: 'approval required', off: 'disabled' })[target.dataset.policy]}`);
        await renderTools();
        break;
      case 'view-tool': toolCodeModal(name); break;
      case 'delete-tool':
        confirmDialog({ title: `Delete the tool “${name}”?`, message: 'It disappears from its capabilities immediately.', confirm: 'Delete', onConfirm: async () => { await api(`/tools/${encodeURIComponent(name)}`, { method: 'DELETE' }); toast('Tool deleted'); await renderTools(); } });
        break;
      case 'new-skill': skillModal(); break;
      case 'edit-skill': skillModal((state.cache.skills || []).find((s) => s.name === name)); break;
      case 'delete-skill':
        confirmDialog({ title: `Delete “${name}”?`, message: 'The procedure will be lost for good.', confirm: 'Delete', onConfirm: async () => { await api(`/skills/${encodeURIComponent(name)}`, { method: 'DELETE' }); toast('Skill deleted'); await renderSkills(); } });
        break;
      case 'persona-history': personaHistoryModal(); break;
      case 'persona-reset': {
        const area = document.getElementById('persona-content');
        if (area) area.value = state.cache.persona?.default || '';
        toast('Original persona loaded — save to apply it.');
        break;
      }
      case 'persona-restore': {
        const v = (state.cache.persona?.versions || []).find((x) => String(x.id) === id);
        if (v) { await api('/persona', { method: 'PUT', body: { content: v.content, reason: `restored version #${v.id}` } }); closeModal(); toast('🎭 Version restored'); await renderSkills(); }
        break;
      }
      case 'rollback':
        confirmDialog({ title: 'Go back to the previous version?', message: 'It restores its previous binary, reverts its last commit and restarts.', confirm: 'Roll back', onConfirm: async () => { await api('/evolution/rollback', { method: 'POST' }); toast('⏪ Rolling back, restarting…'); } });
        break;
      case 'self-update':
        state.updateLog = [];
        state.updateStatus = 'Starting…';
        await api('/evolution/update', { method: 'POST', body: { pull: target.dataset.pull === '1', force: target.dataset.pull !== '1' } });
        toast(target.dataset.pull === '1' ? '⬇️ Pulling and rebuilding…' : '🏗️ Rebuilding from source…');
        await renderEvolution();
        break;
      case 'self-review':
        await api('/evolution/review', { method: 'POST' });
        toast('🧬 Self-review started — follow it in Runs');
        break;
      case 'new-schedule': scheduleModal(); break;
      case 'run-schedule': await api(`/schedules/${encodeURIComponent(id)}/run`, { method: 'POST' }); toast('⏰ Task started'); break;
      case 'delete-schedule':
        confirmDialog({ title: 'Delete this task?', message: 'It won’t run anymore.', confirm: 'Delete', onConfirm: async () => { await api(`/schedules/${encodeURIComponent(id)}`, { method: 'DELETE' }); toast('Task deleted'); await renderSchedules(); } });
        break;
      case 'mail-refresh': { target.disabled = true; const res = await api('/emails/refresh', { method: 'POST' }); toast(res.received ? `📬 ${res.received} new email${res.received === 1 ? '' : 's'}` : 'No new email'); await renderMail(); break; }
      case 'mail-dir': state.mailDir = target.dataset.dir; state.mailPage = 1; await renderMail(); break;
      case 'mail-page': state.mailPage = Number(target.dataset.page) || 1; await renderMail(); break;
      case 'open-mail': await openMail(id); break;
      case 'new-profile': profileModal(); break;
      case 'edit-profile': profileModal(name); break;
      case 'delete-profile':
        confirmDialog({ title: `Delete the profile “${name}”?`, message: 'Its members fall back to the default profile.', confirm: 'Delete', onConfirm: async () => { await api(`/access/profiles/${encodeURIComponent(name)}`, { method: 'DELETE' }); toast('Profile deleted'); await renderAccess(); } });
        break;
      case 'remove-member':
        confirmDialog({ title: 'Remove from the allowlist?', message: `This person will get the default profile (${state.cache.access?.default || 'guest'}).`, confirm: 'Remove', onConfirm: async () => { await api(`/access/members/${encodeURIComponent(id)}`, { method: 'DELETE' }); toast('Removed from the allowlist'); await renderAccess(); } });
        break;
      case 'llm-test': await testModel(target); break;
      case 'tunnel-remove':
        confirmDialog({ title: `Unpublish ${id}?`, message: 'The route and its DNS record (if it points to the tunnel) are deleted.', confirm: 'Unpublish', onConfirm: async () => { const res = await api(`/tunnel/routes?${new URLSearchParams({ hostname: id })}`, { method: 'DELETE' }); toast(`🚇 ${res.message}`); await renderDeployments(); } });
        break;
      case 'vault-sync': {
        target.disabled = true;
        const res = await api('/vault/sync', { method: 'POST' });
        toast(res.restarting ? `🔐 ${res.changed.join(', ')} changed: restarting…` : '🔐 Vault synced, nothing changed');
        if (!res.restarting) await renderSettings();
        break;
      }
      case 'audit-page': state.auditPage = Number(target.dataset.page) || 1; await loadAudit(); window.scrollTo({ top: 0 }); break;
      default: break;
    }
  } catch (error) {
    toast(error.message || 'Something went wrong.', 'err');
    if (target.disabled !== undefined) target.disabled = false;
  }
});

function debounce(fn, delay = 280) {
  clearTimeout(state.searchTimer);
  state.searchTimer = setTimeout(fn, delay);
}

document.addEventListener('input', (event) => {
  const t = event.target;
  if (t.id === 'runs-search') { state.runsSearch = t.value; state.runsPage = 1; debounce(loadRuns); }
  else if (t.id === 'mem-search') { state.memSearch = t.value; state.memPage = 1; debounce(loadMemories, state.memSemantic ? 500 : 280); }
  else if (t.id === 'audit-search') { state.auditSearch = t.value; state.auditPage = 1; debounce(loadAudit); }
  else if (t.id === 'chat-input') { t.style.height = 'auto'; t.style.height = `${Math.min(220, t.scrollHeight + 2)}px`; }
});

document.addEventListener('change', async (event) => {
  const t = event.target;
  try {
    if (t.id === 'runs-source') { state.runsSource = t.value; state.runsPage = 1; await loadRuns(); }
    else if (t.id === 'runs-status') { state.runsStatus = t.value; state.runsPage = 1; await loadRuns(); }
    else if (t.id === 'mem-semantic') { state.memSemantic = t.checked; state.memPage = 1; await loadMemories(); }
    else if (t.id === 'access-default') { await api('/access/default', { method: 'PUT', body: { profile: t.value } }); toast('🚪 Default profile updated'); await renderAccess(); }
    else if (t.dataset.memberProfile) { await api(`/access/members/${encodeURIComponent(t.dataset.memberProfile)}`, { method: 'PUT', body: { profile: t.value } }); toast('Access updated'); await renderAccess(); }
    else if (t.dataset.catToggle) { t.form.querySelectorAll(`input[name="tool"][data-cat="${CSS.escape(t.dataset.catToggle)}"]`).forEach((b) => { b.checked = t.checked; }); updateToolCount(); }
    else if (t.name === 'tool' && t.form?.id === 'profile-form') { updateToolCount(); }
    else if (t.dataset.skillToggle) { await api(`/skills/${encodeURIComponent(t.dataset.skillToggle)}`, { method: 'PATCH', body: { enabled: t.checked } }); toast(t.checked ? 'Skill enabled' : 'Skill disabled'); }
    else if (t.dataset.scheduleToggle) {
      t.disabled = true;
      try { await api(`/schedules/${encodeURIComponent(t.dataset.scheduleToggle)}`, { method: 'PATCH', body: { enabled: t.checked } }); await renderSchedules(); }
      catch (error) { t.checked = !t.checked; throw error; } finally { t.disabled = false; }
    }
  } catch (error) { toast(error.message, 'err'); }
});

document.addEventListener('submit', async (event) => {
  const form = event.target;
  const fd = new FormData(form);
  const submit = form.querySelector('button[type="submit"]');
  const run = async (fn) => {
    event.preventDefault();
    if (submit) submit.disabled = true;
    try { await fn(); } catch (error) { toast(error.message, 'err'); } finally { if (submit) submit.disabled = false; }
  };
  if (form.id === 'login-form') {
    await run(async () => { await api('/auth/password', { method: 'POST', body: { password: fd.get('password') } }); location.reload(); });
  } else if (form.id === 'chat-form') {
    await run(() => sendChat(String(fd.get('message') || '')));
  } else if (form.id === 'memory-form') {
    await run(async () => {
      const body = { content: fd.get('content'), kind: fd.get('kind'), importance: Number(fd.get('importance')), subject: fd.get('subject'), tags: fd.get('tags') };
      if (form.dataset.id) await api(`/memories/${form.dataset.id}`, { method: 'PATCH', body });
      else { const res = await api('/memories', { method: 'POST', body }); if (res.merged) toast('Merged with a very similar memory'); }
      closeModal(); toast('🧠 Remembered'); await loadMemories();
    });
  } else if (form.id === 'person-form') {
    await run(async () => { await api(`/people/${encodeURIComponent(form.dataset.id)}`, { method: 'PATCH', body: { notes: fd.get('notes') } }); closeModal(); toast('Notes updated'); await loadPeople(); });
  } else if (form.id === 'skill-form') {
    await run(async () => { await api('/skills', { method: 'POST', body: { name: fd.get('name'), description: fd.get('description'), content: fd.get('content') } }); closeModal(); toast('📚 Skill saved'); await renderSkills(); });
  } else if (form.id === 'persona-form') {
    await run(async () => { await api('/persona', { method: 'PUT', body: { content: fd.get('content'), reason: fd.get('reason') } }); toast('🎭 Persona updated'); await renderSkills(); });
  } else if (form.id === 'schedule-form') {
    await run(async () => {
      const at = String(fd.get('at') || '');
      await api('/schedules', { method: 'POST', body: { name: fd.get('name'), prompt: fd.get('prompt'), cron: String(fd.get('cron') || '').trim(), at, channel: String(fd.get('channel') || '').trim() } });
      closeModal(); toast('⏰ Task scheduled'); await renderSchedules();
    });
  } else if (form.id === 'member-form') {
    await run(async () => {
      let user = String(fd.get('user') || '').trim();
      const match = (state.cache.access?.people || []).find((p) => p.name && p.name.toLowerCase() === user.toLowerCase());
      if (match) user = match.slack_id;
      await api(`/access/members/${encodeURIComponent(user)}`, { method: 'PUT', body: { profile: fd.get('profile'), note: fd.get('note') } });
      toast('✅ Added to the allowlist'); await renderAccess();
    });
  } else if (form.id === 'tunnel-form') {
    await run(async () => {
      const res = await api('/tunnel/routes', { method: 'POST', body: { hostname: fd.get('hostname'), service: fd.get('service'), path: fd.get('path') } });
      toast(`🚇 ${res.message}`); await renderDeployments();
    });
  } else if (form.id === 'profile-form') {
    await run(() => saveProfile(form));
  } else if (form.dataset.settingsForm) {
    await run(() => saveSettings(form));
  }
});

document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape') {
    if (modalRoot.children.length) closeModal();
    else document.body.classList.remove('nav-open');
  } else if (event.key === 'Enter' && event.target.id === 'chat-input' && !event.shiftKey && !event.isComposing) {
    event.preventDefault();
    event.target.form?.requestSubmit();
  } else if ((event.key === 'Enter' || event.key === ' ') && event.target.classList?.contains('mail-row')) {
    event.preventDefault();
    openMail(event.target.dataset.id);
  }
});

window.addEventListener('hashchange', () => route());
window.addEventListener('resize', () => {
  if (window.innerWidth > 900) document.body.classList.remove('nav-open');
  fitCharts();
});

/* ---------- Router ---------- */
async function route() {
  if (!state.user) return;
  document.body.classList.remove('nav-open');
  closeModal();
  window.scrollTo({ top: 0, behavior: 'instant' });
  const parts = (location.hash.replace(/^#/, '') || '/').split('/').filter(Boolean);
  const [page, arg] = parts;
  switch (page) {
    case undefined: return renderDashboard();
    case 'chat': return renderChat(arg);
    case 'runs': return arg ? renderRun(arg) : renderRuns();
    case 'approvals': return renderApprovals();
    case 'memory': return renderMemory();
    case 'tools': return renderTools();
    case 'skills': return renderSkills();
    case 'evolution': return renderEvolution();
    case 'schedules': return renderSchedules();
    case 'deployments': return renderDeployments();
    case 'mail': return renderMail();
    case 'access': return renderAccess();
    case 'audit': return renderAudit();
    case 'settings': return renderSettings();
    default: {
      setNav('');
      setCrumbs('Not found');
      document.getElementById('view').innerHTML = emptyState('Page not found', 'This page doesn’t exist.', '<a class="btn" href="#/">Back to home</a>');
    }
  }
}

/* ---------- Boot ---------- */
async function init() {
  try {
    const response = await fetch('/api/auth/status', { credentials: 'same-origin' });
    if (!response.ok) throw new Error('Could not reach the API.');
    state.status = await response.json();
    if (!state.status.authenticated) { renderLogin(); return; }
    state.user = state.status.user;
    renderShell();
    connectEvents();
    if (!location.hash) history.replaceState({}, '', '#/');
    if (location.hash !== '#/') refreshOverview();
    await route();
  } catch (error) {
    app.innerHTML = `<div style="min-height:100svh;display:grid;place-items:center;padding:24px"><div class="card" style="max-width:480px;padding:28px"><div class="eyebrow">Connection error</div><h2 style="margin-bottom:8px">Could not start</h2><p class="muted" style="font-size:13px;margin-bottom:16px">${esc(error.message)}</p><button class="btn primary" data-action="reload-page">Reload</button></div></div>`;
  }
}

init();
