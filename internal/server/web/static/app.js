// The ptouch web UI: the printer's state, the newest labels, and a form to
// print one. It polls /api/status and /api/jobs while the tab is visible and
// signs in through the server's /auth/* routes, which set a session cookie.
'use strict';

const POLL_MS = 5000;
const LIST_LIMIT = 20;

const $ = (id) => document.getElementById(id);

const state = {
  status: null, // /api/status
  jobs: [], // /api/jobs, newest first
  seen: null, // job ids already shown, so new ones flash; null before the first load
  filter: 'all',
  kind: 'barcode',
  extra: null, // the open job when it isn't among the newest
  size: 1, // a text label's size, as a fraction of the largest that fits
  align: 'center',
  glyph: null, // code point -> whether Go Bold can draw it, once the font loads
  fontFailed: false,
  textError: '', // why the text label can't print yet, or ''
};

// --- API -------------------------------------------------------------------

class SignedOut extends Error {}

async function api(path, options = {}) {
  const res = await fetch(path, { credentials: 'same-origin', ...options });
  if (res.status === 401) throw new SignedOut();
  const body = res.status === 204 ? null : await res.json().catch(() => null);
  if (!res.ok) throw new Error((body && body.error) || `The server answered ${res.status}.`);
  return body;
}

async function refresh() {
  try {
    const [status, jobs] = await Promise.all([api('/api/status'), api(`/api/jobs?limit=${LIST_LIMIT}`)]);
    state.status = status;
    state.jobs = jobs || [];
    render();
  } catch (err) {
    if (err instanceof SignedOut) showSignIn();
  }
}

let timer = null;
function poll() {
  clearTimeout(timer);
  if (document.hidden) return;
  refresh().finally(() => {
    timer = setTimeout(poll, POLL_MS);
  });
}

// --- Words -----------------------------------------------------------------

function ago(iso) {
  const s = Math.max(0, Math.round((Date.now() - new Date(iso)) / 1000));
  if (s < 5) return 'just now';
  if (s < 60) return `${s}s ago`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.round(m / 60);
  if (h < 24) return h === 1 ? '1 hour ago' : `${h} hours ago`;
  const d = Math.round(h / 24);
  return d === 1 ? 'yesterday' : `${d} days ago`;
}

function when(iso) {
  return new Date(iso).toLocaleString(undefined, {
    weekday: 'short', day: 'numeric', month: 'short', year: 'numeric', hour: 'numeric', minute: '2-digit',
  });
}

function setTime(el, iso) {
  el.textContent = ago(iso);
  el.dateTime = iso;
  el.title = when(iso);
}

function jobName(job) {
  return job.kind === 'image' ? 'Image label' : job.text;
}

// The tag a job's row shows: none once printed.
function jobTag(job) {
  switch (job.status) {
    case 'queued': return { text: 'Waiting', cls: 'wc-state' };
    case 'sent': return { text: 'Printing', cls: 'wc-state wc-state-transcribing' };
    case 'failed': return { text: 'Failed', cls: 'wc-state' };
    default: return null;
  }
}

function mayHavePrinted(job) {
  if (job.status === 'done' || job.status === 'sent') return 'true';
  if (job.status === 'failed') return job.result && job.result.may_have_printed ? 'true' : 'false';
  return 'false';
}

const FILTERS = {
  all: () => true,
  done: (j) => j.status === 'done',
  failed: (j) => j.status === 'failed',
  waiting: (j) => j.status === 'queued' || j.status === 'sent',
};

// --- Rendering -------------------------------------------------------------

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

// showSignIn sends the browser to Dex's sign-in page. If it was sent there in
// the last minute and still isn't signed in, the cookie isn't sticking, so it
// shows a sign-in link instead of going round again.
function showSignIn() {
  let last = 0;
  try { last = Number(sessionStorage.getItem('signInAt')) || 0; } catch {}
  if (Date.now() - last > 60000) {
    try { sessionStorage.setItem('signInAt', String(Date.now())); } catch {}
    location.assign('/auth/login');
    return;
  }
  $('app').hidden = true;
  $('who').hidden = true;
  $('sign-in').hidden = false;
  clearTimeout(timer);
}

