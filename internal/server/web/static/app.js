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
  size: 'large', // a text label's size and alignment
  align: 'left',
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
  if (job.kind === 'image') return 'Image label';
  if (job.kind === 'text') return job.lines.join(' / ');
  return job.text;
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

  $('job-title').textContent = job.kind === 'text' ? job.lines.join('\n') : jobName(job);
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

  const facts = [];
  if (job.result && job.result.code) facts.push(['Code', job.result.code]);
  facts.push(['May have printed', mayHavePrinted(job)]);
  facts.push(['Kind', { barcode: 'Barcode', text: 'Text', image: 'Image' }[job.kind] || job.kind]);
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
  $('png-hint').textContent = st.online && st.height_dots
    ? `Must be ${st.height_dots} dots high for the ${st.tape_mm} mm tape that is loaded. Up to 1 MB.`
    : 'Must be as many dots high as the loaded tape needs. Up to 1 MB.';

  // Only 24 mm tape takes a second line.
  const oneLine = !!st.tape_mm && st.tape_mm !== 24;
  if (oneLine) $('line2').value = '';
  $('line2-field').hidden = oneLine;
  renderText();
}

// renderText shows the text label's size and alignment. The board draws the
// label, so there is no preview: problems show when Print is pressed.
function renderText() {
  pressed('size', 'size');
  pressed('align', 'align');
  $('align').hidden = $('line2-field').hidden || !$('line2').value.trim();
}

function renderBarcode() {
  $('text-hint').textContent = `Code 128, up to 80 characters. ${$('text').value.length} of 80.`;
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
      const lines = [$('line1').value, $('line2-field').hidden ? '' : $('line2').value].filter((l) => l.trim());
      if (!lines.length) return say('Type the text for the label.', true);
      job = { kind: 'text', lines, size: state.size, align: state.align };
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
      state[key] = b.dataset[key];
      renderText();
    });
  }
  $('line2').addEventListener('input', renderText);
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
