'use strict';
/* Views: overview, explorer, owners, owner. Shared list helpers. */
const enc = encodeURIComponent;
S.pageBanners = [];

S.shq = (s) => "'" + String(s).replace(/'/g, "'\\''") + "'";

S.pageHead = function pageHead(title, sub, ...right) {
  return S.h('div', { class: 'page-head' },
    S.h('div', null, S.h('h1', null, title), sub && S.h('div', { class: 'sub' }, sub)),
    S.h('div', { class: 'spacer' }), right);
};

// Age histogram: data older than a year is the story, so it gets the accent
// and fresher buckets recede (emphasis form).
S.ageChart = function ageChart(age, labels) {
  const total = age.reduce((a, b) => a + b, 0);
  const old = age.slice(4).reduce((a, b) => a + b, 0);
  const data = labels.map((l, i) => ({ label: l, value: age[i], emph: i >= 4, note: total ? S.fmt.pct(age[i] / total) + ' от объёма' : '' }));
  const chart = S.chart.columns(data, { title: 'Объём данных по времени последнего изменения' });
  return S.h('div', null,
    S.h('p', { class: 'sub' }, 'Старше года (выделено): ', S.h('b', { text: S.fmt.bytes(old) }), total ? ` — ${S.fmt.pct(old / total)} объёма` : ''),
    S.chart.withTable(chart,
      [{ label: 'Возраст', render: (r) => r.label }, { label: 'Объём', num: true, render: (r) => S.fmt.bytes(r.value) }, { label: 'Доля', num: true, render: (r) => (total ? S.fmt.pct(r.value / total) : '—') }],
      data));
};

// Frontier / stale hit list.
S.hitsTable = function hitsTable(hits, ref, opts) {
  opts = opts || {};
  const maxMatch = Math.max(1, ...hits.map((h) => h.match));
  const cols = [
    { label: 'Каталог', cls: 'name', sort: (h) => h.path, asc: true, render: (h) => S.h('span', null,
      S.pathCell(h.path), ' ', S.classBadge(h.class), h.accession && S.h('span', { class: 'badge', text: h.accession }),
      h.errors > 0 && S.h('span', { class: 'badge err', title: 'Внутри есть нечитаемые каталоги', text: '⚠ ' + h.errors }),
      h.direct && S.h('div', { class: 'muted', text: 'только файлы прямо в этом каталоге, без подкаталогов' })) },
    { label: opts.matchLabel || 'Объём', num: true, sort: (h) => h.match, render: (h) => S.fmt.bytes(h.match) },
    { label: '', cls: 'bar-cell', render: (h) => S.hbar(h.match / maxMatch) },
  ];
  if (!opts.noShare) cols.push({ label: 'Доля каталога', num: true, sort: (h) => h.share, render: (h) => (h.direct ? 'файлы здесь' : S.fmt.pct(h.share, 0)) });
  cols.push(
    { label: 'Файлов', num: true, sort: (h) => h.files, render: (h) => S.fmt.compact(h.files) },
    { label: 'Владелец', sort: (h) => (h.top_owner ? h.top_owner.name : ''), asc: true, render: (h) => S.ownerBadge(h.top_owner || h.owner, true) },
    { label: 'Изменялось', num: true, title: 'Самое свежее изменение внутри (относительно даты снимка)', sort: (h) => -h.newest, render: (h) => S.h('span', { title: S.fmt.date(h.newest) }, S.fmt.age(h.newest, ref)) },
  );
  return S.table(cols, hits, { sortCol: 1, empty: opts.empty || 'Ничего не найдено' });
};

S.coverage = function coverage(covered, total, what) {
  if (!total) return null;
  const frac = covered / total;
  return S.h('p', { class: 'sub' },
    `Показанные каталоги содержат ${S.fmt.bytes(covered)} из ${S.fmt.bytes(total)} ${what} (${S.fmt.pct(frac, 0)}).`,
    frac < 0.9 ? ' Остальное разбросано мелкими порциями по смешанным каталогам.' : '');
};

S.filesTable = function filesTable(files, ref, filesMin) {
  const cols = [
    { label: 'Файл', cls: 'name', sort: (f) => f.path, asc: true, render: (f) => S.h('span', null,
      S.pathCell(f.path, { dir: f.path.slice(0, f.path.lastIndexOf('/')) || '/' }), ' ', S.classBadge(f.class),
      f.accession && S.h('span', { class: 'badge', text: f.accession }),
      f.hardlink && S.h('span', { class: 'badge', title: 'У файла несколько жёстких ссылок; место учтено один раз', text: 'hardlink' })) },
    { label: 'Размер', num: true, sort: (f) => f.dsize, render: (f) => S.fmt.bytes(f.dsize) },
    { label: 'Тип', sort: (f) => f.cat_label, asc: true, render: (f) => S.h('span', { class: 'muted', text: f.cat_label }) },
    { label: 'Владелец', sort: (f) => f.owner.name, asc: true, render: (f) => S.ownerBadge(f.owner) },
    { label: 'Изменён', num: true, sort: (f) => -f.mtime, render: (f) => S.h('span', { title: S.fmt.date(f.mtime) }, S.fmt.age(f.mtime, ref)) },
  ];
  return S.h('div', null, S.table(cols, files, { sortCol: 1, empty: 'Нет крупных файлов' }),
    filesMin > 0 && S.h('p', { class: 'muted' }, `Отдельно учитываются только крупнейшие файлы снимка (от ${S.fmt.bytes(filesMin)}); мелкие файлы входят в суммы каталогов.`));
};

/* ---------- Overview ---------- */
S.views.overview = async ({ snap }) => {
  const d = await S.api(`/api/s/${enc(snap.id)}/overview`);
  S.pageBanners.push(...d.warnings);
  const sn = d.snapshot, t = d.totals, r = d.reclaim, ref = sn.time;
  const out = [];
  out.push(S.pageHead(sn.root, `${sn.host ? sn.host + ' · ' : ''}снимок ${S.fmt.date(sn.time)} · ${S.fmt.num(t.files)} файлов · ${S.fmt.num(t.dirs)} каталогов · ${sn.ncdu_version || ''}`));

  // Hero: the one number — how full the disk is.
  let hero;
  if (sn.df) {
    const frac = sn.df.used / sn.df.size;
    const level = frac >= 0.9 ? 'crit' : frac >= 0.8 ? 'warn' : '';
    const pill = frac >= 0.9 ? ['var(--critical)', '⛔ критично мало места'] : frac >= 0.8 ? ['var(--warning)', '⚠ места мало'] : ['var(--good)', '✓ место есть'];
    hero = S.card(null,
      S.h('div', { class: 'hero' },
        S.h('div', null, S.h('div', { class: 'hero-num', text: S.fmt.pct(frac, 0) }), S.h('div', { class: 'hero-label', text: `занято на ${sn.df.mount}` })),
        S.h('div', null, S.h('div', { class: 'tile-value', text: `${S.fmt.bytes(sn.df.used)} из ${S.fmt.bytes(sn.df.size)}` }),
          S.h('div', { class: 'hero-label' }, `свободно ${S.fmt.bytes(sn.df.avail)} · `, S.h('span', { class: 'status-pill' }, S.h('span', { class: 'dot', style: { background: pill[0] } }), pill[1])))),
      S.h('div', { class: 'meter ' + level, role: 'meter', 'aria-valuemin': 0, 'aria-valuemax': 100, 'aria-valuenow': Math.round(frac * 100), 'aria-label': 'Заполненность диска' },
        S.h('div', { class: 'meter-track' }, S.h('div', { class: 'meter-fill', style: { width: (frac * 100).toFixed(1) + '%' } }))),
      S.h('div', { class: 'meter-legend' },
        S.h('span', null, `в снимке (${sn.root}): ${S.fmt.bytes(t.dsize)}`),
        sn.df.used > t.dsize && S.h('span', null, `вне снимка: ${S.fmt.bytes(sn.df.used - t.dsize)}`),
        t.hardlink_dup > 0 && S.h('span', null, `жёсткие ссылки учтены один раз (−${S.fmt.bytes(t.hardlink_dup)})`)));
  } else {
    hero = S.card(null, S.h('div', { class: 'hero' },
      S.h('div', null, S.h('div', { class: 'hero-num', text: S.fmt.bytes(t.dsize) }), S.h('div', { class: 'hero-label', text: `занято в ${sn.root}; объём диска неизвестен (нет .df)` }))));
  }
  out.push(hero);

  const pctOf = (v) => (t.dsize ? S.fmt.pct(v / t.dsize) + ' от снимка' : '');
  out.push(S.h('div', { class: 'tiles' },
    S.tile('Можно сжать, оценка', '≈ ' + S.fmt.bytes(r.compress), 'несжатые FASTQ/SAM/VCF, BAM→CRAM…', S.link('reclaim/compress')),
    S.tile('Публичные данные NCBI/ENA', S.fmt.bytes(r.public), pctOf(r.public), S.link('reclaim/public')),
    S.tile('Референсные геномы', S.fmt.bytes(r.refgenome), pctOf(r.refgenome), S.link('reclaim/refgenome')),
    S.tile('Временное и кэши', S.fmt.bytes(r.temp), 'work/, tmp, .snakemake, core', S.link('reclaim/temp')),
    S.tile('Софт: conda и др.', S.fmt.bytes(r.software), pctOf(r.software), S.link('reclaim/software')),
    S.tile('Не менялось больше года', S.fmt.bytes(r.old), pctOf(r.old), S.link('reclaim/stale')),
    S.tile('Файлы удалённых пользователей', S.fmt.bytes(r.orphan), r.orphan_users ? `${r.orphan_users} uid без учётной записи` : 'нет', S.link('owners', { orphan: 1 })),
    S.tile('Дубликаты от 100 МБ', S.fmt.bytes(r.dups), 'одинаковые имя и размер', S.link('reclaim/dups')),
    S.tile('Корзина и копии', S.fmt.bytes(r.trash), '.Trash, *.bak, *~', S.link('reclaim/trash'))));

  const owners = d.owners.slice(0, 10);
  const maxO = Math.max(1, ...owners.map((o) => o.dsize));
  const cats = d.cats.filter((c) => c.key !== 'dirmeta').slice(0, 10);
  const maxC = Math.max(1, ...cats.map((c) => c.dsize));
  out.push(S.h('div', { class: 'grid' },
    S.card('Кто сколько занимает',
      S.table([
        { label: 'Владелец', render: (o) => S.ownerBadge(o) },
        { label: 'Объём', num: true, render: (o) => S.fmt.bytes(o.dsize) },
        { label: '', cls: 'bar-cell', render: (o) => S.hbar(o.dsize / maxO) },
        { label: 'Доля', num: true, render: (o) => S.fmt.pct(o.share) },
        { label: 'Старше года', num: true, title: 'Доля данных владельца, не менявшихся больше года', render: (o) => (o.dsize ? S.fmt.pct(o.old / o.dsize, 0) : '—') },
      ], owners, { onRow: (o) => S.go('owner/' + o.uid) }),
      S.h('p', null, S.a('owners', null, `Все владельцы (${d.owners.length}) →`))),
    S.card('Типы файлов',
      S.table([
        { label: 'Тип', render: (c) => S.a('type/' + c.key, null, c.label) },
        { label: 'Объём', num: true, render: (c) => S.fmt.bytes(c.dsize) },
        { label: '', cls: 'bar-cell', render: (c) => S.hbar(c.dsize / maxC) },
        { label: 'Сжатие', num: true, title: 'Оценка освобождаемого места', render: (c) => (c.est_save ? '≈ ' + S.fmt.bytes(c.est_save) : '') },
      ], cats, { onRow: (c) => S.go('type/' + c.key) }),
      S.h('p', null, S.a('types', null, 'Все типы →')))));

  const maxT = Math.max(1, ...d.top.map((x) => x.dsize));
  out.push(S.h('div', { class: 'grid' },
    S.card('Возраст данных', S.ageChart(d.age.map((a) => a.dsize), d.age.map((a) => a.label))),
    S.card('Крупнейшие каталоги верхнего уровня',
      S.table([
        { label: 'Каталог', cls: 'name', render: (x) => S.h('span', null, S.dirLink(x.path, x.name), ' ', S.classBadge(x.class), x.errors > 0 && S.h('span', { class: 'badge err', text: '⚠ ' + x.errors })) },
        { label: 'Объём', num: true, render: (x) => S.fmt.bytes(x.dsize) },
        { label: '', cls: 'bar-cell', render: (x) => S.hbar(x.dsize / maxT) },
        { label: 'Владелец', render: (x) => S.ownerBadge(x.top_owner, true) },
        { label: 'Изменялось', num: true, render: (x) => S.fmt.age(x.newest, ref) },
      ], d.top, { onRow: (x) => S.go('explore', { path: x.path }) }),
      S.h('p', null, S.a('explore', null, 'Открыть проводник →')))));
  return S.h('div', null, out);
};

/* ---------- Explorer ---------- */
S.treemapItem = function treemapItem(c, ref) {
  const g = S.classGroup[c.class];
  return {
    name: c.name, value: c.dsize,
    color: g ? g.color : 'var(--neutral-fill)', ink: g ? g.ink : 'var(--ink)',
    onClick: c.has_kids || c.files ? () => S.go('explore', { path: c.path }) : null,
    tip: () => S.tip.value(S.fmt.bytes(c.dsize), c.name, [
      S.h('div', { class: 'tl', text: `${S.fmt.num(c.files)} файлов · изменялось ${S.fmt.age(c.newest, ref)} назад` }),
      c.top_owner && S.h('div', { class: 'tl', text: `в основном ${c.top_owner.name} (${S.fmt.pct(c.top_owner.share, 0)})` }),
      g && S.h('div', { class: 'tl', text: g.label }),
    ]),
  };
};

S.treemapLegend = function treemapLegend() {
  const seen = new Set();
  const keys = [];
  for (const g of Object.values(S.classGroup)) {
    if (seen.has(g.label)) continue;
    seen.add(g.label);
    keys.push(S.h('span', null, S.h('span', { class: 'key', style: { background: g.color } }), g.label));
  }
  keys.push(S.h('span', null, S.h('span', { class: 'key', style: { background: 'var(--neutral-fill)' } }), 'остальное'));
  return S.h('div', { class: 'legend' }, keys);
};

S.views.explore = async ({ snap, query }) => {
  const path = query.get('path') || '';
  const d = await S.api(`/api/s/${enc(snap.id)}/dir?path=${enc(path)}`);
  const dir = d.dir, ref = snap.time;
  if (dir.read_error) S.pageBanners.push({ level: 'error', text: 'ncdu не смог прочитать этот каталог даже под sudo: его содержимое не учтено.' });
  else if (dir.errors > 0) S.pageBanners.push({ level: 'warn', text: `Внутри ${dir.errors} нечитаемых каталогов (метка ⚠): их содержимое не учтено.` });

  const crumbs = S.h('nav', { class: 'crumbs', 'aria-label': 'Путь' });
  d.crumbs.forEach((c, i) => {
    if (i) crumbs.appendChild(S.h('span', { class: 'sep', text: '/' }));
    crumbs.appendChild(i === d.crumbs.length - 1 ? S.h('b', { text: c.name }) : S.dirLink(c.path, c.name));
  });
  crumbs.appendChild(S.copyBtn(dir.path));

  const items = d.children.map((c) => S.treemapItem(c, ref));
  if (dir.own_dsize > 0) items.push({ name: `файлы здесь (${S.fmt.num(dir.own_files)})`, value: dir.own_dsize, color: 'var(--surface-2)', ink: 'var(--ink-2)' });

  const maxK = Math.max(1, ...d.children.map((c) => c.dsize));
  const table = S.table([
    { label: 'Каталог', cls: 'name', sort: (c) => c.name, asc: true, render: (c) => S.h('span', null, S.dirLink(c.path, c.name), ' ', S.classBadge(c.class),
      c.read_error ? S.h('span', { class: 'badge err', text: '⚠ не прочитан' }) : c.errors > 0 && S.h('span', { class: 'badge err', title: 'Нечитаемых каталогов внутри', text: '⚠ ' + c.errors })) },
    { label: 'Объём', num: true, sort: (c) => c.dsize, render: (c) => S.fmt.bytes(c.dsize) },
    { label: '', cls: 'bar-cell', render: (c) => S.hbar(c.dsize / maxK) },
    { label: '%', num: true, sort: (c) => c.dsize, render: (c) => (dir.dsize ? S.fmt.pct(c.dsize / dir.dsize) : '') },
    { label: 'Файлов', num: true, sort: (c) => c.files, render: (c) => S.fmt.compact(c.files) },
    { label: 'Владелец', sort: (c) => (c.top_owner ? c.top_owner.name : ''), asc: true, render: (c) => S.ownerBadge(c.top_owner, true) },
    { label: 'Изменялось', num: true, sort: (c) => -c.newest, render: (c) => S.h('span', { title: S.fmt.date(c.newest) }, S.fmt.age(c.newest, ref)) },
  ], d.children, { sortCol: 1, onRow: (c) => S.go('explore', { path: c.path }), empty: 'Подкаталогов нет' });

  const maxO = Math.max(1, ...d.owners.map((o) => o.dsize));
  const cats = d.cats.filter((c) => c.key !== 'dirmeta');
  const maxC = Math.max(1, ...cats.map((c) => c.dsize));
  return S.h('div', null,
    crumbs,
    S.pageHead(dir.name, [`${S.fmt.bytes(dir.dsize)} · ${S.fmt.num(dir.files)} файлов · ${S.fmt.num(dir.dirs)} подкаталогов · последнее изменение ${S.fmt.day(dir.newest)} (${S.fmt.ago(dir.newest, ref)}) · владелец каталога `, S.ownerBadge(dir.owner)]),
    S.card(null, S.treemapLegend(), S.chart.treemap(items, { title: 'Подкаталоги по размеру' })),
    S.card(null, table,
      d.more_kids > 0 && S.h('p', { class: 'muted', text: `…и ещё ${d.more_kids} подкаталогов поменьше.` }),
      dir.own_files > 0 && S.h('p', { class: 'sub', text: `Файлы прямо в этом каталоге: ${S.fmt.num(dir.own_files)} шт., ${S.fmt.bytes(dir.own_dsize)}.` })),
    S.h('div', { class: 'grid' },
      S.card('Владельцы здесь', S.table([
        { label: 'Владелец', render: (o) => S.ownerBadge(o) },
        { label: 'Объём', num: true, render: (o) => S.fmt.bytes(o.dsize) },
        { label: '', cls: 'bar-cell', render: (o) => S.hbar(o.dsize / maxO) },
        { label: 'Доля', num: true, render: (o) => S.fmt.pct(o.share) },
      ], d.owners)),
      S.card('Типы файлов здесь', S.table([
        { label: 'Тип', render: (c) => S.a('type/' + c.key, null, c.label) },
        { label: 'Объём', num: true, render: (c) => S.fmt.bytes(c.dsize) },
        { label: '', cls: 'bar-cell', render: (c) => S.hbar(c.dsize / maxC) },
      ], cats))),
    S.card('Крупные файлы прямо в этом каталоге', S.filesTable(d.files, ref, d.files_min)));
};

/* ---------- Owners ---------- */
S.views.owners = async ({ snap, query }) => {
  const d = await S.api(`/api/s/${enc(snap.id)}/owners`);
  const onlyOrphan = query.get('orphan') === '1';
  if (!d.has_users) S.pageBanners.push({ level: 'warn', text: 'Нет файла .passwd рядом со снимком: имена и удалённых пользователей определить нельзя.' });
  const rows = onlyOrphan ? d.owners.filter((o) => o.orphan) : d.owners;
  const total = d.owners.reduce((a, o) => a + o.dsize, 0);
  const maxO = Math.max(1, ...rows.map((o) => o.dsize));
  const orphanBytes = d.owners.filter((o) => o.orphan).reduce((a, o) => a + o.dsize, 0);
  return S.h('div', null,
    S.pageHead('Владельцы', `${d.owners.length} uid · ${S.fmt.bytes(total)}`,
      S.h('div', { class: 'tabs' },
        S.h('a', { href: S.link('owners'), class: onlyOrphan ? '' : 'active' }, 'Все'),
        S.h('a', { href: S.link('owners', { orphan: 1 }), class: onlyOrphan ? 'active' : '' }, 'Удалённые пользователи', S.h('span', { class: 'tab-num', text: S.fmt.bytes(orphanBytes) })))),
    onlyOrphan && S.h('div', { class: 'hint' }, S.h('b', null, 'Удалённые пользователи'), ' — это uid, которых нет в /etc/passwd. Их данные никто не видит в своих квотах. Откройте владельца: там есть список каталогов и готовые команды ', S.h('code', null, 'sudo chown'), ' для передачи данных.'),
    S.card(null, S.table([
      { label: 'Владелец', sort: (o) => o.name, asc: true, render: (o) => S.ownerBadge(o) },
      { label: 'uid', num: true, sort: (o) => o.uid, render: (o) => o.uid },
      { label: 'Объём', num: true, sort: (o) => o.dsize, render: (o) => S.fmt.bytes(o.dsize) },
      { label: '', cls: 'bar-cell', render: (o) => S.hbar(o.dsize / maxO) },
      { label: 'Доля', num: true, sort: (o) => o.dsize, render: (o) => S.fmt.pct(o.share) },
      { label: 'Файлов', num: true, sort: (o) => o.count, render: (o) => S.fmt.compact(o.count) },
      { label: 'Старше года', num: true, sort: (o) => (o.dsize ? o.old / o.dsize : 0), render: (o) => (o.dsize ? S.fmt.pct(o.old / o.dsize, 0) : '—') },
    ], rows, { sortCol: 2, onRow: (o) => S.go('owner/' + o.uid), empty: onlyOrphan ? 'Удалённых пользователей нет' : 'Пусто' })));
};

S.views.owner = async ({ snap, args }) => {
  const uid = args[0];
  const d = await S.api(`/api/s/${enc(snap.id)}/owner/${enc(uid)}`);
  const ref = snap.time;
  const old = d.age ? d.age.slice(4).reduce((a, b) => a + b, 0) : 0;
  const out = [
    S.pageHead(S.h('span', null, d.owner.name, d.owner.orphan && [' ', S.h('span', { class: 'badge orphan', text: 'удалён' })]), `uid ${d.owner.uid}`),
    S.h('div', { class: 'tiles' },
      S.tile('Занимает', S.fmt.bytes(d.dsize), S.fmt.pct(d.share) + ' снимка'),
      S.tile('Файлов', S.fmt.num(d.count)),
      S.tile('Не менялось больше года', S.fmt.bytes(old), d.dsize ? S.fmt.pct(old / d.dsize, 0) + ' данных владельца' : '')),
  ];
  if (d.owner.orphan && d.dirs.length) {
    const who = S.h('input', { type: 'text', placeholder: 'новый владелец, напр. ivanov', 'aria-label': 'Новый владелец', size: 24 });
    const grp = S.h('input', { type: 'text', placeholder: 'группа (необязательно)', 'aria-label': 'Группа', size: 20 });
    const cmds = S.h('code', { class: 'cmd' });
    const copyWrap = S.h('span');
    const upd = () => {
      const u = who.value.trim() || 'НОВЫЙ_ВЛАДЕЛЕЦ';
      const g = grp.value.trim();
      const text = d.dirs.filter((h) => !h.direct).map((h) => `sudo chown -R ${u}${g ? ':' + g : ''} ${S.shq(h.path)}`).join('\n');
      cmds.textContent = text || '# все найденные данные лежат в смешанных каталогах — передавайте их по файлам';
      copyWrap.replaceChildren(S.copyBtn(text, 'скопировать команды'));
    };
    who.addEventListener('input', upd); grp.addEventListener('input', upd); upd();
    out.push(S.card('Передать данные другому пользователю',
      S.h('p', { class: 'sub' }, 'Учётной записи с этим uid больше нет. Команды ниже только сгенерированы: проверьте список каталогов и выполните их сами.'),
      S.h('div', { class: 'row' }, who, grp, copyWrap), cmds));
  }
  out.push(S.card('Где лежат данные', S.coverage(d.covered, d.dsize, 'владельца'), S.hitsTable(d.dirs, ref, { matchLabel: 'Данные владельца' })));
  if (d.age) out.push(S.card('Возраст данных владельца', S.ageChart(d.age, d.age_labels)));
  out.push(S.card('Крупнейшие файлы владельца', S.filesTable(d.files, ref, 0)));
  return S.h('div', null, out);
};
