// Virtual media panel: image library, drives, transfer graph and USB reset.
// Everything from the server is inserted with textContent, never as HTML.
import { createPicker } from '/picker.js';

const TARGET_NAMES = { cd: 'CD/DVD', removable: 'Removable', floppy: 'Floppy' };
const TARGETS = ['cd', 'removable', 'floppy'];
const POLL_OPEN_MS = 1000;   // while the panel is open: the graph is live
const POLL_CLOSED_MS = 3000; // otherwise: just the toolbar badge (1 s while a drive is mapped)

export function formatBytes(n) {
  if (n < 1024) return `${n} B`;
  const units = ['KiB', 'MiB', 'GiB', 'TiB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`;
}

function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  Object.assign(node, props);
  for (const c of children) node.append(c);
  return node;
}

export function setupMedia(apiFetch) {
  const button = document.getElementById('btnMedia');
  const badge = document.getElementById('mediaBadge');
  const modal = document.getElementById('mediaModal');
  const imagesBody = document.getElementById('mediaImages');
  const drivesBody = document.getElementById('mediaDrives');
  const errorLine = document.getElementById('mediaError');
  const rates = document.getElementById('mediaRates');
  const graph = document.getElementById('mediaGraph');
  const statMedia = document.getElementById('statMedia');
  const pick = createPicker(apiFetch);
  let status = null;
  let busy = false; // an action is in flight: keep its message until it answers
  let timer = null;
  let imagesKey = ''; // what the image rows were last built from
  const chosen = {};  // drive picked in each image's dropdown, kept across rebuilds

  function showError(text) {
    errorLine.textContent = text || '';
    errorLine.hidden = !text;
  }

  async function call(method, path, body) {
    const options = { method };
    if (body !== undefined) {
      options.headers = { 'Content-Type': 'application/json' };
      options.body = JSON.stringify(body);
    }
    const resp = await apiFetch(path, options);
    const res = await resp.json().catch(() => ({}));
    if (!resp.ok) throw new Error(res.error || `Failed (HTTP ${resp.status})`);
    return res;
  }

  // act runs an action, then shows the status it returns (or its error).
  async function act(method, path, body, pending, done = '') {
    busy = true;
    if (pending) showError(pending);
    try {
      render(await call(method, path, body));
      showError(done);
    } catch (e) {
      showError(e.message);
    } finally {
      busy = false;
    }
  }

  function imageRow(img) {
    const ro = el('input', { type: 'checkbox', checked: img.read_only });
    ro.disabled = img.kind === 'iso' || Boolean(img.mapped_to);
    ro.title = img.kind === 'iso' ? 'ISO images are always read-only' : 'Change while not mapped';
    ro.addEventListener('change', () => act('PATCH', `/api/media/images/${encodeURIComponent(img.id)}`, { read_only: ro.checked }));

    const drive = el('td');
    const actions = el('td', { className: 'actions' });
    if (img.mapped_to) {
      drive.textContent = TARGET_NAMES[img.mapped_to] || img.mapped_to;
    } else {
      const free = img.targets.filter((t) => !status.drives.some((d) => d.target === t));
      const select = el('select');
      for (const t of free) select.append(el('option', { value: t, textContent: TARGET_NAMES[t] }));
      if (free.includes(chosen[img.id])) select.value = chosen[img.id];
      select.addEventListener('change', () => { chosen[img.id] = select.value; });
      const map = el('button', { textContent: 'Map', disabled: free.length === 0 });
      map.addEventListener('click', () => act('POST', `/api/media/drives/${select.value}`, { image_id: img.id },
        `Mapping ${img.name}… (the BMC can take a few seconds to free a drive)`));
      if (free.length) drive.append(select, map);
      else drive.append(el('span', { className: 'empty', textContent: 'drive in use' }));
      const remove = el('button', { textContent: 'Remove' });
      remove.addEventListener('click', () => act('DELETE', `/api/media/images/${encodeURIComponent(img.id)}`));
      actions.append(remove);
    }
    const name = el('td', { textContent: img.name, title: img.path });
    return el('tr', {}, name, el('td', { className: 'mono', textContent: formatBytes(img.size) }),
      el('td', {}, ro), drive, actions);
  }

  function driveRow(target) {
    const d = status.drives.find((x) => x.target === target);
    const row = el('tr', {}, el('td', { textContent: TARGET_NAMES[target] }));
    if (!d) {
      row.append(el('td', { className: 'empty', textContent: 'empty' }), el('td'), el('td'), el('td'));
      return row;
    }
    const eject = el('button', { textContent: d.mounting ? 'Cancel' : 'Eject' });
    eject.addEventListener('click', () => act('DELETE', `/api/media/drives/${target}`));
    row.append(
      el('td', { textContent: d.mounting ? `${d.image} (mapping…)` : `${d.image}${d.read_only ? ' (read-only)' : ''}` }),
      el('td', { className: 'mono', textContent: formatBytes(d.bytes_read) }),
      el('td', { className: 'mono', textContent: formatBytes(d.bytes_written) }),
      el('td', { className: 'actions' }, eject));
    return row;
  }

  function drawGraph(history) {
    // Draw at the canvas's displayed size and the screen's pixel ratio, so text is not stretched.
    const ratio = window.devicePixelRatio || 1;
    const w = graph.clientWidth || 720;
    const h = graph.clientHeight || 90;
    if (graph.width !== Math.round(w * ratio) || graph.height !== Math.round(h * ratio)) {
      graph.width = Math.round(w * ratio);
      graph.height = Math.round(h * ratio);
    }
    const ctx = graph.getContext('2d');
    ctx.setTransform(ratio, 0, 0, ratio, 0, 0);
    ctx.clearRect(0, 0, w, h);
    const samples = history.slice(-300);
    const max = Math.max(1, ...samples.map((s) => Math.max(s.read, s.written)));
    const css = getComputedStyle(document.documentElement);
    const line = (key, colour) => {
      ctx.strokeStyle = colour;
      ctx.lineWidth = 1.5;
      ctx.beginPath();
      samples.forEach((s, i) => {
        const x = w - (samples.length - 1 - i) * (w / 299);
        const y = h - 4 - (s[key] / max) * (h - 12);
        if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
      });
      ctx.stroke();
    };
    line('read', css.getPropertyValue('--accent').trim() || '#38bdf8');
    line('written', css.getPropertyValue('--warning').trim() || '#f59e0b');
    ctx.fillStyle = css.getPropertyValue('--text-muted').trim() || '#64748b';
    ctx.font = '10px monospace';
    ctx.fillText(`${formatBytes(max)}/s`, 6, 12);
    ctx.fillText('5 min', 6, h - 6);

    const last = samples[samples.length - 1] || { read: 0, written: 0 };
    rates.replaceChildren(
      el('span', { className: 'read', textContent: `read ${formatBytes(last.read)}/s` }), ' · ',
      el('span', { className: 'write', textContent: `write ${formatBytes(last.written)}/s` }));
  }

  // showRate puts a small live bandwidth readout in the stats bar while any drive is mapped.
  function showRate(st, mapped) {
    statMedia.hidden = mapped === 0;
    if (mapped === 0) return;
    const last = (st.history || [])[st.history.length - 1] || { read: 0, written: 0 };
    statMedia.textContent = `VM ↓ ${formatBytes(last.read)}/s ↑ ${formatBytes(last.written)}/s`;
    statMedia.classList.toggle('active', last.read + last.written > 0);
  }

  function render(st) {
    status = st;
    const mapped = st.drives.filter((d) => !d.mounting).length;
    badge.textContent = String(mapped);
    badge.hidden = mapped === 0;
    showRate(st, mapped);
    if (mapped > 0 !== fastPoll) schedule();
    if (!modal.classList.contains('open')) return;

    // Rebuild the image rows only when they would change: rebuilding every poll would close an
    // open dropdown under the user.
    const key = JSON.stringify([st.images, st.drives.map((d) => d.target)]);
    if (key !== imagesKey) {
      imagesKey = key;
      imagesBody.replaceChildren(...(st.images.length
        ? st.images.map(imageRow)
        : [el('tr', {}, el('td', { colSpan: 5, className: 'empty', textContent: 'No images yet: add an .iso or .img by its full path.' }))]));
    }
    drivesBody.replaceChildren(...TARGETS.map(driveRow));
    drawGraph(st.history || []);
    if (!busy) showError(st.error || '');
  }

  async function poll() {
    try {
      const resp = await apiFetch('/api/media');
      if (resp.status === 501) { stop(); return; } // this BMC has no virtual media
      button.hidden = false;
      if (resp.ok) render(await resp.json());
    } catch (e) {
      console.warn('Media poll error:', e);
    }
  }

  let fastPoll = false;

  function stop() {
    clearInterval(timer);
    timer = null;
  }

  // Poll every second while the panel is open or a drive is mapped (the stats bar shows the
  // rate); otherwise every few seconds.
  function schedule() {
    stop();
    fastPoll = Boolean(status && status.drives.some((d) => !d.mounting));
    timer = setInterval(poll, modal.classList.contains('open') || fastPoll ? POLL_OPEN_MS : POLL_CLOSED_MS);
  }

  function open() {
    modal.classList.add('open');
    imagesKey = ''; // rows are not kept up to date while closed
    poll();
    schedule();
  }

  function close() {
    modal.classList.remove('open');
    schedule();
  }

  button.addEventListener('click', open);
  document.getElementById('btnCloseMedia').addEventListener('click', close);
  document.getElementById('btnMediaDone').addEventListener('click', close);
  document.getElementById('mediaAddForm').addEventListener('submit', (e) => {
    e.preventDefault();
    const input = document.getElementById('mediaPath');
    const path = input.value.trim();
    if (!path) return;
    act('POST', '/api/media/images', { path, read_only: document.getElementById('mediaAddReadOnly').checked })
      .then(() => { if (errorLine.hidden) input.value = ''; });
  });
  const createForm = document.getElementById('mediaCreateForm');
  const sizeSelect = document.getElementById('mediaCreateSize');
  const folderInput = document.getElementById('mediaCreateFolder');
  const browseFolder = document.getElementById('btnBrowseFolder');
  createForm.addEventListener('change', () => {
    const fromFolder = createForm.elements.createKind.value === 'folder';
    sizeSelect.hidden = fromFolder;
    folderInput.hidden = !fromFolder;
    browseFolder.hidden = !fromFolder;
  });
  document.getElementById('btnBrowseImage').addEventListener('click', async () => {
    const path = await pick('file', 'Choose an image (.iso or .img)');
    if (path) document.getElementById('mediaPath').value = path;
  });
  browseFolder.addEventListener('click', async () => {
    const path = await pick('dir', 'Choose the folder to copy into the image');
    if (path) folderInput.value = path;
  });
  document.getElementById('btnBrowseSave').addEventListener('click', async () => {
    const label = document.getElementById('mediaCreateLabel').value.trim().toLowerCase();
    const path = await pick('save', 'Where should the new image go?', `${label || 'rescue'}.img`);
    if (path) document.getElementById('mediaCreatePath').value = path;
  });
  createForm.addEventListener('submit', (e) => {
    e.preventDefault();
    const path = document.getElementById('mediaCreatePath').value.trim();
    if (!path) return;
    const body = { path, label: document.getElementById('mediaCreateLabel').value.trim() };
    if (createForm.elements.createKind.value === 'folder') body.folder = folderInput.value.trim();
    else body.size = Number(sizeSelect.value);
    act('POST', '/api/media/create', body, 'Creating image…', 'Image created and added to the list.');
  });
  document.getElementById('btnUsbReset').addEventListener('click', () => {
    act('POST', '/api/media/usb-reset', undefined, 'Resetting USB…',
      'USB reset sent: virtual media, keyboard and mouse reconnect on the host.');
  });

  poll();
  schedule();
}
