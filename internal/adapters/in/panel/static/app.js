// Barnraise panel. Plain ES module, no build step. Every text that comes
// from the server is written with textContent (through h()), never as HTML.
import { t, tm, lang } from './i18n.js';

const $ = (sel) => document.querySelector(sel);
const LS = {
  get(k) { try { return localStorage.getItem('barnraise.' + k) || ''; } catch { return ''; } },
  set(k, v) { try { v ? localStorage.setItem('barnraise.' + k, v) : localStorage.removeItem('barnraise.' + k); } catch { /* private mode */ } },
};

const state = {
  token: '',
  project: '',
  projects: [],
  agents: new Map(), // agent name -> SessionInfo, kept live from agent.session events
  metricsWindow: LS.get('window') || '7d',
  renderSeq: 0,
};

// ---------- DOM helpers ----------

// h builds an element. Strings and numbers become plain text; attributes
// starting with "on" become listeners. There is no HTML parsing anywhere.
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v === undefined || v === null || v === false) continue;
      if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
      else if (k === 'class') el.className = v;
      else if (k === 'style' && typeof v === 'object') Object.assign(el.style, v);
      else if (k in el && typeof v !== 'string') el[k] = v;
      else el.setAttribute(k, v === true ? '' : String(v));
    }
  }
  append(el, kids);
  return el;
}
function append(el, kids) {
  for (const k of kids.flat(Infinity)) {
    if (k === undefined || k === null || k === false || k === '') continue;
    el.appendChild(typeof k === "object" && "nodeType" in k ? k : document.createTextNode(String(k)));
  }
}
const txt = (s) => document.createTextNode(String(s ?? ''));

function section(title, ...kids) {
  return h('section', { class: 'box' }, title ? h('h2', null, title) : null, ...kids);
}
function empty(msg) { return h('p', { class: 'muted' }, msg || t('none')); }
function chip(text, cls) { return h('span', { class: 'chip ' + (cls || '') }, text); }
function itemLink(id) { return id ? h('a', { href: '#/item/' + encodeURIComponent(id), class: 'id' }, id) : null; }
function bar(pct, opts = {}) {
  const v = Math.max(0, Math.min(100, Number(pct) || 0));
  const cls = opts.ceiling && v >= opts.ceiling ? 'over' : v >= 90 ? 'over' : v >= 75 ? 'warn' : '';
  const b = h('div', { class: 'bar ' + cls, role: 'progressbar', 'aria-valuemin': '0', 'aria-valuemax': '100', 'aria-valuenow': String(Math.round(v)), 'aria-label': opts.label || '' },
    h('span', { class: 'fill', style: { width: v + '%' } }));
  if (opts.ceiling) b.appendChild(h('span', { class: 'mark', style: { left: Math.min(100, opts.ceiling) + '%' }, title: t('ceiling', opts.ceiling) }));
  return b;
}
function kv(pairs) {
  const dl = h('dl', { class: 'kv' });
  for (const [k, v] of pairs) {
    if (v === undefined || v === null || v === '' || (Array.isArray(v) && !v.length)) continue;
    dl.append(h('dt', null, k), h('dd', null, v));
  }
  return dl;
}
function list(items, render) {
  if (!items || !items.length) return null;
  return h('ul', { class: 'plain' }, items.map((x) => h('li', null, render ? render(x) : x)));
}

// ---------- formatting ----------

const nf = new Intl.NumberFormat(lang, { maximumFractionDigits: 1 });
const nfc = new Intl.NumberFormat(lang, { notation: 'compact', maximumFractionDigits: 1 });
const rtf = new Intl.RelativeTimeFormat(lang, { numeric: 'auto' });
const dtf = new Intl.DateTimeFormat(lang, { dateStyle: 'medium', timeStyle: 'short' });
const df = new Intl.DateTimeFormat(lang, { dateStyle: 'medium' });

const num = (v) => nf.format(Number(v) || 0);
const compact = (v) => nfc.format(Number(v) || 0);
function parseTime(s) {
  if (!s || s.startsWith('0001-')) return null;
  const d = new Date(s);
  return isNaN(d) ? null : d;
}
function ago(s) {
  const d = parseTime(s);
  if (!d) return '';
  const sec = (d.getTime() - Date.now()) / 1000;
  const a = Math.abs(sec);
  if (a < 60) return rtf.format(Math.round(sec), 'second');
  if (a < 3600) return rtf.format(Math.round(sec / 60), 'minute');
  if (a < 86400) return rtf.format(Math.round(sec / 3600), 'hour');
  return rtf.format(Math.round(sec / 86400), 'day');
}
function when(s) { const d = parseTime(s); return d ? dtf.format(d) : ''; }
function day(s) { const d = parseTime(s); return d ? df.format(d) : s || ''; }
function dur(sec) {
  sec = Math.max(0, Math.round(sec));
  if (sec < 60) return sec + 's';
  const m = Math.floor(sec / 60);
  if (m < 60) return m + 'm';
  const hh = Math.floor(m / 60);
  if (hh < 48) return hh + 'h ' + (m % 60) + 'm';
  return Math.floor(hh / 24) + 'd ' + (hh % 24) + 'h';
}
function since(s) { const d = parseTime(s); return d ? dur((Date.now() - d.getTime()) / 1000) : ''; }
function pct(v) { return nf.format(Number(v) || 0) + '%'; }
function ratioPct(v) { const n = Number(v) || 0; return pct(n <= 1 ? n * 100 : n); }

// ---------- API ----------

class AuthError extends Error {}

async function rpc(method, body = {}) {
  const res = await fetch('/barnraise.v1.BarnraiseService/' + method, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + state.token },
    body: JSON.stringify(body),
    credentials: 'omit',
  });
  let data = null;
  try { data = await res.json(); } catch { data = null; }
  if (res.status === 401) throw new AuthError((data && (data.message || data.error)) || t('tokenBad'));
  if (!res.ok) throw new Error((data && (data.message || data.error)) || res.status + ' ' + res.statusText);
  return data || {};
}
const P = (extra) => Object.assign({ project: state.project }, extra);

// act runs an action from a button: disables it, shows the result or the error.
async function act(btn, fn, okMsg) {
  if (btn) btn.disabled = true;
  try {
    const r = await fn();
    const msg = typeof okMsg === 'function' ? okMsg(r) : okMsg;
    if (msg) toast(msg);
    scheduleRefresh(0);
    return r;
  } catch (e) {
    handleError(e);
    return null;
  } finally {
    if (btn) btn.disabled = false;
  }
}
function handleError(e) {
  if (e instanceof AuthError) { signOut(t('tokenBad')); return; }
  toast(t('error', e.message), true);
}

