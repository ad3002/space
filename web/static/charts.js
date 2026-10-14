'use strict';
/* Charts: column chart, line chart with crosshair, squarified treemap,
   diverging bar. Mark specs follow the dataviz skill: thin marks, 4px rounded
   data-ends square at the baseline, 2px lines, >=8px markers with a 2px
   surface ring, hairline solid grid, hover/focus tooltips, table twins. */
S.chart = {};

// Nice byte ticks: clean numbers in the unit that fits the maximum.
S.chart.byteTicks = function byteTicks(max, count) {
  count = count || 4;
  if (!(max > 0)) return { max: 1, ticks: [0] };
  let unit = 1;
  while (max / unit >= 1024) unit *= 1024;
  const m = max / unit;
  const raw = m / count;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const step = [1, 2, 2.5, 5, 10].map((s) => s * mag).find((s) => s >= raw) * unit;
  const top = Math.ceil(max / step) * step;
  const ticks = [];
  for (let v = 0; v <= top + step / 2; v += step) ticks.push(v);
  return { max: top, ticks };
};

function barPath(x, y, w, h) {
  if (h <= 0) return '';
  const r = Math.min(4, w / 2, h);
  return `M${x},${y + h}L${x},${y + r}Q${x},${y} ${x + r},${y}L${x + w - r},${y}Q${x + w},${y} ${x + w},${y + r}L${x + w},${y + h}Z`;
}

// Column chart. data: [{label, value, emph, note}]; emph=true marks the
// emphasised columns (accent), others are de-emphasised gray.
S.chart.columns = function columns(data, opts) {
  opts = opts || {};
  const fmt = opts.fmt || S.fmt.bytes;
  const W = 600, H = opts.height || 200, padL = 64, padR = 8, padT = 10, padB = 30;
  const { max, ticks } = S.chart.byteTicks(Math.max(...data.map((d) => d.value), 0));
  const plotW = W - padL - padR, plotH = H - padT - padB;
  const band = plotW / Math.max(1, data.length);
  const bw = Math.min(24, band * 0.6);
  const y = (v) => padT + plotH - (v / max) * plotH;
  const svg = S.svg('svg', { class: 'chart', viewBox: `0 0 ${W} ${H}`, role: 'img', 'aria-label': opts.title || 'Столбчатая диаграмма' });
  for (const t of ticks) {
    svg.appendChild(S.svg('line', { class: t === 0 ? 'axis-line' : 'grid-line', x1: padL, x2: W - padR, y1: y(t), y2: y(t) }));
    svg.appendChild(S.svg('text', { x: padL - 6, y: y(t) + 4, 'text-anchor': 'end' }, fmt(t)));
  }
  data.forEach((d, i) => {
    const cx = padL + band * i + band / 2;
    const h = Math.max(d.value > 0 ? 1 : 0, plotH - (y(d.value) - padT));
    const bar = S.svg('path', { d: barPath(cx - bw / 2, padT + plotH - h, bw, h), fill: d.emph === false ? 'var(--neutral-fill)' : 'var(--s1)' });
    const hit = S.svg('rect', { x: padL + band * i, y: padT, width: band, height: plotH + padB, fill: 'transparent', tabindex: 0, 'aria-label': `${d.label}: ${fmt(d.value)}` });
    S.tip.bind(hit, () => S.tip.value(fmt(d.value), d.label, d.note && S.h('div', { class: 'tl', text: d.note })));
    hit.addEventListener('pointerenter', () => bar.setAttribute('opacity', '0.8'));
    hit.addEventListener('pointerleave', () => bar.removeAttribute('opacity'));
    svg.appendChild(bar);
    svg.appendChild(S.svg('text', { x: cx, y: H - 10, 'text-anchor': 'middle', class: 'lbl' }, d.label));
    svg.appendChild(hit);
  });
  return svg;
};

// Wrap a chart with a collapsible table twin.
S.chart.withTable = function withTable(chart, cols, rows) {
  return S.h('div', null, chart,
    S.h('details', null, S.h('summary', { class: 'muted', text: 'Таблица' }), S.table(cols, rows)));
};

