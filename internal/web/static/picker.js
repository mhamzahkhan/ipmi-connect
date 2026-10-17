// Local file picker. The server runs on this machine and lists folders for us: browsers never
// reveal a picked file's real path. Server data goes into the page with textContent only.

const LAST_KEY = 'ipmi-connect-picker-dir';

function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  Object.assign(node, props);
  for (const c of children) node.append(c);
  return node;
}

function formatSize(n) {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return `${i ? n.toFixed(1) : n} ${units[i]}`;
}

function remembered() {
  try { return localStorage.getItem(LAST_KEY) || ''; } catch { return ''; }
}

function remember(path) {
  try { localStorage.setItem(LAST_KEY, path); } catch { /* ignore */ }
}

// createPicker returns pick(mode, title, suggestedName): a promise of the chosen path, or null.
// mode is 'file' (an .iso or .img), 'dir' (a folder) or 'save' (a folder plus a new file name).
export function createPicker(apiFetch) {
  const modal = document.getElementById('pickerModal');
  const title = document.getElementById('pickerTitle');
  const pathLabel = document.getElementById('pickerPath');
  const list = document.getElementById('pickerList');
  const error = document.getElementById('pickerError');
  const nameInput = document.getElementById('pickerName');
  const choose = document.getElementById('pickerChoose');
  let state = null; // { mode, path, parent, resolve }

  function finish(result) {
    modal.classList.remove('open');
    const s = state;
    state = null;
    if (s) s.resolve(result);
  }

  async function load(path) {
    const q = new URLSearchParams({ mode: state.mode === 'file' ? 'file' : 'dir' });
    if (path) q.set('path', path);
    const resp = await apiFetch(`/api/files?${q}`);
    if (!resp.ok) {
      error.textContent = (await resp.text()).trim() || 'Cannot open that folder';
      error.hidden = false;
      if (path) return load(''); // fall back to the home folder
      return undefined;
    }
    error.hidden = true;
    const data = await resp.json();
    state.path = data.path;
    state.parent = data.parent;
    remember(data.path);
    pathLabel.replaceChildren(el('span', { textContent: data.path }));
    pathLabel.title = data.path;
    list.replaceChildren(...data.entries.map((e) => {
      const item = el('li', { className: e.dir ? 'dir' : 'file', tabIndex: 0 },
        el('span', { className: 'picker-name', textContent: e.dir ? `${e.name}/` : e.name }),
        el('span', { className: 'picker-size', textContent: e.dir ? '' : formatSize(e.size) }));
      const open = () => (e.dir ? load(`${data.path}/${e.name}`.replace(/^\/\//, '/')) : finish(`${data.path}/${e.name}`));
      item.addEventListener('click', open);
      item.addEventListener('keydown', (ev) => { if (ev.key === 'Enter') open(); });
      return item;
    }));
    if (!data.entries.length) {
      list.append(el('li', { className: 'empty', textContent: state.mode === 'file' ? 'No folders or images here.' : 'No folders here.' }));
    }
  }

  document.getElementById('pickerUp').addEventListener('click', () => state && load(state.parent));
  document.getElementById('pickerHome').addEventListener('click', () => state && load(''));
  document.getElementById('pickerCancel').addEventListener('click', () => finish(null));
  document.getElementById('btnClosePicker').addEventListener('click', () => finish(null));
  choose.addEventListener('click', () => {
    if (!state) return;
    if (state.mode === 'dir') return finish(state.path);
    const name = nameInput.value.trim();
    if (!name || name.includes('/')) {
      error.textContent = 'Give a file name (without a folder).';
      error.hidden = false;
      return undefined;
    }
    return finish(`${state.path}/${name.toLowerCase().endsWith('.img') ? name : `${name}.img`}`.replace(/^\/\//, '/'));
  });

  return function pick(mode, heading, suggestedName = '') {
    if (state) finish(null);
    return new Promise((resolve) => {
      state = { mode, path: '', parent: '', resolve };
      title.textContent = heading;
      nameInput.hidden = mode !== 'save';
      nameInput.value = suggestedName;
      choose.hidden = mode === 'file';
      choose.textContent = mode === 'save' ? 'Save Here' : 'Use This Folder';
      error.hidden = true;
      modal.classList.add('open');
      load(remembered());
    });
  };
}