let toastTimer = 0;
function toast(msg, bad) {
  const el = $('#toast');
  el.textContent = msg;
  el.className = 'toast' + (bad ? ' bad' : '');
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; }, bad ? 7000 : 3500);
}

// ---------- sheet: in-page form instead of browser dialogs ----------

let sheetReturn = null;
function openSheet(title, fields, submitLabel, onSubmit, opts = {}) {
  const sheet = $('#sheet');
  sheetReturn = document.activeElement;
  $('#sheet-title').textContent = title;
  const body = $('#sheet-body');
  body.replaceChildren();
  const form = h('form', { class: 'form', novalidate: true });
  const err = h('p', { class: 'err', role: 'alert', hidden: true });
  const inputs = {};
  for (const f of fields) {
    const id = 'f-' + f.name;
    let input;
    if (f.type === 'textarea') input = h('textarea', { id, name: f.name, rows: f.rows || 3, required: f.required, placeholder: f.placeholder || '' });
    else if (f.type === 'select') input = h('select', { id, name: f.name }, f.options.map(([v, l]) => h('option', { value: v, selected: v === f.value }, l)));
    else input = h('input', { id, name: f.name, type: f.type || 'text', required: f.required, placeholder: f.placeholder || '', list: f.list ? id + '-list' : null, autocomplete: 'off' });
    if (f.value && f.type !== 'select') input.value = f.value;
    inputs[f.name] = input;
    form.append(h('label', { for: id }, f.label), input,
      f.help ? h('small', { class: 'muted' }, f.help) : null,
      f.list ? h('datalist', { id: id + '-list' }, f.list.map((v) => h('option', { value: v }))) : null);
  }
  const submit = h('button', { type: 'submit', class: opts.danger ? 'danger' : 'primary' }, submitLabel);
  form.append(err, h('div', { class: 'row end' },
    h('button', { type: 'button', class: 'ghost', onclick: closeSheet }, t('cancel')), submit));
  form.addEventListener('submit', async (ev) => {
    ev.preventDefault();
    const values = {};
    for (const [k, el] of Object.entries(inputs)) values[k] = el.value.trim();
    for (const f of fields) {
      if (f.required && !values[f.name]) { inputs[f.name].focus(); inputs[f.name].setAttribute('aria-invalid', 'true'); return; }
    }
    submit.disabled = true;
    err.hidden = true;
    try {
      const msg = await onSubmit(values);
      closeSheet();
      if (msg) toast(msg);
      scheduleRefresh(0);
    } catch (e) {
      if (e instanceof AuthError) { closeSheet(); signOut(t('tokenBad')); return; }
      err.textContent = t('error', e.message);
      err.hidden = false;
    } finally {
      submit.disabled = false;
    }
  });
  body.append(form);
  sheet.hidden = false;
  const first = form.querySelector('input, textarea, select');
  (first || submit).focus();
}
function closeSheet() {
  if ($('#sheet').hidden) return;
  $('#sheet').hidden = true;
  $('#sheet-body').replaceChildren();
  if (sheetReturn && sheetReturn.focus) sheetReturn.focus();
  sheetReturn = null;
}
document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && !$('#sheet').hidden) closeSheet(); });
$('#sheet').addEventListener('click', (e) => { if (e.target.id === 'sheet') closeSheet(); });

// ---------- routing ----------

const routes = {
  home: { label: 'navHome', view: viewHome },
  queue: { label: 'navQueue', view: viewQueue },
  agents: { label: 'navAgents', view: viewAgents },
  accounts: { label: 'navAccounts', view: viewAccounts },
  proposals: { label: 'navProposals', view: viewProposals },
  machines: { label: 'navMachines', view: viewMachines },
  metrics: { label: 'navMetrics', view: viewMetrics },
  item: { view: viewItem, parent: 'queue' },
};
function route() {
  const parts = (location.hash || '#/home').replace(/^#\/?/, '').split('/');
  const name = routes[parts[0]] ? parts[0] : 'home';
  return { name, arg: parts[1] ? decodeURIComponent(parts[1]) : '' };
}
function renderNav() {
  const r = route();
  const nav = $('#nav');
  nav.replaceChildren(...Object.entries(routes).filter(([, v]) => v.label).map(([k, v]) =>
    h('a', { href: '#/' + k, 'aria-current': (r.name === k || routes[r.name].parent === k) ? 'page' : null }, t(v.label))));
}

let lastRouteKey = '';
async function render() {
  if (!state.token) return showSignIn();
  if (!state.project) return;
  renderNav();
  const r = route();
  const key = r.name + '/' + r.arg + '@' + state.project;
  const view = $('#view');
  const seq = ++state.renderSeq;
  const fresh = key !== lastRouteKey;
  if (fresh) {
    lastRouteKey = key;
    view.replaceChildren(h('p', { class: 'muted' }, t('loading')));
  }
  try {
    const content = await routes[r.name].view(r.arg);
    if (seq !== state.renderSeq) return; // a newer render started
    const scroll = window.scrollY;
    view.replaceChildren(content);
    if (!fresh) window.scrollTo(0, scroll);
    else { document.title = 'Barnraise · ' + (routes[r.name].label ? t(routes[r.name].label) : r.arg); }
  } catch (e) {
    if (seq !== state.renderSeq) return;
    if (e instanceof AuthError) return signOut(t('tokenBad'));
    view.replaceChildren(section(null, h('p', { class: 'err' }, t('error', e.message)),
      h('button', { type: 'button', onclick: () => render() }, t('retry'))));
  }
}

let refreshTimer = 0;
function scheduleRefresh(ms = 300) {
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(() => {
    // Do not pull the page from under someone typing in a form of the view.
    const a = document.activeElement;
    if (a && $('#view').contains(a) && /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName)) return scheduleRefresh(1500);
    render();
  }, ms);
}

// ---------- screens ----------

