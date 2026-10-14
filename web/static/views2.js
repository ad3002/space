'use strict';
/* Views: file types, reclaim, trend, diff, snapshots. */

const HOWTO = {
  fastq: 'pigz -p 8 reads.fastq            # или: zstd -T8 --rm reads.fastq',
  sam: 'samtools view -@8 -b -o aln.bam aln.sam   # ещё меньше: -C -T ref.fa -o aln.cram',
  bam: 'samtools view -@8 -C -T ref.fa -o aln.cram aln.bam && samtools index aln.cram',
  vcf: 'bgzip -@8 calls.vcf && tabix -p vcf calls.vcf.gz',
  fasta: 'bgzip -@8 ref.fa && samtools faidx ref.fa.gz',
  annot: 'bgzip genes.gtf && tabix -p gff genes.gtf.gz   # wig/bedGraph → bedGraphToBigWig',
  text: 'zstd -T8 --rm table.tsv',
  tar_raw: 'zstd -T8 --rm archive.tar',
  raw_signal: 'pod5 convert fast5 run/*.fast5 --output run.pod5',
  core: 'file core.12345   # убедиться, что это дамп, и удалить',
};

/* ---------- Types ---------- */
S.views.types = async ({ snap }) => {
  const d = await S.api(`/api/s/${enc(snap.id)}/cats`);
  const maxC = Math.max(1, ...d.cats.map((c) => c.dsize));
  const save = d.cats.reduce((a, c) => a + c.est_save, 0);
  return S.h('div', null,
    S.pageHead('Типы файлов', `${S.fmt.bytes(d.total)} · можно сжать ≈ ${S.fmt.bytes(save)}`),
    S.card(null, S.table([
      { label: 'Тип', sort: (c) => c.label, asc: true, render: (c) => S.a('type/' + c.key, null, c.label) },
      { label: 'Объём', num: true, sort: (c) => c.dsize, render: (c) => S.fmt.bytes(c.dsize) },
      { label: '', cls: 'bar-cell', render: (c) => S.hbar(c.dsize / maxC) },
      { label: 'Доля', num: true, sort: (c) => c.dsize, render: (c) => S.fmt.pct(c.dsize / d.total) },
      { label: 'Файлов', num: true, sort: (c) => c.count, render: (c) => S.fmt.compact(c.count) },
      { label: 'Сжатие, оценка', num: true, sort: (c) => c.est_save, render: (c) => (c.est_save ? '≈ ' + S.fmt.bytes(c.est_save) : '') },
    ], d.cats, { sortCol: 1, onRow: (c) => S.go('type/' + c.key) })));
};

S.views.type = async ({ snap, args }) => {
  const d = await S.api(`/api/s/${enc(snap.id)}/cat/${enc(args[0])}`);
  const ref = snap.time;
  const out = [S.pageHead(d.cat.label, `${S.fmt.bytes(d.dsize)} · ${S.fmt.num(d.count)} файлов`)];
  if (d.cat.compress) {
    out.push(S.h('div', { class: 'hint' },
      S.h('b', null, 'Как уменьшить: '), d.cat.compress, '. ',
      `Оценка экономии ≈ ${S.fmt.bytes(d.est_save)} (типичный коэффициент ${S.fmt.pct(d.cat.save_frac, 0)}; реальный зависит от данных).`,
      HOWTO[d.cat.key] && S.h('code', { class: 'cmd', text: HOWTO[d.cat.key] })));
  }
  out.push(S.card('Где лежат', S.coverage(d.covered, d.dsize, 'этого типа'), S.hitsTable(d.dirs, ref, { matchLabel: 'Этого типа' })));
  out.push(S.h('div', { class: 'grid' },
    S.card('Возраст', S.ageChart(d.age, d.age_labels)),
    S.card('Крупнейшие файлы', S.filesTable(d.files.slice(0, 20), ref, 0))));
  return S.h('div', null, out);
};

/* ---------- Reclaim ---------- */
const RECLAIM_TABS = [
  ['compress', 'Сжатие'], ['public', 'NCBI / ENA'], ['refgenome', 'Референсы'], ['temp', 'Временное'],
  ['software', 'Софт'], ['trash', 'Корзина'], ['stale', 'Давно не менялось'], ['dups', 'Дубликаты'],
];