function ready() {
  const st = state.status;
  return !!(st && st.online && !st.problem && st.tape_mm);
}

function render() {
  $('sign-in').hidden = true;
  $('app').hidden = false;
  renderPrinter();
  renderList();
  renderPane();
  renderForm();
}

function renderPrinter() {
  const st = state.status || {};
  let tag = 'Ready';
  let fix = '';
  let hot = false;
  if (!st.last_poll) {
    tag = 'Offline';
    fix = 'The board has never checked in. Check that it is powered and on the network.';
  } else if (!st.online) {
    tag = 'Offline';
    fix = 'Printer offline';
    hot = true;
  } else if (st.problem) {
    tag = st.problem.replace(/_/g, ' ');
    fix = st.fix || '';
  } else if (!st.tape_mm) {
    tag = 'No tape';
    fix = 'The printer reports no supported tape. Load 12 or 24 mm tape.';
  }
  $('printer-state').textContent = tag;
  $('printer-fix').textContent = fix;
  $('printer-fix').hidden = !fix;
  $('printer-fix').classList.toggle('is-hot', hot);
  $('tape').textContent = st.online && st.tape_mm ? `${st.tape_mm} mm` : 'None reported';
  $('height').textContent = st.online && st.height_dots ? `${st.height_dots} dots` : '—';
  if (st.last_poll) setTime($('checked'), st.last_poll);
  else $('checked').textContent = 'Never';
  $('wifi').textContent = st.wifi || '—';
}

const rowCache = new Map(); // job id -> { key, li, time }

function renderList() {
  const jobs = state.jobs;
  const printing = jobs.filter((j) => j.status === 'sent').length;
  const failed = jobs.filter((j) => j.status === 'failed').length;
  const parts = [`${jobs.length} newest`];
  if (printing) parts.push(`${printing} printing`);
  if (failed) parts.push(`${failed} failed`);
  $('count').textContent = parts.join(' · ');

  for (const b of $('filter').querySelectorAll('button')) {
    b.setAttribute('aria-pressed', String(b.dataset.filter === state.filter));
  }

  const open = openJobID();
  const shown = jobs.filter(FILTERS[state.filter]);
  const ul = $('rows');
  const keep = new Set();
  shown.forEach((job, i) => {
    const key = JSON.stringify(job) + (job.id === open);
    let row = rowCache.get(job.id);
    if (!row || row.key !== key) {
      const fresh = state.seen !== null && !state.seen.has(job.id);
      const li = buildRow(job, job.id === open, fresh);
      if (row) row.li.replaceWith(li);
      row = { key, li, time: li.querySelector('time') };
      rowCache.set(job.id, row);
    }
    setTime(row.time, job.created_at);
    keep.add(job.id);
    if (ul.children[i] !== row.li) ul.insertBefore(row.li, ul.children[i] || null);
  });
  for (const [id, row] of rowCache) {
    if (!keep.has(id)) {
      row.li.remove();
      rowCache.delete(id);
    }
  }
  $('no-rows').hidden = shown.length > 0;
  state.seen = new Set(jobs.map((j) => j.id));
}

function buildRow(job, selected, fresh) {
  const a = el('a', 'wc-row' + (selected ? ' is-selected' : '') + (fresh ? ' is-arriving' : ''));
  a.href = `#job-${job.id}`;
  const name = el('span', 'wc-row-name', jobName(job));
  if (job.kind === 'barcode') name.classList.add('mono');
  const tag = jobTag(job);
  if (tag) name.append(el('span', tag.cls, tag.text));
  const when = el('span', 'wc-ago');
  when.append(el('time'));
  const meta = el('span', 'wc-meta');
  meta.append(el('span', '', job.kind));
  if (job.tape_mm) meta.append(el('span', '', `${job.tape_mm} mm`));
  meta.append(el('span', '', job.submitter), el('span', '', `#${job.id}`));
  a.append(name, when, meta);
  if (job.kind === 'image') {
    const img = el('span', 'row-image', 'Image');
    img.setAttribute('aria-hidden', 'true');
    a.append(img);
  }
  const li = el('li');
  li.append(a);
  return li;
}