async function viewHome() {
  const s = await rpc('Status', P());
  const root = h('div', { class: 'grid' });

  // emergency first when paused: it explains everything else
  const emergency = h('section', { class: 'box emergency' + (s.paused ? ' is-paused' : '') },
    h('h2', null, t('emergency')),
    h('p', { class: 'state' }, s.paused ? (s.paused_why ? t('pausedSince', s.paused_why) : t('pausedNoWhy')) : t('running')),
    s.paused
      ? h('button', { type: 'button', class: 'big primary', onclick: (e) => act(e.currentTarget, () => rpc('Resume', P()), t('resumed')) }, t('resumeAll'))
      : h('button', { type: 'button', class: 'big danger', onclick: () => openSheet(t('pauseTitle'),
        [{ name: 'why', label: t('pauseWhy'), type: 'textarea', required: true }], t('pauseConfirm'),
        async (v) => { await rpc('Pause', P({ why: v.why })); return t('paused'); }, { danger: true }) }, t('pauseAll')),
    s.paused ? null : h('small', { class: 'muted' }, t('pauseHelp')));

  // goal
  const g = s.goal, pr = s.progress || {};
  const goalBox = section(t('goal'));
  if (g && g.name) {
    goalBox.append(
      h('p', { class: 'lead' }, g.name),
      g.done_when ? h('p', { class: 'muted' }, t('doneWhen', g.done_when)) : null,
      h('div', { class: 'big-num' }, (pr.percentage || 0) + '%'),
      bar(pr.percentage, { label: t('goal') }),
      h('p', null, t('goalItems', pr.done || 0, pr.total || 0)),
      h('p', null, t('pace', num(pr.per_day)), ' · ', pr.forecast ? t('forecast', day(pr.forecast)) : t('noForecast')));
  } else goalBox.append(empty(t('noGoal')));

  // focus
  const f = s.focus;
  const focusBox = section(t('focus'));
  if (f && f.name && !f.cancelled) {
    const all = s.focus_all || 0, done = s.focus_done || 0;
    focusBox.append(
      h('p', { class: 'lead' }, f.name, f.area ? ' ' : null, f.area ? chip(f.area) : null),
      bar(all ? (done * 100) / all : 0, { label: t('focus') }),
      h('p', null, t('focusItems', done, all)),
      h('p', { class: 'muted' }, f.until ? t('until', day(f.until)) : t('untilDone'), f.share ? ' · ' + t('share', f.share) : ''));
  } else focusBox.append(empty(t('noFocus')));

  // waiting for the owner
  const questions = s.waiting_owner || [];
  const asked = new Set(questions.map((q) => q.item_id).filter(Boolean));
  const waitingItems = (s.queue || []).filter((it) => (it.status === 'waiting_owner' || /approv/i.test(it.step || '')) && !asked.has(it.id));
  const waitBox = section(t('waiting'));
  if (!questions.length && !waitingItems.length) waitBox.append(empty(t('noWaiting')));
  for (const q of questions) waitBox.append(questionView(q));
  for (const it of waitingItems) {
    waitBox.append(h('article', { class: 'q' },
      h('p', null, itemLink(it.id), ' ', it.title),
      it.waiting_question ? h('p', { class: 'muted' }, it.waiting_question) : h('p', { class: 'muted' }, t('step') + ': ' + (it.step || '')),
      approvalButtons(it)));
  }
  if (s.open_proposals) waitBox.append(h('p', null, h('a', { href: '#/proposals' }, t('openProposals', s.open_proposals))));

  // alerts
  const alerts = s.alerts || [];
  const alertBox = section(t('alerts'), alerts.length ? h('ul', { class: 'alerts' }, alerts.map((a) => h('li', null, a))) : empty(t('noAlerts')));

  // counts
  const counts = s.counts || {};
  const order = ['ready', 'in_progress', 'waiting_owner', 'blocked', 'queued', 'draft', 'paused', 'integrated'];
  const countBox = section(t('counts'), h('ul', { class: 'counts' },
    order.filter((k) => counts[k]).map((k) => h('li', null, h('a', { href: '#/queue' }, h('strong', null, counts[k]), ' ', tm('statuses', k))))));

  root.append(waitBox, goalBox, focusBox, emergency, alertBox, countBox);
  if (s.paused) root.prepend(emergency);
  return root;
}

function questionView(q) {
  const opts = q.options || [];
  const art = h('article', { class: 'q' + (q.urgent ? ' urgent' : '') },
    h('p', null, q.urgent ? chip(t('urgent'), 'bad') : null, q.urgent ? ' ' : null, itemLink(q.item_id), q.item_id ? ' ' : null, q.context),
    h('p', { class: 'lead' }, q.decision),
    h('div', { class: 'row wrap' }, opts.map((o) =>
      h('button', { type: 'button', class: o.recommended ? 'primary' : '', onclick: (e) =>
        act(e.currentTarget, () => rpc('Answer', P({ id: q.id, answer: o.label })), t('answered', o.label)) },
      o.label, o.recommended ? h('span', { class: 'sr' }, ' (' + t('recommended') + ')') : null, o.recommended ? ' ★' : null))),
    h('p', { class: 'muted small' },
      q.if_no_answer ? t('ifNoAnswer', q.if_no_answer) : null,
      q.deadline ? ' · ' + t('deadline', when(q.deadline)) : null,
      q.asked_by ? ' · ' + t('askedBy', q.asked_by, ago(q.asked_at)) : null));
  return art;
}

// approvalWhat guesses what the current step approves from the item. The
// daemon checks it and answers with an error if it does not match.
function approvalWhat(it) {
  const s = ((it.step || '') + ' ' + (it.waiting_question || '')).toLowerCase();
  return /plan/.test(s) && !/deliver/.test(s) ? 'plan' : 'delivery';
}
function approvalButtons(it) {
  const what = approvalWhat(it);
  return h('div', { class: 'row wrap' },
    h('button', { type: 'button', class: 'primary', onclick: (e) =>
      act(e.currentTarget, () => rpc('Approve', P({ target: it.id, what, approve: true })), t('approved')) },
    what === 'plan' ? t('approvePlan') : t('approveDelivery')),
    h('button', { type: 'button', onclick: () => openSheet(t('sendBackTitle', it.id),
      [{ name: 'note', label: t('reason'), type: 'textarea', required: true }], t('sendBack'),
      async (v) => { await rpc('Approve', P({ target: it.id, what, approve: false, note: v.note })); return t('sentBack'); }) }, t('sendBack')));
}

const QUEUE_COLS = ['ready', 'in_progress', 'waiting_owner', 'blocked', 'queued', 'draft', 'paused'];