S.views.reclaim = async ({ snap, args, query }) => {
  const tab = args[0] || 'compress';
  const tabs = S.h('div', { class: 'tabs', role: 'tablist' },
    RECLAIM_TABS.map(([k, l]) => S.h('a', { href: S.link('reclaim/' + k), class: k === tab ? 'active' : '', role: 'tab', 'aria-selected': k === tab ? 'true' : 'false' }, l)));
  let body;
  if (tab === 'compress') body = await reclaimCompress(snap);
  else if (tab === 'stale') body = await reclaimStale(snap, query);
  else if (tab === 'dups') body = await reclaimDups(snap, query);
  else body = await reclaimClass(snap, tab);
  return S.h('div', null, S.pageHead('Что можно освободить', 'Всё ниже — эвристики по именам, путям и датам. Решение принимает человек.'), tabs, body);
};

async function reclaimCompress(snap) {
  const d = await S.api(`/api/s/${enc(snap.id)}/cats`);
  const rows = d.cats.filter((c) => c.est_save > 0).sort((a, b) => b.est_save - a.est_save);
  const total = rows.reduce((a, c) => a + c.est_save, 0);
  const maxS = Math.max(1, ...rows.map((c) => c.est_save));
  return S.h('div', null,
    S.h('div', { class: 'tiles' }, S.tile('Можно освободить сжатием, оценка', '≈ ' + S.fmt.bytes(total), 'по типичным коэффициентам сжатия')),
    S.card(null, S.table([
      { label: 'Тип', render: (c) => S.a('type/' + c.key, null, c.label) },
      { label: 'Сейчас', num: true, render: (c) => S.fmt.bytes(c.dsize) },
      { label: 'Освободится ≈', num: true, render: (c) => S.fmt.bytes(c.est_save) },
      { label: '', cls: 'bar-cell', render: (c) => S.hbar(c.est_save / maxS) },
      { label: 'Как', render: (c) => S.h('span', { class: 'sub', text: c.compress }) },
    ], rows, { onRow: (c) => S.go('type/' + c.key), empty: 'Несжатых данных известных типов не найдено' })));
}

async function reclaimClass(snap, key) {
  const d = await S.api(`/api/s/${enc(snap.id)}/class/${enc(key)}`);
  const ref = snap.time;
  const extra = [];
  if (key === 'public') {
    const accs = [...new Set(d.dirs.map((h) => h.accession).concat(d.files.map((f) => f.accession)).filter(Boolean))].slice(0, 5);
    extra.push(S.h('code', { class: 'cmd', text:
      '# SRA/ENA/DDBJ run (SRR/ERR/DRR):\nprefetch SRR1234567 && fasterq-dump --split-files SRR1234567\n' +
      '# сборки GenBank/RefSeq (GCA_/GCF_):\ndatasets download genome accession GCF_000001405.40' }));
    if (accs.length) extra.push(S.h('p', { class: 'sub' }, 'Проверить accession в архиве: ',
      accs.map((a, i) => [i ? ', ' : '', S.h('a', { href: 'https://www.ncbi.nlm.nih.gov/search/all/?term=' + enc(a), target: '_blank', rel: 'noopener', text: a })])));
  }
  return S.h('div', null,
    S.h('div', { class: 'hint' }, S.h('b', null, d.class.label + '. '), d.class.hint, extra),
    S.h('div', { class: 'tiles' }, S.tile('Всего', S.fmt.bytes(d.dsize), S.fmt.num(d.count) + ' файлов')),
    S.card('Каталоги', S.coverage(d.covered, d.dsize, 'этой группы'), S.hitsTable(d.dirs, ref)),
    S.card('Крупнейшие файлы', S.filesTable(d.files, ref, 0)));
}

function selectParam(label, name, value, options, onChange) {
  const sel = S.h('select', { 'aria-label': label },
    options.map(([v, l]) => S.h('option', { value: v, selected: String(v) === String(value) }, l)));
  sel.addEventListener('change', () => onChange(sel.value));
  return S.h('label', { class: 'row', style: { gap: '6px' } }, S.h('span', { class: 'muted', text: label }), sel);
}

