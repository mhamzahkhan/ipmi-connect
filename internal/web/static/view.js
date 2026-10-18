// Screen scaling, fullscreen with keyboard lock, and a guard against closing the tab by accident.

const MODE_KEY = 'ipmi-connect-scale';
const MODES = ['fit', '1:1', 'integer'];
const LABELS = { fit: 'Fit', '1:1': '1:1', integer: 'Integer' };

function load() {
  try { return MODES.includes(localStorage.getItem(MODE_KEY)) ? localStorage.getItem(MODE_KEY) : 'fit'; } catch { return 'fit'; }
}

function save(mode) {
  try { localStorage.setItem(MODE_KEY, mode); } catch { /* ignore */ }
}

// scaleFor returns the display scale for a w x h picture in a vw x vh area.
export function scaleFor(mode, w, h, vw, vh) {
  if (!w || !h || mode === '1:1') return 1;
  const fit = Math.min(vw / w, vh / h);
  if (mode === 'integer') return Math.max(1, Math.floor(fit));
  return fit;
}

export function setupView(canvas, viewport, isConnected) {
  const button = document.getElementById('btnScale');
  const label = document.getElementById('scaleLabel');
  let mode = load();

  function apply() {
    const style = getComputedStyle(viewport);
    const vw = viewport.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight) - 2;
    const vh = viewport.clientHeight - parseFloat(style.paddingTop) - parseFloat(style.paddingBottom) - 2;
    const s = scaleFor(mode, canvas.width, canvas.height, vw, vh);
    canvas.style.width = `${Math.floor(canvas.width * s)}px`;
    canvas.style.height = `${Math.floor(canvas.height * s)}px`;
    // Whole-number scales stay crisp; others are smoothed so text stays readable.
    canvas.style.imageRendering = Number.isInteger(s) ? 'pixelated' : 'auto';
    label.textContent = LABELS[mode];
    button.title = `Scaling: ${LABELS[mode]} (${Math.round(s * 100)}%). Click to change.`;
  }

  button.addEventListener('click', () => {
    mode = MODES[(MODES.indexOf(mode) + 1) % MODES.length];
    save(mode);
    apply();
  });
  new ResizeObserver(apply).observe(viewport);
  // The video stream changes the canvas's pixel size when the host changes resolution.
  new MutationObserver(apply).observe(canvas, { attributes: true, attributeFilter: ['width', 'height'] });

  // Fullscreen the console area and, where supported (Chromium), lock the keyboard so Esc,
  // Cmd/Ctrl+W, Alt+Tab and friends reach the host. Holding Esc leaves fullscreen.
  document.getElementById('btnFullscreen').addEventListener('click', async () => {
    if (document.fullscreenElement) {
      await document.exitFullscreen();
      return;
    }
    try {
      await viewport.requestFullscreen();
      await navigator.keyboard?.lock?.();
    } catch { /* fullscreen or lock refused */ }
  });
  document.addEventListener('fullscreenchange', () => {
    if (!document.fullscreenElement) navigator.keyboard?.unlock?.();
    apply();
  });

  // A stray Cmd/Ctrl+W should not end the session.
  window.addEventListener('beforeunload', (e) => {
    if (isConnected()) e.preventDefault();
  });

  apply();
}