async function viewQueue() {
  const items = await rpc('ListItems', P());
  const all = (items.items || []).filter((it) => it.status !== 'integrated' && it.status !== 'closed');
  const by = {};
  for (const it of all) (by[it.status] = by[it.status] || []).push(it);
  for (const k of Object.keys(by)) by[k].sort((a, b) => (a.priority || 0) - (b.priority || 0) || String(a.id).localeCompare(String(b.id), undefined, { numeric: true }));
  const cols = QUEUE_COLS.filter((k) => k !== 'paused' || by.paused);
  const head = h('div', { class: 'row between' },
    h('h1', null, t('queue')),
    h('button', { type: 'button', class: 'primary', onclick: newItemSheet }, t('newItem')));
  const board = h('div', { class: 'board' }, cols.map((k) =>
    h('section', { class: 'col', 'aria-label': tm('statuses', k) },
      h('h2', null, tm('statuses', k), ' ', h('span', { class: 'count' }, (by[k] || []).length)),
      (by[k] || []).length ? h('ul', { class: 'plain' }, by[k].map((it) => h('li', null, itemEntry(it)))) : empty())));
  return h('div', null, head, board);
}

function itemEntry(it) {
  const holder = it.claim && (it.claim.agent || it.claim.machine)
    ? [it.claim.agent, it.claim.machine].filter(Boolean).join(' @ ') : '';
  return h('a', { class: 'entry', href: '#/item/' + encodeURIComponent(it.id) },
    h('span', { class: 'entry-top' }, h('span', { class: 'id' }, it.id), ' ', chip('P' + (it.priority || 0), 'p' + (it.priority || 0)),
      it.kind ? chip(it.kind) : null, it.area ? chip(it.area) : null, it.risky ? chip(t('risky'), 'bad') : null),
    h('span', { class: 'entry-title' }, it.title),
    h('span', { class: 'entry-meta' },
      it.step ? h('span', null, t('step') + ': ' + it.step) : null,
      holder ? h('span', null, t('heldBy', holder)) : null,
      it.step_started_at ? h('span', null, t('since', since(it.step_started_at))) : null,
      (it.rework || []).length ? h('span', null, t('reworkRounds', it.rework.length)) : null,
      (it.deps || []).length ? h('span', null, t('deps') + ': ' + it.deps.join(', ')) : null));
}

function newItemSheet() {
  openSheet(t('newItem'), [
    { name: 'title', label: t('title'), required: true },
    { name: 'kind', label: t('kind'), list: ['feature', 'bug', 'refactor', 'chore', 'docs'], value: 'feature' },
    { name: 'priority', label: t('priority'), type: 'select', value: 'P2', options: [['P0', 'P0'], ['P1', 'P1'], ['P2', 'P2'], ['P3', 'P3']] },
    { name: 'area', label: t('area') },
    { name: 'body', label: t('body'), type: 'textarea', rows: 4 },
    { name: 'criteria', label: t('criteria'), type: 'textarea', rows: 4, help: t('criteriaHelp'),
      placeholder: 'WHEN …, THE SYSTEM SHALL … | check' },
  ], t('create'), async (v) => {
    const criteria = v.criteria.split('\n').map((l) => l.trim()).filter(Boolean).map((l) => {
      const i = l.lastIndexOf('|');
      return i > 0 ? { text: l.slice(0, i).trim(), check: l.slice(i + 1).trim() } : { text: l };
    });
    const r = await rpc('CreateItem', P({ title: v.title, kind: v.kind, priority: v.priority, area: v.area, body: v.body, criteria }));
    if (r.item && r.item.id) { location.hash = '#/item/' + encodeURIComponent(r.item.id); return t('created', r.item.id); }
    if (r.proposal && r.proposal.id) return t('createdProposal', r.proposal.id, r.note || '');
    return r.note || t('done');
  });
}

async function viewAgents() {
  const s = await rpc('Status', P());
  // the status is the base; live sessions from the stream are newer
  const merged = new Map();
  for (const a of s.agents || []) merged.set(a.agent, a);
  for (const [k, a] of state.agents) {
    const base = merged.get(k);
    if (!base || (parseTime(a.at) || 0) >= (parseTime(base.at) || 0)) merged.set(k, a);
  }
  return h('div', null, h('h1', null, t('agents')), agentsTable([...merged.values()]));
}
function agentsTable(rows) {
  // finished sessions stay visible for ten minutes, then leave the table
  rows = rows.filter((a) => !/^(done|failed)$/.test(a.state || '') || Date.now() - (parseTime(a.at) || 0) < 10 * 60e3);
  if (!rows.length) return empty(t('noAgents'));
  rows.sort((a, b) => String(a.role).localeCompare(String(b.role)) || String(a.agent).localeCompare(String(b.agent)));
  return h('div', { class: 'table-wrap', id: 'agents-table' }, h('table', null,
    h('thead', null, h('tr', null, ['agent', 'role', 'account', 'model', 'item', 'step', 'state', 'updated'].map((k) => h('th', { scope: 'col' }, t(k))))),
    h('tbody', null, rows.map((a) => h('tr', null,
      h('td', null, a.agent),
      h('td', null, a.role),
      h('td', null, a.account, a.provider ? h('small', { class: 'muted' }, ' · ' + a.provider) : null),
      h('td', null, a.model, a.effort ? h('small', { class: 'muted' }, ' · ' + t('effort', a.effort)) : null),
      h('td', null, itemLink(a.item_id)),
      h('td', null, a.step),
      h('td', null, h('span', { class: 'dot s-' + (a.state || 'idle'), 'aria-hidden': 'true' }), ' ', tm('agentStates', a.state),
        a.detail ? h('div', { class: 'muted small' }, a.detail) : null),
      h('td', null, ago(a.at)))))));
}