async function reclaimStale(snap, query) {
  const days = query.get('days') || '365';
  const min = query.get('min') || ''; // empty: server picks a floor relative to the snapshot size
  const d = await S.api(`/api/s/${enc(snap.id)}/stale?days=${enc(days)}` + (min ? `&min=${enc(min)}` : ''));
  const go = (k, v) => S.go('reclaim/stale', Object.assign({ days, min }, { [k]: v }));
  const minOpts = [['', `авто (от ${S.fmt.bytes(d.min)})`], [100 << 20, '100 МБ'], [1 << 30, '1 ГБ'], [10 * (1 << 30), '10 ГБ'], [100 * (1 << 30), '100 ГБ']];
  return S.h('div', null,
    S.h('div', { class: 'hint' }, 'Каталоги, внутри которых ', S.h('b', null, 'ни один файл не менялся'), ' дольше выбранного срока (по mtime на дату снимка). Время последнего чтения ncdu не видит: «не менялось» не значит «не нужно». Это кандидаты на архивацию или обсуждение с владельцем.'),
    S.h('div', { class: 'row', style: { marginBottom: '12px' } },
      selectParam('Не менялось дольше', 'days', days, [[180, '6 месяцев'], [365, '1 года'], [730, '2 лет'], [1095, '3 лет'], [1825, '5 лет']], (v) => go('days', v)),
      selectParam('Каталоги от', 'min', min, minOpts, (v) => go('min', v))),
    S.h('div', { class: 'tiles' }, S.tile('Всего в таких каталогах', S.fmt.bytes(d.total), S.fmt.pct(d.share) + ' снимка')),
    S.card(null, S.hitsTable(d.dirs, snap.time, { noShare: true, empty: 'Таких каталогов нет' }),
      d.dirs.length >= 300 && S.h('p', { class: 'muted', text: 'Показаны 300 крупнейших.' })));
}

async function reclaimDups(snap, query) {
  const min = query.get('min') || String(100 << 20);
  const d = await S.api(`/api/s/${enc(snap.id)}/dups?min=${enc(min)}`);
  const rows = d.groups.map((g) => S.h('tr', null,
    S.h('td', { class: 'name' }, S.h('b', { class: 'mono', text: g.name }),
      S.h('div', null, g.files.map((f) => S.h('div', null, S.pathCell(f.path, { dir: f.path.slice(0, f.path.lastIndexOf('/')) || '/' }), ' ', S.h('span', { class: 'muted' }, f.owner.name, ' · ', S.fmt.age(f.mtime, snap.time)))))),
    S.h('td', { class: 'num', text: S.fmt.bytes(g.asize) }),
    S.h('td', { class: 'num', text: String(g.files.length) }),
    S.h('td', { class: 'num', text: S.fmt.bytes(g.waste) })));
  return S.h('div', null,
    S.h('div', { class: 'hint' }, 'Файлы с ', S.h('b', null, 'одинаковым именем и размером'), ' в разных местах (жёсткие ссылки уже исключены). Совпадение содержимого не проверялось: перед удалением сравните контрольные суммы (', S.h('code', null, 'md5sum'), ').'),
    S.h('div', { class: 'row', style: { marginBottom: '12px' } },
      selectParam('Файлы от', 'min', min, [[10 << 20, '10 МБ'], [100 << 20, '100 МБ'], [1 << 30, '1 ГБ'], [10 * (1 << 30), '10 ГБ']], (v) => S.go('reclaim/dups', { min: v }))),
    S.h('div', { class: 'tiles' }, S.tile('Лишние копии', S.fmt.bytes(d.waste), `${d.groups.length} групп`)),
    S.card(null, S.h('div', { class: 'table-wrap' }, S.h('table', null,
      S.h('thead', null, S.h('tr', null, S.h('th', null, 'Файл и копии'), S.h('th', { class: 'num' }, 'Размер'), S.h('th', { class: 'num' }, 'Копий'), S.h('th', { class: 'num' }, 'Лишнее'))),
      S.h('tbody', null, rows.length ? rows : S.h('tr', null, S.h('td', { colspan: 4, class: 'empty', text: 'Дубликатов не найдено' }))))),
    d.files_min > 0 && S.h('p', { class: 'muted', text: `Сравниваются только крупнейшие файлы снимка (от ${S.fmt.bytes(d.files_min)}).` })));
}