// Line chart over time. series: [{name, color, points: [{t, v}]}];
// opts.ref: {v, label} draws a labelled horizontal reference (capacity).
S.chart.line = function line(series, opts) {
  opts = opts || {};
  const fmt = opts.fmt || S.fmt.bytes;
  const W = 760, H = opts.height || 260, padL = 70, padR = 130, padT = 14, padB = 28;
  const all = series.flatMap((s) => s.points);
  if (!all.length) return S.h('div', { class: 'empty', text: 'Нет данных' });
  let t0 = Math.min(...all.map((p) => p.t)), t1 = Math.max(...all.map((p) => p.t));
  if (t1 - t0 < 86400) { t0 -= 86400; t1 += 86400; }
  const vmax = Math.max(...all.map((p) => p.v), opts.ref ? opts.ref.v : 0);
  const { max, ticks } = S.chart.byteTicks(vmax);
  const plotW = W - padL - padR, plotH = H - padT - padB;
  const x = (t) => padL + ((t - t0) / (t1 - t0)) * plotW;
  const y = (v) => padT + plotH - (v / max) * plotH;
  const svg = S.svg('svg', { class: 'chart wide', viewBox: `0 0 ${W} ${H}`, role: 'img', 'aria-label': opts.title || 'График' });
  for (const t of ticks) {
    svg.appendChild(S.svg('line', { class: t === 0 ? 'axis-line' : 'grid-line', x1: padL, x2: padL + plotW, y1: y(t), y2: y(t) }));
    svg.appendChild(S.svg('text', { x: padL - 6, y: y(t) + 4, 'text-anchor': 'end' }, fmt(t)));
  }
  const withYear = new Date(t0 * 1000).getFullYear() !== new Date(t1 * 1000).getFullYear();
  const nx = 4;
  for (let i = 0; i <= nx; i++) {
    const t = t0 + ((t1 - t0) * i) / nx;
    svg.appendChild(S.svg('text', { x: x(t), y: H - 8, 'text-anchor': i === 0 ? 'start' : i === nx ? 'end' : 'middle' }, S.fmt.axisDay(t, withYear)));
  }
  if (opts.ref) {
    svg.appendChild(S.svg('line', { x1: padL, x2: padL + plotW, y1: y(opts.ref.v), y2: y(opts.ref.v), stroke: 'var(--critical)', 'stroke-width': 1 }));
    svg.appendChild(S.svg('text', { x: padL + plotW + 8, y: y(opts.ref.v) + 4, class: 'lbl' }, `${opts.ref.label} ${fmt(opts.ref.v)}`));
  }
  // End labels: skip one that would collide with an already placed label.
  const placed = opts.ref ? [y(opts.ref.v)] : [];
  for (const s of series) {
    const pts = s.points.slice().sort((a, b) => a.t - b.t);
    if (!pts.length) continue;
    const d = pts.map((p, i) => (i ? 'L' : 'M') + x(p.t).toFixed(1) + ',' + y(p.v).toFixed(1)).join('');
    svg.appendChild(S.svg('path', { d, fill: 'none', stroke: s.color, 'stroke-width': 2, 'stroke-linejoin': 'round', 'stroke-linecap': 'round' }));
    for (const p of pts) {
      svg.appendChild(S.svg('circle', { cx: x(p.t), cy: y(p.v), r: 4, fill: s.color, stroke: 'var(--surface)', 'stroke-width': 2 }));
    }
    const last = pts[pts.length - 1];
    const ly = y(last.v);
    if (placed.every((py) => Math.abs(py - ly) > 13)) {
      placed.push(ly);
      svg.appendChild(S.svg('text', { x: padL + plotW + 8, y: ly + 4, class: 'lbl' }, `${s.short || s.name} ${fmt(last.v)}`));
    }
  }
  // Crosshair snapping to the nearest time that has data.
  const times = [...new Set(all.map((p) => p.t))].sort((a, b) => a - b);
  const cross = S.svg('line', { y1: padT, y2: padT + plotH, stroke: 'var(--axis)', 'stroke-width': 1, visibility: 'hidden' });
  svg.appendChild(cross);
  const overlay = S.svg('rect', { x: padL, y: padT, width: plotW, height: plotH, fill: 'transparent', tabindex: 0, 'aria-label': 'Значения по датам' });
  let cur = times.length - 1;
  const readout = (evt) => {
    const t = times[cur];
    cross.setAttribute('x1', x(t)); cross.setAttribute('x2', x(t));
    cross.setAttribute('visibility', 'visible');
    const rows = series.map((s) => {
      const p = s.points.find((q) => q.t === t);
      return p && S.h('div', { class: 'trow' }, S.h('span', { class: 'tkey', style: { background: s.color } }),
        S.h('b', { text: fmt(p.v) }), S.h('span', { class: 'tl', text: s.name }));
    });
    if (opts.ref) rows.push(S.h('div', { class: 'tl', text: `${opts.ref.label}: ${fmt(opts.ref.v)}` }));
    S.tip.show(evt, S.h('div', { class: 'tl', text: S.fmt.date(t) }), rows);
  };
  overlay.addEventListener('pointermove', (evt) => {
    const r = svg.getBoundingClientRect();
    const px = ((evt.clientX - r.left) / r.width) * W;
    let best = 0;
    times.forEach((t, i) => { if (Math.abs(x(t) - px) < Math.abs(x(times[best]) - px)) best = i; });
    cur = best;
    readout(evt);
  });
  overlay.addEventListener('focus', readout);
  overlay.addEventListener('keydown', (evt) => {
    if (evt.key === 'ArrowLeft') cur = Math.max(0, cur - 1);
    else if (evt.key === 'ArrowRight') cur = Math.min(times.length - 1, cur + 1);
    else return;
    evt.preventDefault();
    readout(evt);
  });
  const off = () => { cross.setAttribute('visibility', 'hidden'); S.tip.hide(); };
  overlay.addEventListener('pointerleave', off);
  overlay.addEventListener('blur', off);
  svg.appendChild(overlay);
  const legend = series.length > 1 || opts.ref ? S.h('div', { class: 'legend' },
    series.map((s) => S.h('span', null, S.h('span', { class: 'key line', style: { background: s.color } }), s.name)),
    opts.ref && S.h('span', null, S.h('span', { class: 'key line', style: { background: 'var(--critical)' } }), opts.ref.label)) : null;
  return S.h('div', null, legend, svg);
};

