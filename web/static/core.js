'use strict';
/* Core helpers: DOM, formatting, API, tooltip, banners, tables. */
const S = (window.S = { views: {}, state: { snaps: [], dir: '', series: null, snapId: null } });

/* ---------- DOM ---------- */
S.h = function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v == null || v === false) continue;
      if (k === 'class') el.className = v;
      else if (k === 'text') el.textContent = v;
      else if (k === 'style' && typeof v === 'object') Object.assign(el.style, v);
      else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
      else if (k === 'dataset') Object.assign(el.dataset, v);
      else el.setAttribute(k, v === true ? '' : v);
    }
  }
  S.append(el, kids);
  return el;
};
S.append = function append(el, kids) {
  for (const k of kids.flat(Infinity)) {
    if (k == null || k === false) continue;
    el.appendChild(k instanceof Node ? k : document.createTextNode(String(k)));
  }
  return el;
};
S.svg = function svg(tag, attrs, ...kids) {
  const el = document.createElementNS('http://www.w3.org/2000/svg', tag);
  if (attrs) for (const [k, v] of Object.entries(attrs)) if (v != null) el.setAttribute(k, v);
  for (const k of kids.flat(Infinity)) {
    if (k == null) continue;
    el.appendChild(k instanceof Node ? k : document.createTextNode(String(k)));
  }
  return el;
};