async function viewAccounts() {
  const r = await rpc('ListAccounts', P());
  const accs = r.accounts || [];
  const root = h('div', null, h('h1', null, t('accounts')));
  if (!accs.length) { root.append(empty(t('noAccounts'))); return root; }
  const grid = h('div', { class: 'grid' });
  for (const a of accs) {
    const u = a.usage || {};
    const ceiling = Number(a.ceiling_pct) || 0;
    const meter = (label, v, resets) => h('div', { class: 'meter' },
      h('div', { class: 'row between' }, h('span', null, label), h('strong', null, pct(v))),
      bar(v, { ceiling, label }),
      resets ? h('small', { class: 'muted' }, t('resets', ago(resets) + ' · ' + when(resets))) : null);
    grid.append(h('section', { class: 'box' },
      h('h2', null, a.id, ' ', a.status ? chip(tm('accountStatus', a.status), 'st-' + a.status) : null),
      kv([[t('provider'), a.provider], [t('plan'), a.plan], [t('machine'), a.machine], [t('projects'), (a.projects || []).join(', ')]]),
      meter(t('fiveHour'), u.five_hour_pct, u.five_hour_resets),
      meter(t('weekly'), u.weekly_pct, u.weekly_resets),
      u.weekly_top_pct ? meter(t('weeklyTop'), u.weekly_top_pct) : null,
      ceiling ? h('p', { class: 'muted small' }, t('ceiling', num(ceiling))) : null,
      u.updated_at ? h('p', { class: 'muted small' }, t('usageFrom', u.source || '', ago(u.updated_at))) : null));
  }
  root.append(grid);
  return root;
}

async function viewItem(id) {
  const [ir, tl, est] = await Promise.all([
    rpc('GetItem', P({ id })),
    rpc('Timeline', P({ id })).catch(() => ({ entries: [] })),
    rpc('Estimate', P({ id })).catch(() => null),
  ]);
  const it = ir.item || {};
  const root = h('div', { class: 'item' });
  root.append(h('p', null, h('a', { href: '#/queue' }, '← ' + t('backQueue'))));

  // header
  const holder = it.claim && (it.claim.agent || it.claim.machine) ? [it.claim.agent, it.claim.machine].filter(Boolean).join(' @ ') : '';
  root.append(h('header', { class: 'box item-head' },
    h('h1', null, h('span', { class: 'id' }, it.id), ' ', it.title),
    h('div', { class: 'row wrap' }, chip(tm('statuses', it.status), 'st-' + it.status), chip('P' + (it.priority || 0), 'p' + (it.priority || 0)),
      it.kind ? chip(it.kind) : null, it.area ? chip(it.area) : null, it.size ? chip(it.size) : null,
      it.risky ? chip(t('risky'), 'bad') : null, it.parked ? chip(t('parked')) : null),
    kv([
      [t('step'), it.step ? [it.step, it.step_started_at ? ' · ' + t('since', since(it.step_started_at)) : ''].join('') : ''],
      [t('holderLabel'), holder],
      [t('goal'), it.goal_id],
      [t('tier'), it.tier || ''],
      [t('attempts'), it.attempts || ''],
      [t('branch'), it.branch],
      [t('pr'), it.pr],
      [t('tracker'), it.tracker_ref],
      [t('deps'), (it.deps || []).length ? it.deps.map((d, i) => [i ? ', ' : '', itemLink(d)]) : null],
    ]),
    it.waiting_question ? h('p', { class: 'warn-text' }, t('waitingQuestion', it.waiting_question)) : null,
    it.closed_reason ? h('p', { class: 'muted' }, t('closedReason', it.closed_reason + (it.closed_by ? ' (' + it.closed_by + ')' : ''))) : null,
    it.merge_commit ? h('p', { class: 'muted' }, t('merged', it.merge_commit)) : null,
    itemActions(it)));

  // estimate
  if (est && (est.hours || est.tokens)) {
    root.append(section(t('estimate'), h('p', null, t('estimateLine', num(est.hours), compact(est.tokens), num(est.week_pct), est.account || '—')),
      est.based_on_items ? h('p', { class: 'muted small' }, t('estimateBase', est.based_on_items)) : null));
  }

  // body: tracker text stays plain text
  if (it.body) root.append(section(t('body'), h('pre', { class: 'body' }, it.body)));

  // criteria
  const crit = it.criteria || [];
  if (crit.length) {
    root.append(section(t('criteria'), h('ul', { class: 'checks' }, crit.map((c) => h('li', { class: c.met ? 'met' : '' },
      h('span', { class: 'tick', 'aria-label': c.met ? t('met') : t('notMet'), role: 'img' }, c.met ? '✓' : '○'),
      h('div', null, c.id ? h('span', { class: 'id' }, c.id + ' ') : null, c.text,
        c.check ? h('div', { class: 'muted small' }, t('checkName', c.check)) : null,
        c.evidence ? h('div', { class: 'muted small' }, t('evidence', c.evidence)) : null))))));
  }
  for (const [k, arr] of [['preserve', it.preserve], ['checklist', it.checklist], ['clarifications', it.clarifications], ['tweaks', it.tweaks]]) {
    if (arr && arr.length) root.append(section(t(k), list(arr)));
  }

  // anchors
  const an = it.anchors || {};
  const anchorPairs = ['files', 'symbols', 'areas', 'screens', 'contracts', 'decisions', 'specs']
    .filter((k) => (an[k] || []).length).map((k) => [t(k), h('span', { class: 'mono' }, an[k].join(', '))]);
  if (an.base) anchorPairs.push([t('base'), h('span', { class: 'mono' }, an.base)]);
  if (anchorPairs.length) root.append(section(t('anchors'), kv(anchorPairs)));
  if ((it.files_touched || []).length) root.append(section(t('filesTouched'), h('p', { class: 'mono small' }, it.files_touched.join(', '))));

  // rework rounds
  if ((it.rework || []).length) {
    root.append(section(t('rework'), h('ol', { class: 'plain' }, it.rework.map((r) => h('li', null,
      t('reworkLine', r.from, r.to, r.by, r.reason), r.outcome ? h('span', { class: 'muted' }, ' · ' + r.outcome) : null,
      r.at ? h('span', { class: 'muted small' }, ' · ' + when(r.at)) : null)))));
  }

  // timeline
  root.append(section(t('timeline'), timelineView(it, tl.entries || [])));
  return root;
}

function itemActions(it) {
  const row = h('div', { class: 'row wrap actions' });
  if (it.status === 'waiting_owner' || /approv/i.test(it.step || '')) row.append(approvalButtons(it));
  if (it.status === 'paused') {
    row.append(h('button', { type: 'button', onclick: () => openSheet(t('handbackTitle', it.id),
      [{ name: 'note', label: t('note'), type: 'textarea' }], t('handback'),
      async (v) => { await rpc('Handback', P({ id: it.id, note: v.note })); return t('handedBack'); }) }, t('handback')));
  } else if (it.status !== 'integrated' && it.status !== 'closed') {
    row.append(h('button', { type: 'button', onclick: (e) => act(e.currentTarget, () => rpc('Takeover', P({ id: it.id })),
      (r) => t('takenOver', (r && r.worktree) || '')) }, t('takeover')));
  }
  return row.childNodes.length ? row : null;
}