function openJobID() {
  const m = /^#job-(\d+)$/.exec(location.hash);
  return m ? Number(m[1]) : null;
}

function renderPane() {
  const id = openJobID();
  let job = id && state.jobs.find((j) => j.id === id);
  if (id && !job) {
    if (state.extra && state.extra.id === id) job = state.extra;
    else loadExtra(id);
  }
  $('app').classList.toggle('has-call', !!job);
  $('job-pane').hidden = !job;
  $('print-pane').hidden = !!job;
  if (!job) return;

  $('job-title').textContent = jobName(job);
  $('job-title').classList.toggle('mono', job.kind === 'barcode');
  const tag = jobTag(job) || { text: 'Printed', cls: 'wc-state' };
  $('job-state').textContent = tag.text;
  $('job-state').className = tag.cls;
  setTime($('job-ago'), job.created_at);

  let why = '';
  if (job.status === 'failed') why = (job.result && job.result.error) || 'The board reported a failure without a reason.';
  if (job.status === 'sent') why = 'The board took this label but hasn\'t said how it went. It may have printed: check the printer before you print it again.';
  if (job.status === 'queued') why = 'Waiting for the board to take it. It checks in every 15 seconds.';
  $('job-why').hidden = !why;
  $('job-why-text').textContent = why;

  const bits = job.kind === 'barcode' && code128(job.text);
  $('job-preview').hidden = !bits;
  if (bits) {
    $('job-barcode').setAttribute('aria-label', `Barcode preview for ${job.text}`);
    $('job-barcode').replaceChildren(barsSVG(bits), el('span', '', job.text));
  }

  const facts = [];
  if (job.result && job.result.code) facts.push(['Code', job.result.code]);
  facts.push(['May have printed', mayHavePrinted(job)]);
  facts.push(['Kind', job.kind === 'image' ? 'Image' : 'Barcode']);
  if (job.tape_mm) facts.push(['Tape', `${job.tape_mm} mm`]);
  facts.push(['Submitted by', job.submitter]);
  facts.push(['Queued', when(job.created_at)]);
  if (job.sent_at) facts.push(['Sent', when(job.sent_at)]);
  if (job.finished_at) facts.push(['Finished', when(job.finished_at)]);
  facts.push(['Job ID', String(job.id)]);
  const dl = $('job-facts');
  dl.replaceChildren(...facts.map(([k, v]) => {
    const d = el('div', 'wc-fact');
    d.append(el('dt', '', k), el('dd', '', v));
    return d;
  }));
}

let loading = null;
async function loadExtra(id) {
  if (loading === id) return;
  loading = id;
  try {
    state.extra = await api(`/api/jobs/${id}`);
  } catch (err) {
    if (err instanceof SignedOut) return showSignIn();
    location.hash = ''; // gone, perhaps pruned
  } finally {
    loading = null;
  }
  renderList();
  renderPane();
}

// --- The print form --------------------------------------------------------

function pressed(group, key) {
  for (const b of $(group).querySelectorAll('button')) {
    b.setAttribute('aria-pressed', String(b.dataset[key] === String(state[key])));
  }
}

function renderForm() {
  pressed('kind', 'kind');
  $('barcode-fields').hidden = state.kind !== 'barcode';
  $('text-fields').hidden = state.kind !== 'text';
  $('image-fields').hidden = state.kind !== 'image';
  $('print').disabled = !ready();

  const st = state.status || {};
  const preview = st.online && st.tape_mm ? `Preview, ${st.tape_mm} mm tape` : 'Preview';
  $('preview-label').textContent = preview;
  $('text-preview-label').textContent = preview;
  $('png-hint').textContent = st.online && st.height_dots
    ? `Must be ${st.height_dots} dots high for the ${st.tape_mm} mm tape that is loaded. Up to 1 MB.`
    : 'Must be as many dots high as the loaded tape needs. Up to 1 MB.';

  // Only 24 mm tape takes a second line.
  const oneLine = !!st.tape_mm && st.tape_mm !== 24;
  if (oneLine) $('line2').value = '';
  $('line2-field').hidden = oneLine;
  drawText();
}