/* ---------- formatting ---------- */
const UNITS = ['Б', 'КБ', 'МБ', 'ГБ', 'ТБ', 'ПБ'];
S.fmt = {
  bytes(n) {
    if (n == null || isNaN(n)) return '—';
    const neg = n < 0; let v = Math.abs(n); let i = 0;
    while (v >= 1024 && i < UNITS.length - 1) { v /= 1024; i++; }
    const s = i === 0 ? String(v) : v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2);
    return (neg ? '−' : '') + s.replace('.', ',') + ' ' + UNITS[i];
  },
  signedBytes(n) { return (n > 0 ? '+' : '') + S.fmt.bytes(n); },
  num(n) { return n == null ? '—' : Math.round(n).toLocaleString('ru-RU'); },
  compact(n) {
    const a = Math.abs(n);
    if (a >= 1e9) return (n / 1e9).toFixed(1).replace('.', ',') + ' млрд';
    if (a >= 1e6) return (n / 1e6).toFixed(1).replace('.', ',') + ' млн';
    if (a >= 1e4) return (n / 1e3).toFixed(0) + ' тыс';
    return S.fmt.num(n);
  },
  pct(x, digits) {
    if (x == null || isNaN(x)) return '—';
    const p = x * 100;
    if (p > 0 && p < 0.01) return '<0,01%';
    const d = digits != null ? digits : p < 1 && p > 0 ? 2 : p < 10 ? 1 : 0;
    return p.toFixed(d).replace('.', ',') + '%';
  },
  date(ts) {
    if (!ts) return '—';
    return new Date(ts * 1000).toLocaleString('ru-RU', { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  },
  day(ts) {
    if (!ts) return '—';
    return new Date(ts * 1000).toLocaleDateString('ru-RU', { year: 'numeric', month: 'short', day: 'numeric' });
  },
  // Age relative to a reference time (the scan time), not to "now".
  age(ts, ref) {
    if (!ts) return '—';
    const days = Math.max(0, (ref - ts) / 86400);
    if (days < 1) return 'сегодня';
    if (days < 31) return Math.round(days) + ' дн';
    if (days < 365) return Math.round(days / 30.4) + ' мес';
    const y = days / 365.25;
    return (y < 10 ? y.toFixed(1).replace('.', ',') : Math.round(y)) + ' г';
  },
  // "3 мес назад" / "сегодня"
  ago(ts, ref) {
    const a = S.fmt.age(ts, ref);
    return a === 'сегодня' || a === '—' ? a : a + ' назад';
  },
  // Short axis date: "10 сент." (+ year when the range spans years).
  axisDay(ts, withYear) {
    const o = withYear ? { day: 'numeric', month: 'short', year: '2-digit' } : { day: 'numeric', month: 'short' };
    return new Date(ts * 1000).toLocaleDateString('ru-RU', o);
  },
  plural(n, one, few, many) {
    const m10 = n % 10, m100 = n % 100;
    if (m10 === 1 && m100 !== 11) return one;
    if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return few;
    return many;
  },
};

/* ---------- API ---------- */
S.api = async function api(path) {
  let resp;
  try {
    resp = await fetch(path, { headers: { Accept: 'application/json' } });
  } catch (e) {
    throw new Error('Сервер недоступен (SSH-туннель закрыт?): ' + e.message);
  }
  const text = await resp.text();
  let body;
  try {
    body = JSON.parse(text);
  } catch (e) {
    throw new Error(`Некорректный ответ сервера (${resp.status}): ${text.slice(0, 200)}`);
  }
  if (!resp.ok) {
    const err = new Error(body.error || `HTTP ${resp.status}`);
    err.status = resp.status;
    err.snapStatus = body.status;
    throw err;
  }
  return body;
};

/* ---------- routing helpers ---------- */
S.link = function link(route, params) {
  const q = new URLSearchParams();
  if (S.state.snapId) q.set('s', S.state.snapId);
  for (const [k, v] of Object.entries(params || {})) if (v != null && v !== '') q.set(k, v);
  const qs = q.toString();
  return '#/' + route + (qs ? '?' + qs : '');
};
S.go = function go(route, params) { location.hash = S.link(route, params); };
S.a = function a(route, params, ...kids) { return S.h('a', { href: S.link(route, params) }, ...kids); };
S.dirLink = function dirLink(path, label) { return S.a('explore', { path }, label || path); };

/* ---------- storage (per-viewer convenience only) ---------- */
S.store = {
  get(k) { try { return localStorage.getItem('space.' + k); } catch (e) { return null; } },
  set(k, v) { try { localStorage.setItem('space.' + k, v); } catch (e) { /* private mode: fine */ } },
};

/* ---------- tooltip ---------- */
const tipEl = () => document.getElementById('tip');
S.tip = {
  show(evt, ...content) {
    const t = tipEl();
    t.replaceChildren();
    S.append(t, content);
    t.hidden = false;
    S.tip.move(evt);
  },
  move(evt) {
    const t = tipEl();
    if (t.hidden) return;
    let x, y;
    if (evt.clientX != null && evt.type !== 'focus') { x = evt.clientX; y = evt.clientY; }
    else { const r = evt.target.getBoundingClientRect(); x = r.left + r.width / 2; y = r.top; }
    const w = t.offsetWidth, hgt = t.offsetHeight;
    let left = x + 14, top = y + 14;
    if (left + w > window.innerWidth - 8) left = x - w - 14;
    if (top + hgt > window.innerHeight - 8) top = y - hgt - 14;
    t.style.left = Math.max(8, left) + 'px';
    t.style.top = Math.max(8, top) + 'px';
  },
  hide() { tipEl().hidden = true; },
  // Attach a tooltip (built lazily by fn) to an element for hover and focus.
  bind(el, fn) {
    el.addEventListener('pointerenter', (e) => S.tip.show(e, fn()));
    el.addEventListener('pointermove', (e) => S.tip.move(e));
    el.addEventListener('pointerleave', S.tip.hide);
    el.addEventListener('focus', (e) => S.tip.show(e, fn()));
    el.addEventListener('blur', S.tip.hide);
    return el;
  },
  value(v, label, extra) {
    return [S.h('div', { class: 'tv', text: v }), label && S.h('div', { class: 'tl', text: label }), extra];
  },
};

/* ---------- banners ---------- */
const ICON = { error: '!', warn: '⚠', info: 'i' };
S.banner = function banner(level, text, extra) {
  return S.h('div', { class: 'banner ' + level, role: level === 'error' ? 'alert' : 'status' },
    S.h('span', { class: 'ico', 'aria-hidden': 'true', text: ICON[level] || 'i' }),
    S.h('div', null, text, extra));
};
S.setBanners = function setBanners(list) {
  const el = document.getElementById('banners');
  el.replaceChildren(...list.map((w) => S.banner(w.level, w.text)));
};
S.errorView = function errorView(err) {
  if (err.status === 409) {
    return S.h('div', { class: 'card' },
      S.banner('info', 'Этот снимок ещё не проиндексирован (статус: ' + (err.snapStatus || '?') + ').'),
      S.h('p', null, 'Индексация идёт в фоне; прогресс виден на странице ', S.a('snapshots', null, 'Снимки'), '.'));
  }
  return S.h('div', { class: 'card' }, S.banner('error', err.message || String(err)));
};

/* ---------- small components ---------- */
S.copyBtn = function copyBtn(text, label) {
  const b = S.h('button', { class: 'copy', type: 'button', title: 'Скопировать', 'aria-label': 'Скопировать ' + (label || '') }, label || '⧉');
  b.addEventListener('click', async (e) => {
    e.stopPropagation();
    try {
      await navigator.clipboard.writeText(text);
      b.textContent = '✓';
    } catch (err) {
      // Clipboard API unavailable (non-secure origin): select for manual copy.
      window.prompt('Скопируйте вручную:', text);
    }
    setTimeout(() => { b.textContent = label || '⧉'; }, 1200);
  });
  return b;
};
S.pathCell = function pathCell(path, opts) {
  opts = opts || {};
  return S.h('span', null, opts.link === false ? S.h('span', { class: 'mono', text: path }) : S.h('a', { class: 'mono', href: S.link('explore', { path: opts.dir || path }), text: path }), ' ', S.copyBtn(path));
};
S.ownerBadge = function ownerBadge(o, withShare) {
  if (!o) return '—';
  const kids = [S.a('owner/' + o.uid, null, o.name)];
  if (withShare && o.share != null && o.share < 0.995) kids.push(S.h('span', { class: 'muted', text: ' ' + S.fmt.pct(o.share, 0) }));
  if (o.orphan) kids.push(' ', S.h('span', { class: 'badge orphan', title: 'uid нет в /etc/passwd: пользователь удалён', text: 'удалён' }));
  return S.h('span', { class: 'nowrap' }, kids);
};

// Class → treemap/badge color group. Three colored groups only (all-pairs
// validated), everything else neutral.
S.classGroup = {
  public: { color: 'var(--s1)', label: 'Публичные / референсы', ink: '#fff' },
  refgenome: { color: 'var(--s1)', label: 'Публичные / референсы', ink: '#fff' },
  temp: { color: 'var(--s2)', label: 'Временное / корзина', ink: '#fff' },
  trash: { color: 'var(--s2)', label: 'Временное / корзина', ink: '#fff' },
  software: { color: 'var(--s3)', label: 'Софт (conda и т.п.)', ink: '#0b0b0b' },
};
S.classNames = {
  public: 'NCBI/ENA', refgenome: 'референс', temp: 'временное', software: 'софт', trash: 'корзина',
};
S.classBadge = function classBadge(key) {
  if (!key || key === 'none') return null;
  const g = S.classGroup[key];
  return S.h('span', { class: 'badge', title: g ? g.label : key },
    g && S.h('span', { class: 'sw', style: { background: g.color } }), S.classNames[key] || key);
};

S.hbar = function hbar(frac, neutral) {
  const w = Math.max(0, Math.min(1, frac || 0)) * 100;
  return S.h('div', { class: 'hbar' + (neutral ? ' neutral' : ''), 'aria-hidden': 'true' },
    S.h('span', { style: { width: w.toFixed(2) + '%' } }));
};

/* ---------- sortable table ---------- */
// cols: [{label, key?, num?, cls?, render(row), sort?(row) -> value, title?}]
S.table = function table(cols, rows, opts) {
  opts = opts || {};
  let sortCol = opts.sortCol != null ? opts.sortCol : -1;
  let asc = false;
  const tbody = S.h('tbody');
  const ths = cols.map((c, i) => {
    const th = S.h('th', { class: (c.num ? 'num ' : '') + (c.sort ? 'sortable' : ''), title: c.title, scope: 'col' }, c.label);
    if (c.sort) {
      th.tabIndex = 0;
      const act = () => {
        if (sortCol === i) asc = !asc; else { sortCol = i; asc = !!c.asc; }
        draw();
      };
      th.addEventListener('click', act);
      th.addEventListener('keydown', (e) => { if (e.key === 'Enter') act(); });
    }
    return th;
  });
  function draw() {
    let data = rows.slice();
    if (sortCol >= 0 && cols[sortCol].sort) {
      const f = cols[sortCol].sort;
      data.sort((a, b) => {
        const x = f(a), y = f(b);
        const r = typeof x === 'string' ? x.localeCompare(y, 'ru') : x - y;
        return asc ? r : -r;
      });
    }
    ths.forEach((th, i) => { th.classList.toggle('sorted', i === sortCol); th.classList.toggle('asc', i === sortCol && asc); });
    tbody.replaceChildren(...data.map((row) => {
      const tr = S.h('tr', { class: opts.onRow ? 'clickable' : null },
        cols.map((c) => S.h('td', { class: (c.num ? 'num ' : '') + (c.cls || '') }, c.render(row))));
      if (opts.onRow) tr.addEventListener('click', (e) => { if (!e.target.closest('a,button')) opts.onRow(row); });
      return tr;
    }));
    if (!data.length) tbody.appendChild(S.h('tr', null, S.h('td', { colspan: cols.length, class: 'empty', text: opts.empty || 'Пусто' })));
  }
  draw();
  return S.h('div', { class: 'table-wrap' }, S.h('table', null, S.h('thead', null, S.h('tr', null, ths)), tbody));
};

S.card = function card(title, ...kids) {
  return S.h('section', { class: 'card' }, title && S.h('h2', null, title), kids);
};
S.tile = function tile(label, value, note, href) {
  return S.h(href ? 'a' : 'div', { class: 'tile', href },
    S.h('div', { class: 'tile-label', text: label }),
    S.h('div', { class: 'tile-value', text: value }),
    note && S.h('div', { class: 'tile-note', text: note }));
};