function timelineView(it, entries) {
  if (!entries.length) return empty(t('noTimeline'));
  const canRedo = it.status !== 'integrated' && it.status !== 'closed';
  return h('ol', { class: 'timeline' }, entries.map((e) => {
    const p = e.payload || {};
    const isStep = e.type === 'item.step.started' || e.type === 'item.step.finished';
    const cls = e.type.replace(/\W+/g, '-');
    const li = h('li', { class: 'tl ' + cls + (failed(e, p) ? ' fail' : '') },
      h('div', { class: 'tl-head' },
        h('strong', null, tm('events', e.type)), e.step ? chip(e.step) : null,
        h('time', { datetime: e.time, title: when(e.time) }, when(e.time)),
        e.actor ? h('span', { class: 'muted small' }, e.actor + (e.machine ? ' @ ' + e.machine : '')) : null),
      e.summary ? h('p', null, e.summary) : null,
      payloadView(p),
      canRedo && isStep && e.step && e.type === 'item.step.started'
        ? h('button', { type: 'button', class: 'small', onclick: () => openSheet(t('redoTitle', it.id, e.step),
          [{ name: 'why', label: t('redoWhy'), type: 'textarea', required: true }], t('redoConfirm'),
          async (v) => { await rpc('Redo', P({ id: it.id, step: e.step, why: v.why })); return t('redone', it.id, e.step); }) }, t('redoHere'))
        : null);
    return li;
  }));
}
function failed(e, p) {
  if (p.check && !p.check.passed) return true;
  if (p.review && /rework|reject/i.test(p.review.decision || '')) return true;
  return e.type === 'rework.started' || e.type === 'item.blocked';
}
function payloadView(p) {
  const out = [];
  if (p.delivery) {
    const d = p.delivery;
    out.push(h('div', { class: 'pl' }, chip(d.status, /DONE$/.test(d.status || '') ? 'good' : 'warn'), ' ', d.summary,
      d.reason ? h('p', { class: 'muted' }, d.reason) : null,
      (d.concerns || []).length ? h('div', null, h('small', null, t('concerns')), list(d.concerns)) : null));
  }
  if (p.review) {
    const r = p.review;
    const v = (x) => chip(x === 'pass' ? t('passed') : x === 'fail' ? t('failed') : x || '—', x === 'pass' ? 'good' : x === 'fail' ? 'bad' : '');
    out.push(h('div', { class: 'pl' },
      h('div', { class: 'row wrap' }, t('verdictCriteria', ''), v(r.criteria_verdict), t('verdictCode', ''), v(r.code_verdict),
        t('decision', ''), chip(r.decision, r.decision === 'APPROVE' ? 'good' : 'bad'), r.reviewer ? h('span', { class: 'muted small' }, r.reviewer + (r.family ? ' · ' + r.family : '')) : null),
      r.summary ? h('p', null, r.summary) : null,
      (r.findings || []).length ? h('div', null, h('small', null, t('findings')), list(r.findings, (f) => [
        f.severity ? chip(f.severity) : null, ' ', f.file ? h('span', { class: 'mono' }, f.file + (f.line ? ':' + f.line : '') + ' ') : null, f.text])) : null));
  }
  if (p.check) {
    const c = p.check;
    out.push(h('div', { class: 'pl' }, chip(c.passed ? t('passed') : t('failed'), c.passed ? 'good' : 'bad'), c.flaky ? chip(t('flaky'), 'warn') : null,
      ' ', t('checkName', c.check), c.duration ? h('span', { class: 'muted' }, ' · ' + dur(Number(c.duration) / 1e9)) : null,
      c.runs_on ? h('span', { class: 'muted' }, ' · ' + c.runs_on) : null,
      c.summary ? h('p', null, c.summary) : null));
  }
  if (p.rework) {
    const r = p.rework;
    out.push(h('p', { class: 'pl' }, t('reworkLine', r.from, r.to, r.by, r.reason)));
  }
  if (p.usage) {
    const u = p.usage;
    out.push(h('p', { class: 'pl muted small' }, t('tokens') + ': ', t('tokenLine', compact(u.input), compact(u.output), compact(u.cache_read)),
      u.model ? ' · ' + u.model : '', u.role ? ' · ' + u.role : '', u.duration ? ' · ' + dur(Number(u.duration) / 1e9) : ''));
  }
  if (p.question) out.push(h('p', { class: 'pl' }, p.question.decision, p.question.answer ? ' → ' + p.question.answer : ''));
  if (p.note && !out.length) out.push(h('p', { class: 'pl muted' }, p.note));
  return out;
}

async function viewProposals() {
  const r = await rpc('ListProposals', P());
  let groups = r.groups || [];
  if (!groups.length && (r.proposals || []).length) {
    const by = {};
    for (const p of r.proposals) (by[p.area || ''] = by[p.area || ''] || []).push(p);
    groups = Object.entries(by).map(([area, proposals]) => ({ area, proposals }));
  }
  const root = h('div', null, h('h1', null, t('proposals')));
  const open = groups.map((g) => ({ area: g.area, proposals: (g.proposals || []).filter((p) => !p.status || p.status === 'open' || p.status === 'later') }))
    .filter((g) => g.proposals.length);
  if (!open.length) { root.append(empty(t('noProposals'))); return root; }
  for (const g of open) {
    root.append(section(g.area || t('noArea'), g.proposals.map(proposalView)));
  }
  return root;
}
function proposalView(p) {
  const decide = (decision, reason) => rpc('DecideProposal', P({ id: p.id, decision, reason }));
  return h('article', { class: 'proposal' },
    h('h3', null, h('span', { class: 'id' }, p.id), ' ', p.title),
    h('div', { class: 'row wrap' }, p.kind ? chip(p.kind) : null, chip('P' + (p.priority || 0), 'p' + (p.priority || 0)), p.size ? chip(p.size) : null,
      p.status === 'later' && p.later_until ? chip(t('later') + ' · ' + day(p.later_until)) : null,
      p.by ? h('span', { class: 'muted small' }, t('by', p.by) + ' · ' + ago(p.created_at)) : null),
    kv([[t('problem'), p.problem], [t('evidenceLabel'), p.evidence], [t('impact'), p.impact], [t('goalLink'), p.goal_link]]),
    list(p.comments),
    h('div', { class: 'row wrap' },
      h('button', { type: 'button', class: 'primary', onclick: (e) => act(e.currentTarget, () => decide('approve'), t('decided', p.id, t('approveProposal'))) }, t('approveProposal')),
      h('button', { type: 'button', onclick: () => openSheet(t('rejectTitle', p.id), [{ name: 'reason', label: t('reason'), type: 'textarea' }], t('reject'),
        async (v) => { await decide('reject', v.reason); return t('decided', p.id, t('reject')); }) }, t('reject')),
      h('button', { type: 'button', class: 'ghost', onclick: (e) => act(e.currentTarget, () => decide('later'), t('decided', p.id, t('later'))) }, t('later'))));
}