// --- Text labels -----------------------------------------------------------

// The board prints at 180 dpi. A text label is padded 14 dots at each end and
// may be 15 cm long, padding included.
const DOTS_PER_CM = 180 / 2.54;
const TEXT_PAD = 14;
const MAX_TEXT_DOTS = Math.round(15 * DOTS_PER_CM);
const LINE_GAP = 0.1; // between two lines, as a fraction of a line's height

// cmapLookup reads the font's Windows Unicode cmap (format 4, which Go Bold
// has) and returns whether a code point has a glyph.
function cmapLookup(buf) {
  const v = new DataView(buf);
  let cmap = 0;
  for (let i = 0; i < v.getUint16(4); i++) {
    if (v.getUint32(12 + 16 * i) === 0x636d6170) cmap = v.getUint32(12 + 16 * i + 8); // 'cmap'
  }
  let sub = 0;
  for (let i = 0; i < v.getUint16(cmap + 2); i++) {
    const r = cmap + 4 + 8 * i;
    if (v.getUint16(r) === 3 && v.getUint16(r + 2) === 1) sub = cmap + v.getUint32(r + 4);
  }
  const segs = v.getUint16(sub + 6) / 2;
  const ends = sub + 14;
  const starts = ends + 2 * segs + 2;
  const deltas = starts + 2 * segs;
  const offsets = deltas + 2 * segs;
  return (c) => {
    for (let i = 0; i < segs; i++) {
      if (c > v.getUint16(ends + 2 * i)) continue;
      const start = v.getUint16(starts + 2 * i);
      if (c < start) return false;
      const ro = v.getUint16(offsets + 2 * i);
      if (!ro) return ((c + v.getInt16(deltas + 2 * i)) & 0xffff) !== 0;
      return v.getUint16(offsets + 2 * i + ro + 2 * (c - start)) !== 0;
    }
    return false;
  };
}

async function loadFont() {
  try {
    const res = await fetch('/static/gobold.ttf');
    if (!res.ok) throw new Error();
    const buf = await res.arrayBuffer();
    document.fonts.add(await new FontFace('Go Bold', buf.slice(0)).load());
    state.glyph = cmapLookup(buf);
  } catch {
    state.fontFailed = true;
  }
  drawText();
}

function textLines() {
  return [$('line1').value, $('line2-field').hidden ? '' : $('line2').value].filter(Boolean);
}

// drawText draws the text label onto the canvas Print sends, at the loaded
// tape's height, and sets state.textError when it can't be printed.
function drawText() {
  const st = state.status || {};
  const lines = textLines();
  pressed('size', 'size');
  pressed('align', 'align');
  $('align').hidden = lines.length < 2;

  let none = '';
  let problem = '';
  if (state.fontFailed) none = 'Couldn\'t load the Go Bold font. Reload the page to try again.';
  else if (!state.glyph) none = 'Loading the font…';
  else if (!st.online || !st.height_dots) none = 'The preview appears once the printer reports its tape.';
  else if (!lines.length) none = 'Type the text to see the label.';
  else {
    const missing = [...new Set(lines.join(''))].filter((ch) => !state.glyph(ch.codePointAt(0)));
    if (missing.length) problem = `Go Bold can't draw: ${missing.join(' ')}`;
  }

  const canvas = $('text-canvas');
  canvas.hidden = !!(none || problem);
  $('text-none').hidden = !none;
  $('text-none').textContent = none;
  state.textError = none || problem;
  if (none || problem) {
    $('text-length').textContent = problem;
    $('text-length').classList.toggle('is-error', !!problem);
    return;
  }

  const ctx = canvas.getContext('2d');
  ctx.font = '100px "Go Bold"';
  const m = ctx.measureText('Hg');
  const ascent = m.fontBoundingBoxAscent / 100;
  const descent = m.fontBoundingBoxDescent / 100;
  const height = st.height_dots;
  const lineHeight = (height / (lines.length + (lines.length - 1) * LINE_GAP)) * state.size;
  const font = `${lineHeight / (ascent + descent)}px "Go Bold"`;
  ctx.font = font;
  const widths = lines.map((l) => Math.ceil(ctx.measureText(l).width));
  const width = Math.max(...widths);

  canvas.width = width + 2 * TEXT_PAD; // resets the context
  canvas.height = height;
  ctx.fillStyle = '#ffffff';
  ctx.fillRect(0, 0, canvas.width, height);
  ctx.fillStyle = '#000000';
  ctx.font = font;
  const top = (height - lineHeight * (lines.length + (lines.length - 1) * LINE_GAP)) / 2;
  lines.forEach((l, i) => {
    const spare = width - widths[i];
    const x = TEXT_PAD + { left: 0, center: spare / 2, right: spare }[state.align];
    ctx.fillText(l, x, top + i * lineHeight * (1 + LINE_GAP) + ascent * (lineHeight / (ascent + descent)));
  });

  const cm = (canvas.width / DOTS_PER_CM).toFixed(1);
  if (canvas.width > MAX_TEXT_DOTS) state.textError = `The label is ${cm} cm long; the limit is 15 cm.`;
  $('text-length').textContent = state.textError || `${cm} cm of 15 cm`;
  $('text-length').classList.toggle('is-error', !!state.textError);
}

