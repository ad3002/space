'use strict';
/* Router, snapshot pickers, status polling, boot. */
(function () {
  const st = S.state;
  const main = () => document.getElementById('view');
  let renderToken = 0;
  let lastStatusKey = '';

  function parseHash() {
    const h = location.hash.replace(/^#\/?/, '');
    const qi = h.indexOf('?');
    const p = qi >= 0 ? h.slice(0, qi) : h;
    const parts = p.split('/').filter(Boolean).map(decodeURIComponent);
    return { route: parts[0] || 'overview', args: parts.slice(1), query: new URLSearchParams(qi >= 0 ? h.slice(qi + 1) : '') };
  }

  async function loadSnaps() {
    const r = await S.api('/api/snapshots');
    st.snaps = r.snapshots;
    st.dir = r.dir;
    st.refreshError = r.refresh_error;
  }

  function statusKey() { return st.snaps.map((s) => s.id + ':' + s.status).join('|'); }

  function pickSnap(query) {
    const want = query.get('s') || st.snapId || S.store.get('snap');
    let sn = st.snaps.find((s) => s.id === want);
    if (!sn) {
      const series = st.series || S.store.get('series');
      const ready = st.snaps.filter((s) => s.status === 'ready');
      const inSeries = ready.filter((s) => s.series === series);
      sn = (inSeries.length ? inSeries : ready).slice(-1)[0] || st.snaps.slice(-1)[0];
    }
    st.snapId = sn ? sn.id : null;
    st.series = sn ? sn.series : null;
    if (sn) { S.store.set('snap', sn.id); S.store.set('series', sn.series); }
  }
  S.currentSnap = () => st.snaps.find((s) => s.id === st.snapId);

  const STATUS = { ready: '', pending: ' · в очереди', indexing: ' · индексируется', scanning: ' · идёт сканирование', error: ' · ОШИБКА' };
  function renderPickers() {
    const seriesSel = document.getElementById('series');
    const snapSel = document.getElementById('snap');
    const series = [...new Set(st.snaps.map((s) => s.series))];
    seriesSel.replaceChildren(...series.map((s) => S.h('option', { value: s, selected: s === st.series }, s)));
    seriesSel.disabled = series.length < 2;
    const inSeries = st.snaps.filter((s) => s.series === st.series);
    snapSel.replaceChildren(...inSeries.slice().reverse().map((s) =>
      S.h('option', { value: s.id, selected: s.id === st.snapId }, S.fmt.date(s.time) + (STATUS[s.status] || ''))));
    snapSel.disabled = inSeries.length < 1;
  }

  function currentRoute() { return parseHash(); }

  document.getElementById('series').addEventListener('change', (e) => {
    const inSeries = st.snaps.filter((s) => s.series === e.target.value);
    const ready = inSeries.filter((s) => s.status === 'ready');
    const sn = (ready.length ? ready : inSeries).slice(-1)[0];
    st.series = e.target.value;
    st.snapId = sn ? sn.id : null;
    const { route, args } = currentRoute();
    // Paths don't carry over between series; stay on the same page kind.
    S.go([route, ...args].join('/'), {});
  });
  document.getElementById('snap').addEventListener('change', (e) => {
    st.snapId = e.target.value;
    const { route, args, query } = currentRoute();
    const params = Object.fromEntries(query.entries());
    delete params.s;
    S.go([route, ...args].join('/'), params);
  });

  function globalBanners(route) {
    const list = [];
    if (st.refreshError) list.push({ level: 'error', text: 'Не удаётся прочитать каталог снимков: ' + st.refreshError });
    for (const s of st.snaps) {
      if (s.status === 'error') list.push({ level: 'error', text: `Снимок ${s.id} не проиндексирован: ${s.error}` });
      if (s.status === 'indexing') list.push({ level: 'info', text: `Индексируется снимок ${s.id}: ${S.fmt.pct(s.progress, 0)}` });
    }
    const cur = S.currentSnap();
    if (cur && route !== 'overview') {
      for (const e of cur.sidecar_errors) list.push({ level: 'error', text: 'Вспомогательный файл снимка повреждён: ' + e });
    }
    return list;
  }

  async function render() {
    const { route, args, query } = parseHash();
    pickSnap(query);
    renderPickers();
    for (const a of document.querySelectorAll('#nav a')) {
      const r = a.dataset.route;
      a.classList.toggle('active', r === route || (r === 'owners' && route === 'owner') || (r === 'types' && route === 'type') || (r === 'trend' && route === 'diff'));
      a.href = S.link(r);
    }
    const token = ++renderToken;
    const el = main();
    el.classList.add('loading'); // keep the previous render, dimmed
    S.tip.hide();
    S.pageBanners = [];
    const view = S.views[route];
    let content;
    try {
      if (!view) throw new Error(`Неизвестная страница «${route}»`);
      const needsSnap = !['snapshots', 'trend', 'diff'].includes(route);
      const snap = S.currentSnap();
      if (needsSnap && (!snap || snap.status !== 'ready')) {
        content = await S.views.snapshots({ args, query, snap, reason: snap ? 'not-ready' : 'none' });
      } else {
        content = await view({ args, query, snap });
      }
    } catch (e) {
      content = S.errorView(e);
    }
    if (token !== renderToken) return; // a newer navigation won
    el.replaceChildren(content);
    el.classList.remove('loading');
    S.setBanners(globalBanners(route).concat(S.pageBanners));
    const snap = S.currentSnap();
    document.title = 'space · ' + (snap ? (snap.root || snap.label) : 'снимки');
  }
  S.render = render;

  // Poll snapshot status: fast while something is being scanned or indexed.
  async function poll() {
    let busy = false;
    try {
      await loadSnaps();
      busy = st.snaps.some((s) => ['scanning', 'pending', 'indexing'].includes(s.status));
      const key = statusKey();
      const { route } = parseHash();
      if (key !== lastStatusKey) {
        lastStatusKey = key;
        render();
      } else if (route === 'snapshots' || busy) {
        renderPickers();
        S.setBanners(globalBanners(route).concat(S.pageBanners));
        if (route === 'snapshots') render();
      }
    } catch (e) {
      S.setBanners([{ level: 'error', text: e.message }]);
    }
    setTimeout(poll, busy ? 4000 : 60000);
  }

  window.addEventListener('hashchange', render);
  loadSnaps()
    .then(() => { lastStatusKey = statusKey(); return render(); })
    .catch((e) => main().replaceChildren(S.errorView(e)))
    .finally(() => setTimeout(poll, 4000));
})();