/* ---------- Trend ---------- */
function changeTable(first, last, label) {
  const keys = [...new Set(Object.keys(first).concat(Object.keys(last)))];
  const rows = keys.map((k) => ({ k, a: first[k] || 0, b: last[k] || 0 })).map((r) => Object.assign(r, { delta: r.b - r.a }))
    .filter((r) => r.delta !== 0).sort((x, y) => Math.abs(y.delta) - Math.abs(x.delta)).slice(0, 20);
  const maxD = Math.max(1, ...rows.map((r) => Math.abs(r.delta)));
  return S.table([
    { label, render: (r) => r.k },
    { label: 'Было', num: true, render: (r) => S.fmt.bytes(r.a) },
    { label: 'Стало', num: true, render: (r) => S.fmt.bytes(r.b) },
    { label: 'Изменение', num: true, render: (r) => deltaText(r.delta) },
    { label: '', cls: 'bar-cell', render: (r) => S.chart.divbar(r.delta, maxD) },
  ], rows, { empty: 'Без изменений' });
}

function deltaText(v) {
  return S.h('span', { class: v > 0 ? 'delta-up' : v < 0 ? 'delta-down' : '' }, (v > 0 ? '▲ ' : v < 0 ? '▼ ' : ''), S.fmt.signedBytes(v));
}

S.views.trend = async () => {
  const series = S.state.series;
  if (!series) return S.views.snapshots({ reason: 'none' });
  const d = await S.api(`/api/trend?series=${enc(series)}`);
  const pts = d.points;
  const withDF = pts.filter((p) => p.df);
  const lines = [];
  if (withDF.length) lines.push({ name: 'занято на диске (df)', short: 'df', color: 'var(--s1)', points: withDF.map((p) => ({ t: p.time, v: p.df.used })) });
  lines.push({ name: 'в снимке (ncdu)', short: 'ncdu', color: 'var(--s2)', points: pts.map((p) => ({ t: p.time, v: p.dsize })) });
  const last = pts[pts.length - 1];
  const ref = last.df ? { v: last.df.size, label: 'ёмкость' } : null;
  const f = d.forecast;
  const out = [S.pageHead('Динамика', `${series} · ${pts.length} ${S.fmt.plural(pts.length, 'снимок', 'снимка', 'снимков')}`)];
  if (pts.length < 2) {
    out.push(S.h('div', { class: 'hint' }, 'Для динамики нужен второй снимок той же серии. Запустите ', S.h('code', null, 'scripts/collect.sh'), ' ещё раз, например через неделю.'));
  } else {
    const full = f.days_to_full >= 0 ? `≈ ${Math.round(f.days_to_full)} дн` : f.per_day <= 0 ? 'не растёт' : '—';
    const fullNote = f.days_to_full >= 0 ? 'к ' + S.fmt.day(last.time + f.days_to_full * 86400) : f.explanation;
    out.push(S.h('div', { class: 'tiles' },
      S.tile('Рост в сутки', S.fmt.signedBytes(f.per_day), f.basis === 'df' ? 'по df, весь диск' : 'по снимкам ncdu'),
      S.tile('Рост за 30 дней', S.fmt.signedBytes(f.per_day * 30), `по ${f.points} точкам за ${Math.round(f.span_days)} дн`),
      S.tile('До заполнения диска', full, fullNote)));
  }
  out.push(S.card('Занятое место', S.chart.line(lines, { ref, title: 'Занятое место во времени' })));
  if (pts.length >= 2) {
    const a = pts[0], b = pts[pts.length - 1];
    out.push(S.h('div', { class: 'grid' },
      S.card(`Владельцы: ${S.fmt.day(a.time)} → ${S.fmt.day(b.time)}`, changeTable(a.owners, b.owners, 'Владелец')),
      S.card('Группы данных', changeTable(a.classes, b.classes, 'Группа'))));
    const ready = pts.slice().reverse();
    const selA = S.h('select', { 'aria-label': 'Старый снимок' }, ready.map((p, i) => S.h('option', { value: p.id, selected: i === 1 }, S.fmt.date(p.time))));
    const selB = S.h('select', { 'aria-label': 'Новый снимок' }, ready.map((p, i) => S.h('option', { value: p.id, selected: i === 0 }, S.fmt.date(p.time))));
    const btn = S.h('button', { class: 'primary', type: 'button' }, 'Сравнить');
    btn.addEventListener('click', () => S.go('diff', { a: selA.value, b: selB.value }));
    out.push(S.card('Что изменилось между снимками', S.h('div', { class: 'row' }, selA, '→', selB, btn)));
  }
  return S.h('div', null, out);
};