function renderBarcode() {
  const text = $('text').value;
  $('text-hint').textContent = `Code 128, up to 80 characters. ${text.length} of 80.`;
  const box = $('barcode');
  box.replaceChildren();
  box.setAttribute('aria-label', text ? `Barcode preview for ${text}` : 'Barcode preview');
  if (!text) {
    box.append(el('span', '', 'Type the text to see the barcode.'));
    return;
  }
  const bits = code128(text);
  if (!bits) {
    box.append(el('span', '', 'Only printable ASCII can go in a barcode.'));
    return;
  }
  box.append(barsSVG(bits), el('span', '', text));
}

// Code 128 bar patterns by symbol value, 1 = bar; 106 is the stop pattern.
const PATTERNS = (
  '11011001100 11001101100 11001100110 10010011000 10010001100 10001001100 10011001000 10011000100 ' +
  '10001100100 11001001000 11001000100 11000100100 10110011100 10011011100 10011001110 10111001100 10011101100 ' +
  '10011100110 11001110010 11001011100 11001001110 11011100100 11001110100 11101101110 11101001100 11100101100 ' +
  '11100100110 11101100100 11100110100 11100110010 11011011000 11011000110 11000110110 10100011000 10001011000 ' +
  '10001000110 10110001000 10001101000 10001100010 11010001000 11000101000 11000100010 10110111000 10110001110 ' +
  '10001101110 10111011000 10111000110 10001110110 11101110110 11010001110 11000101110 11011101000 11011100010 ' +
  '11011101110 11101011000 11101000110 11100010110 11101101000 11101100010 11100011010 11101111010 11001000010 ' +
  '11110001010 10100110000 10100001100 10010110000 10010000110 10000101100 10000100110 10110010000 10110000100 ' +
  '10011010000 10011000010 10000110100 10000110010 11000010010 11001010000 11110111010 11000010100 10001111010 ' +
  '10100111100 10010111100 10010011110 10111100100 10011110100 10011110010 11110100100 11110010100 11110010010 ' +
  '11011011110 11011110110 11110110110 10101111000 10100011110 10001011110 10111101000 10111100010 11110101000 ' +
  '11110100010 10111011110 10111101110 11101011110 11110101110 11010000100 11010010000 11010011100 ' +
  '1100011101011'
).split(' ');

// code128 encodes printable ASCII in code set B. The printed label may use
// code set C for runs of digits, so its bars can differ; this is a preview.
function code128(text) {
  const START_B = 104, STOP = 106;
  const values = [START_B];
  for (const ch of text) {
    const c = ch.charCodeAt(0);
    if (c < 0x20 || c > 0x7e) return null;
    values.push(c - 32);
  }
  let sum = START_B;
  for (let i = 1; i < values.length; i++) sum += i * values[i];
  values.push(sum % 103, STOP);
  return values.map((v) => PATTERNS[v]).join('');
}