async function viewMachines() {
  const r = await rpc('ListMachines', P());
  const ms = r.machines || [];
  const root = h('div', null, h('h1', null, t('machines')));
  if (!ms.length) { root.append(empty(t('noMachines'))); return root; }
  const grid = h('div', { class: 'grid' });
  for (const m of ms) {
    const health = m.left ? 'gray' : (m.health || 'gray');
    const free = Number(m.free_cpu) || 0;
    grid.append(h('section', { class: 'box' },
      h('h2', null, h('span', { class: 'dot h-' + health, role: 'img', 'aria-label': tm('health', health) }), ' ', m.name,
        ' ', h('small', { class: 'muted' }, tm('health', health)),
        m.leaving ? chip(t('leaving'), 'warn') : null, m.left ? chip(t('left')) : null),
      h('div', { class: 'meter' }, h('div', { class: 'row between' }, h('span', null, t('slots', m.busy || 0, m.slots || 0))),
        bar(m.slots ? ((m.busy || 0) * 100) / m.slots : 0, { label: t('slots', m.busy || 0, m.slots || 0) })),
      kv([
        ['CPU', t('cpus', m.cpus || 0, free <= 1 && free > 0 ? pct(free * 100) : num(free))],
        [t('memoryLabel'), t('memory', num(m.mem_gb))],
        [t('emulatorsLabel'), t('emulators', m.emulators || 0)],
        [t('gpu'), m.gpu ? '✓' : ''],
        [t('versionLabel'), m.version],
        [t('lastSeenLabel'), ago(m.last_seen)],
        [t('accounts'), (m.accounts || []).join(', ')],
        [t('labels'), (m.labels || []).length ? m.labels.map((l) => chip(l)) : null],
      ])));
  }
  root.append(grid);
  return root;
}

async function viewMetrics() {
  const m = await rpc('Metrics', P({ window: state.metricsWindow }));
  const sel = h('select', { id: 'window', onchange: (e) => { state.metricsWindow = e.target.value; LS.set('window', e.target.value); e.target.blur(); render(); } },
    ['1d', '7d', '30d', '90d'].map((w) => h('option', { value: w, selected: w === state.metricsWindow }, w)));
  const stat = (label, value, alert) => h('div', { class: 'stat' + (alert ? ' alert' : '') }, h('span', { class: 'stat-num' }, value), h('span', { class: 'stat-label' }, label));
  const made = m.proposals_made || 0, ok = m.proposals_approved || 0;
  const root = h('div', null,
    h('div', { class: 'row between' }, h('h1', null, t('metrics')), h('label', { class: 'row' }, t('window') + ' ', sel)),
    h('div', { class: 'stats' },
      stat(t('integratedPerDay'), num(m.integrated_per_day)),
      stat(t('reworkAvg'), num(m.rework_avg), Number(m.rework_avg) > 2),
      stat(t('cacheHit'), ratioPct(m.cache_hit_rate)),
      stat(t('proposalsRate'), t('proposalsLine', ok, made), made >= 5 && ok / made < 0.2),
      stat(t('waitingMax'), m.waiting_owner_max || '—'),
      stat(t('violations'), m.violations || 0, (m.violations || 0) > 0),
      stat(t('flakyQ'), m.flaky_quarantined || 0, (m.flaky_quarantined || 0) > 5)),
    (m.alerts || []).length ? section(t('alerts'), h('ul', { class: 'alerts' }, m.alerts.map((a) => h('li', null, a)))) : null,
    h('div', { class: 'grid' },
      section(t('byKind'), bars(m.integrated_by_kind, num)),
      section(t('reworkCauses'), bars(m.rework_causes, num, true)),
      section(t('tokensByModel'), bars(m.tokens_by_model, compact)),
      section(t('tokensByRole'), bars(m.tokens_by_role, compact)),
      section(t('stepSeconds'), bars(m.step_seconds, (v) => dur(v))),
      section(t('tokensByItem'), bars(m.tokens_by_item, compact, false, 10, true))));
  return root;
}
function bars(map, fmt, share, limit = 12, links) {
  const rows = Object.entries(map || {}).map(([k, v]) => [k, Number(v) || 0]).filter(([, v]) => v > 0).sort((a, b) => b[1] - a[1]).slice(0, limit);
  if (!rows.length) return empty();
  const max = rows[0][1];
  const total = rows.reduce((s, [, v]) => s + v, 0);
  return h('ul', { class: 'hbars' }, rows.map(([k, v]) => h('li', null,
    h('span', { class: 'hb-label' }, links ? itemLink(k) : k),
    h('span', { class: 'hb-track' }, h('span', { class: 'hb-fill' + (share && v / total > 0.3 ? ' over' : ''), style: { width: (v * 100) / max + '%' } })),
    h('span', { class: 'hb-val' }, fmt(v), share ? h('small', { class: 'muted' }, ' ' + pct((v * 100) / total)) : null))));
}

// ---------- live events ----------

const EVENT_TYPES = [
  'item.created', 'item.updated', 'item.claimed', 'item.released', 'item.step.started', 'item.step.finished',
  'item.blocked', 'item.waiting_owner', 'item.queued', 'item.integrated', 'item.closed', 'item.paused', 'item.resumed',
  'item.redo', 'item.revalidated', 'item.impacted', 'item.updated_by_change', 'item.obsoleted', 'item.superseded',
  'item.merged_into', 'delivery.submitted', 'review.submitted', 'check.requested', 'check.completed', 'rework.started',
  'proposal.created', 'proposal.updated', 'tweak.added', 'tweak.attached', 'owner.question.asked', 'owner.question.answered',
  'goal.set', 'goal.finished', 'focus.changed', 'system.paused', 'system.resumed', 'account.updated',
  'account.limit.reached', 'machine.updated', 'machine.left', 'agent.session', 'agent.usage', 'message.sent',
  'message.delivered', 'journal.entry', 'policy.violation.blocked', 'secret.blocked',
];