/* ---------- Diff ---------- */
S.views.diff = async ({ query }) => {
  const a = query.get('a'), b = query.get('b'), path = query.get('path') || '';
  if (!a || !b) throw new Error('Выберите два снимка на странице «Динамика».');
  const d = await S.api(`/api/diff?a=${enc(a)}&b=${enc(b)}&path=${enc(path)}`);
  S.pageBanners.push(...d.warnings);
  const days = Math.round((d.b.time - d.a.time) / 86400);
  const link = (p) => S.link('diff', { a: d.a.id, b: d.b.id, path: p });
  const cols = (maxD) => [
    { label: 'Каталог', cls: 'name', render: (c) => S.h('span', null,
      S.h('a', { href: link(c.path), class: 'mono', text: c.path }), ' ', S.copyBtn(c.path),
      c.a_id < 0 && S.h('span', { class: 'badge', text: 'новый' }), c.b_id < 0 && S.h('span', { class: 'badge', text: 'удалён' }),
      c.direct && S.h('div', { class: 'muted', text: 'файлы прямо в этом каталоге' })) },
    { label: 'Было', num: true, render: (c) => S.fmt.bytes(c.a_size) },
    { label: 'Стало', num: true, render: (c) => S.fmt.bytes(c.b_size) },
    { label: 'Изменение', num: true, render: (c) => deltaText(c.delta) },
    { label: '', cls: 'bar-cell', render: (c) => S.chart.divbar(c.delta, maxD) },
  ];
  const maxO = Math.max(1, ...d.origins.map((c) => Math.abs(c.delta)));
  const kids = d.children.filter((c) => c.delta !== 0);
  const maxK = Math.max(1, ...kids.map((c) => Math.abs(c.delta)));
  const root = d.b.root || d.a.root || '';
  const parent = d.dir.path !== root && d.dir.path.includes('/') ? d.dir.path.slice(0, d.dir.path.lastIndexOf('/')) || '/' : '';
  return S.h('div', null,
    S.pageHead('Что изменилось', [`${S.fmt.date(d.a.time)} → ${S.fmt.date(d.b.time)} (${days} дн) · всего `, deltaText(d.total)]),
    S.card('Откуда изменения', S.h('p', { class: 'sub', text: `Новые и удалённые каталоги целиком и изменения файлов прямо в каталогах; в сумме дают общий итог. Показано от ${S.fmt.bytes(d.min_delta)}.` }),
      S.table(cols(maxO), d.origins, { empty: 'Заметных изменений нет' })),
    S.card(S.h('span', null, 'По каталогам: ', S.h('span', { class: 'mono', text: d.dir.path })),
      parent && S.h('p', null, S.h('a', { href: link(parent) }, '↑ на уровень выше')),
      S.h('p', { class: 'sub', text: `Было ${S.fmt.bytes(d.dir.a_size)}, стало ${S.fmt.bytes(d.dir.b_size)}.` }),
      S.table(cols(maxK), kids, { empty: 'Подкаталоги не изменились' })));
};