function barsSVG(bits) {
  const NS = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(NS, 'svg');
  svg.setAttribute('viewBox', `0 0 ${bits.length} 1`);
  svg.setAttribute('preserveAspectRatio', 'none');
  svg.setAttribute('width', String(bits.length * 2));
  svg.setAttribute('aria-hidden', 'true');
  for (let i = 0; i < bits.length; ) {
    if (bits[i] !== '1') { i++; continue; }
    let j = i;
    while (bits[j] === '1') j++;
    const r = document.createElementNS(NS, 'rect');
    r.setAttribute('x', String(i));
    r.setAttribute('width', String(j - i));
    r.setAttribute('height', '1');
    r.setAttribute('fill', '#121212');
    svg.append(r);
    i = j;
  }
  return svg;
}

let previewURL = null;
function showImage() {
  const file = $('png').files[0];
  if (previewURL) URL.revokeObjectURL(previewURL);
  previewURL = file ? URL.createObjectURL(file) : null;
  $('png-preview').hidden = !file;
  $('png-none').hidden = !!file;
  if (file) $('png-preview').src = previewURL;
  else $('png-preview').removeAttribute('src');
}

function base64(file) {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result).split(',')[1]);
    r.onerror = () => reject(new Error('Couldn\'t read the file.'));
    r.readAsDataURL(file);
  });
}

function say(text, error) {
  $('print-status').textContent = text;
  $('print-status').classList.toggle('is-error', !!error);
}

async function submit(e) {
  e.preventDefault();
  if (!ready()) return;
  let job;
  try {
    if (state.kind === 'barcode') {
      const text = $('text').value;
      if (!text) return say('Type the text for the barcode.', true);
      job = { kind: 'barcode', text };
    } else if (state.kind === 'text') {
      drawText();
      if (state.textError) return say(state.textError, true);
      const image = $('text-canvas').toDataURL('image/png').split(',')[1];
      job = { kind: 'image', tape_mm: state.status.tape_mm, image };
    } else {
      const file = $('png').files[0];
      if (!file) return say('Choose a PNG image.', true);
      job = { kind: 'image', tape_mm: state.status.tape_mm, image: await base64(file) };
    }
  } catch (err) {
    return say(err.message, true);
  }
  $('print').disabled = true;
  say('Sending…');
  try {
    const j = await api('/api/jobs', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(job),
    });
    say(`Queued as #${j.id}. It prints within about 20 seconds.`);
    await refresh();
  } catch (err) {
    if (err instanceof SignedOut) return showSignIn();
    say(err.message, true);
  } finally {
    $('print').disabled = !ready();
  }
}

// --- Start -----------------------------------------------------------------

async function start() {
  $('filter').addEventListener('click', (e) => {
    const b = e.target.closest('button');
    if (!b) return;
    state.filter = b.dataset.filter;
    renderList();
  });
  $('kind').addEventListener('click', (e) => {
    const b = e.target.closest('button');
    if (!b) return;
    state.kind = b.dataset.kind;
    say('');
    renderForm();
  });
  $('text').addEventListener('input', renderBarcode);
  for (const key of ['size', 'align']) {
    $(key).addEventListener('click', (e) => {
      const b = e.target.closest('button');
      if (!b) return;
      state[key] = key === 'size' ? Number(b.dataset.size) : b.dataset.align;
      drawText();
    });
  }
  $('line1').addEventListener('input', drawText);
  $('line2').addEventListener('input', drawText);
  $('png').addEventListener('change', showImage);
  $('print-form').addEventListener('submit', submit);
  $('sign-out').addEventListener('click', async () => {
    await fetch('/auth/logout', { method: 'POST', credentials: 'same-origin' });
    location.reload();
  });
  window.addEventListener('hashchange', () => {
    renderList();
    renderPane();
  });
  document.addEventListener('visibilitychange', poll);
  renderBarcode();
  loadFont();

  try {
    const me = await api('/auth/me');
    $('email').textContent = me.email;
    $('who').hidden = false;
  } catch (err) {
    return showSignIn();
  }
  poll();
}

start();