const live = { es: null, lastSeq: 0, backoff: 1000, timer: 0, countdown: 0 };

function setLive(mode, extra) {
  const el = $('#live');
  el.className = 'live ' + mode;
  el.querySelector('.live-text').textContent = mode === 'on' ? t('live') : mode === 'off' ? t('offline', extra) : t('connecting');
}
function pulse() {
  const el = $('#live');
  el.classList.remove('pulse');
  void el.offsetWidth; // restart the animation
  el.classList.add('pulse');
}

function connect() {
  disconnect();
  if (!state.token || !state.project) return;
  setLive('wait');
  const q = new URLSearchParams({ project: state.project, token: state.token });
  if (live.lastSeq > 0) q.set('after_seq', String(live.lastSeq));
  else q.set('backlog', '1');
  const es = new EventSource('/v1/events?' + q.toString());
  live.es = es;
  let replaying = !live.lastSeq;
  const onEvent = (ev) => {
    if (ev.lastEventId) { const n = Number(ev.lastEventId); if (n > live.lastSeq) live.lastSeq = n; }
    let ce = null;
    try { ce = JSON.parse(ev.data); } catch { /* not JSON */ }
    const type = (ev.type && ev.type !== 'message') ? ev.type : String((ce && ce.type) || '').replace(/^dev\.barnraise\./, '');
    if (type === 'agent.session' && ce && ce.data && ce.data.session) {
      const s = ce.data.session;
      if (s.agent) state.agents.set(s.agent, s);
    }
    if (replaying) return; // the backlog only fills state; the screen is already fresh
    pulse();
    scheduleRefresh();
  };
  es.onmessage = onEvent;
  for (const typ of EVENT_TYPES) es.addEventListener(typ, onEvent);
  es.onopen = () => {
    live.backoff = 1000;
    setLive('on');
    // The ": connected" comment ends the backlog; there is no event for it,
    // so stop treating events as replay shortly after the stream opens.
    setTimeout(() => { replaying = false; }, 800);
  };
  es.onerror = () => {
    if (live.es !== es) return;
    es.close();
    live.es = null;
    const wait = live.backoff;
    live.backoff = Math.min(live.backoff * 2, 30000);
    let left = Math.round(wait / 1000);
    setLive('off', left);
    clearInterval(live.countdown);
    live.countdown = setInterval(() => { left -= 1; if (left > 0) setLive('off', left); }, 1000);
    live.timer = setTimeout(() => { clearInterval(live.countdown); connect(); scheduleRefresh(0); }, wait);
  };
}
function disconnect() {
  clearTimeout(live.timer);
  clearInterval(live.countdown);
  if (live.es) { live.es.close(); live.es = null; }
}

// ---------- auth and boot ----------

function showSignIn(msg) {
  disconnect();
  $('#nav').replaceChildren();
  $('#project').replaceChildren();
  document.body.classList.add('signed-out');
  const input = h('input', { id: 'token', type: 'password', autocomplete: 'current-password', required: true, spellcheck: 'false' });
  const err = h('p', { class: 'err', role: 'alert' }, msg || '');
  const form = h('form', { class: 'form signin box' },
    h('h1', null, t('signin')),
    h('label', { for: 'token' }, t('tokenLabel')), input,
    h('small', { class: 'muted' }, t('tokenHelp')),
    err,
    h('button', { type: 'submit', class: 'primary' }, t('signin')));
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const v = input.value.trim();
    if (!v) return input.focus();
    state.token = v;
    if (!(await boot())) { err.textContent = t('tokenBad'); input.select(); }
  });
  $('#view').replaceChildren(form);
  input.focus();
}

function signOut(msg) {
  state.token = '';
  LS.set('token', '');
  lastRouteKey = '';
  showSignIn(msg);
}

async function boot() {
  let r;
  try {
    r = await rpc('ListProjects', {});
  } catch (e) {
    if (e instanceof AuthError) { state.token = ''; LS.set('token', ''); return false; }
    $('#view').replaceChildren(section(null, h('p', { class: 'err' }, t('error', e.message)), h('button', { type: 'button', onclick: boot }, t('retry'))));
    return true;
  }
  LS.set('token', state.token);
  document.body.classList.remove('signed-out');
  state.projects = r.projects || [];
  const saved = LS.get('project');
  state.project = state.projects.includes(saved) ? saved : (state.projects[0] || '');
  const sel = $('#project');
  sel.replaceChildren(...state.projects.map((p) => h('option', { value: p, selected: p === state.project }, p)));
  sel.disabled = state.projects.length < 2;
  if (!state.project) { $('#view').replaceChildren(empty(t('noProject'))); return true; }
  live.lastSeq = 0;
  state.agents.clear();
  connect();
  lastRouteKey = '';
  await render();
  return true;
}

function init() {
  document.documentElement.lang = lang;
  $('.skip').textContent = t('skip');
  $('#project-label').textContent = t('project');
  $('#nav').setAttribute('aria-label', t('screensNav'));
  const lo = $('#logout');
  lo.textContent = t('logout');
  lo.addEventListener('click', () => signOut(''));
  $('#project').addEventListener('change', (e) => {
    state.project = e.target.value;
    LS.set('project', state.project);
    live.lastSeq = 0;
    state.agents.clear();
    connect();
    lastRouteKey = '';
    render();
  });
  window.addEventListener('hashchange', () => { closeSheet(); render(); $('#view').focus({ preventScroll: true }); });
  document.addEventListener('visibilitychange', () => { if (!document.hidden) scheduleRefresh(0); });

  // A token in the address bar wins once and then leaves the history.
  const url = new URL(location.href);
  const fromURL = url.searchParams.get('token');
  if (fromURL) {
    url.searchParams.delete('token');
    history.replaceState(null, '', url.pathname + (url.search ? url.search : '') + url.hash);
  }
  state.token = fromURL || LS.get('token');
  if (!state.token) return showSignIn();
  boot().then((ok) => { if (!ok) showSignIn(t('tokenBad')); });
}

init();