const measureCtx = document.createElement('canvas').getContext('2d');
function textWidth(text, bold) {
  measureCtx.font = `${bold ? 600 : 400} 12px system-ui, -apple-system, "Segoe UI", sans-serif`;
  return measureCtx.measureText(text).width;
}

// Squarified treemap (Bruls et al.). items: [{name, value, color, ink, onClick, tip}]
S.chart.treemap = function treemap(items, opts) {
  opts = opts || {};
  const el = S.h('div', { class: 'treemap', role: 'list', 'aria-label': opts.title || 'Карта размеров' });
  const maxCells = opts.maxCells || 150;
  let list = items.filter((d) => d.value > 0).sort((a, b) => b.value - a.value);
  if (list.length > maxCells) {
    const rest = list.slice(maxCells - 1);
    list = list.slice(0, maxCells - 1);
    list.push({ name: `ещё ${rest.length}`, value: rest.reduce((s, d) => s + d.value, 0), color: 'var(--neutral-fill)', ink: 'var(--ink)', rest: true });
  }
  function layout() {
    const W = el.clientWidth, H = el.clientHeight;
    if (!W || !H) return;
    el.replaceChildren();
    const total = list.reduce((s, d) => s + d.value, 0);
    if (!total) { el.appendChild(S.h('div', { class: 'empty', text: 'Пусто' })); return; }
    const scale = (W * H) / total;
    const rects = [];
    let rect = { x: 0, y: 0, w: W, h: H };
    let row = [];
    const worst = (r, side) => {
      const s = r.reduce((a, d) => a + d.area, 0);
      const mx = Math.max(...r.map((d) => d.area)), mn = Math.min(...r.map((d) => d.area));
      return Math.max((side * side * mx) / (s * s), (s * s) / (side * side * mn));
    };
    const place = (r) => {
      const s = r.reduce((a, d) => a + d.area, 0);
      if (rect.w >= rect.h) {
        const w = s / rect.h; let yy = rect.y;
        for (const d of r) { const h = d.area / w; rects.push({ d, x: rect.x, y: yy, w, h }); yy += h; }
        rect = { x: rect.x + w, y: rect.y, w: rect.w - w, h: rect.h };
      } else {
        const h = s / rect.w; let xx = rect.x;
        for (const d of r) { const w = d.area / h; rects.push({ d, x: xx, y: rect.y, w, h }); xx += w; }
        rect = { x: rect.x, y: rect.y + h, w: rect.w, h: rect.h - h };
      }
    };
    for (const item of list.map((d) => ({ item: d, area: d.value * scale }))) {
      const side = Math.min(rect.w, rect.h);
      if (!row.length || worst(row.concat([item]), side) <= worst(row, side)) row.push(item);
      else { place(row); row = [item]; }
    }
    if (row.length) place(row);
    for (const r of rects) {
      const d = r.d.item;
      const cell = S.h('div', {
        class: 'tm-cell' + (d.ink === '#0b0b0b' ? ' light-ink' : ''), role: 'listitem', tabindex: d.onClick ? 0 : null,
        style: { left: r.x + 1 + 'px', top: r.y + 1 + 'px', width: Math.max(0, r.w - 2) + 'px', height: Math.max(0, r.h - 2) + 'px', background: d.color, color: d.ink || '#fff' },
        'aria-label': `${d.name}: ${S.fmt.bytes(d.value)}`,
      });
      // Label only what fits with padding (measured, never clipped); the
      // tooltip and the table below carry the rest.
      const room = r.w - 2 - 12;
      const size = S.fmt.bytes(d.value);
      const fitsSize = textWidth(size, false) <= room && r.h > 20;
      if (fitsSize && textWidth(d.name, true) <= room && r.h > 36) {
        cell.appendChild(S.h('div', { class: 'tm-name', text: d.name }));
        cell.appendChild(S.h('div', { class: 'tm-size', text: size }));
      } else if (fitsSize) {
        cell.appendChild(S.h('div', { class: 'tm-size', text: size }));
      }
      S.tip.bind(cell, () => (d.tip ? d.tip() : S.tip.value(S.fmt.bytes(d.value), d.name)));
      if (d.onClick) {
        cell.addEventListener('click', () => { S.tip.hide(); d.onClick(); });
        cell.addEventListener('keydown', (e) => { if (e.key === 'Enter') { S.tip.hide(); d.onClick(); } });
      }
      el.appendChild(cell);
    }
  }
  new ResizeObserver(layout).observe(el);
  return el;
};

// Diverging bar for a delta: grows right (uses space) or left (frees space)
// from a centre baseline.
S.chart.divbar = function divbar(delta, maxAbs) {
  const frac = maxAbs > 0 ? Math.min(1, Math.abs(delta) / maxAbs) : 0;
  const w = (frac * 50).toFixed(2) + '%';
  const bar = S.h('span', { style: {
    position: 'absolute', top: 0, bottom: 0, width: w,
    left: delta >= 0 ? '50%' : null, right: delta < 0 ? '50%' : null,
    background: delta >= 0 ? 'var(--div-up)' : 'var(--div-down)',
    borderRadius: delta >= 0 ? '0 4px 4px 0' : '4px 0 0 4px',
  } });
  return S.h('div', { class: 'hbar', 'aria-hidden': 'true' },
    S.h('span', { style: { left: '50%', width: '1px', background: 'var(--axis)', borderRadius: 0 } }), bar);
};