/* ---------- Snapshots ---------- */
S.views.snapshots = ({ reason }) => {
  const st = S.state;
  const now = Date.now() / 1000;
  const status = (s) => {
    if (s.status === 'ready') return S.h('span', { class: 'status-pill' }, S.h('span', { class: 'dot', style: { background: 'var(--good)' } }), 'готов');
    if (s.status === 'indexing') return S.h('span', null, S.h('span', { class: 'progress' }, S.h('span', { style: { width: (s.progress * 100).toFixed(0) + '%' } })), ' индексация ', S.fmt.pct(s.progress, 0));
    if (s.status === 'pending') return S.h('span', { class: 'status-pill' }, S.h('span', { class: 'dot', style: { background: 'var(--muted)' } }), 'в очереди');
    if (s.status === 'scanning') {
      const idle = now - s.source_mtime;
      return S.h('span', null, S.h('span', { class: 'status-pill' }, S.h('span', { class: 'dot', style: { background: 'var(--s1)' } }), 'идёт сканирование'),
        S.h('div', { class: idle > 3600 ? 'delta-up' : 'muted', text: `${S.fmt.bytes(s.source_size)} записано, обновлён ${S.fmt.age(s.source_mtime, now)} назад` + (idle > 3600 ? ' — ncdu, возможно, прерван' : '') }));
    }
    return S.h('span', null, S.h('span', { class: 'status-pill' }, S.h('span', { class: 'dot', style: { background: 'var(--critical)' } }), 'ошибка'), S.h('div', { class: 'delta-up', text: s.error }));
  };
  const out = [S.pageHead('Снимки', `каталог: ${st.dir}`)];
  if (reason === 'none') out.push(S.h('div', { class: 'hint' }, S.h('b', null, 'Готовых снимков пока нет. '), 'Сделайте первый снимок командой ниже; после сканирования он появится здесь сам.'));
  if (reason === 'not-ready') out.push(S.h('div', { class: 'hint' }, S.h('b', null, 'Выбранный снимок ещё не готов. '), 'Страница обновится, когда индексация закончится.'));
  out.push(S.card(null, S.table([
    { label: 'Серия', sort: (s) => s.series, asc: true, render: (s) => s.series },
    { label: 'Время', num: true, sort: (s) => s.time, render: (s) => (s.status === 'ready' ? S.h('a', { href: '#/overview?s=' + enc(s.id), text: S.fmt.date(s.time) }) : S.fmt.date(s.time)) },
    { label: 'Статус', render: status },
    { label: 'Объём', num: true, sort: (s) => s.dsize, render: (s) => (s.dsize ? S.fmt.bytes(s.dsize) : '') },
    { label: 'Файлов', num: true, sort: (s) => s.files, render: (s) => (s.files ? S.fmt.compact(s.files) : '') },
    { label: 'Файл снимка', num: true, sort: (s) => s.source_size, render: (s) => S.fmt.bytes(s.source_size) },
    { label: 'Индексация', num: true, render: (s) => (s.index_seconds ? Math.round(s.index_seconds) + ' с' : '') },
    { label: 'Заметки', render: (s) => S.h('span', null,
      !s.df && s.status !== 'scanning' && S.h('span', { class: 'badge', text: 'нет .df' }), ' ',
      !s.has_users && s.status !== 'scanning' && S.h('span', { class: 'badge', text: 'нет .passwd' }), ' ',
      s.read_errors > 0 && S.h('span', { class: 'badge err', text: `${s.read_errors} ошибок чтения` }),
      s.sidecar_errors.map((e) => S.h('div', { class: 'delta-up', text: e }))) },
  ], st.snaps, { sortCol: 1, empty: 'Снимков нет' })));
  out.push(S.card('Как сделать снимок',
    S.h('p', { class: 'sub' }, 'На сервере с данными, внутри tmux (сканирование десятков миллионов файлов идёт долго; sudo спросит пароль один раз):'),
    S.h('code', { class: 'cmd', text: `tmux new -s scan\ncd <каталог репозитория space>\n./scripts/collect.sh /data            # несколько путей: ./scripts/collect.sh /data /home` }),
    S.h('p', { class: 'sub' }, 'Скрипт использует только ', S.h('code', null, 'sudo ncdu'), '. Снимки пишутся в ', S.h('code', null, st.dir), ' (переменная OUT), сервер подхватывает их сам. Если ', S.h('code', null, 'sudo ncdu -o -'), ' не пишет в pipe, запустите с ', S.h('code', null, 'MODE=file'), ': ncdu запишет файл сам, а ', S.h('code', null, 'sudo chown'), ' вернёт его вам.')));
  return S.h('div', null, out);
};
