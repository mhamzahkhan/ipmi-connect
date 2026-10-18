// IPMI Modern Console - Client App
import { KEY_TO_HID } from '/keymap.js';
import { setupMedia } from '/media.js';
import { setupPaste } from '/paste.js';
import { toast } from '/toast.js';

(function() {
  'use strict';

  // Elements
  const canvas = document.getElementById('screenCanvas');
  const ctx = canvas.getContext('2d');
  const canvasWrapper = document.getElementById('canvasWrapper');
  const connectingOverlay = document.getElementById('connectingOverlay');
  const overlayStatus = document.getElementById('overlayStatus');
  const overlaySubtext = document.getElementById('overlaySubtext');

  const statusDot = document.getElementById('statusDot');
  const serverTarget = document.getElementById('serverTarget');
  const vendorBadge = document.getElementById('vendorBadge');

  const statRes = document.getElementById('statRes');
  const statFPS = document.getElementById('statFPS');
  const statLatency = document.getElementById('statLatency');

  // Modals

  const powerModal = document.getElementById('powerModal');
  const powerModalTitle = document.getElementById('powerModalTitle');
  const powerModalMessage = document.getElementById('powerModalMessage');
  const btnClosePowerModal = document.getElementById('btnClosePowerModal');
  const btnCancelPower = document.getElementById('btnCancelPower');
  const btnConfirmPower = document.getElementById('btnConfirmPower');

  // State
  let ws = null;
  let isConnected = false;
  let currentPowerAction = null;
  let currentPowerTitle = '';
  let paste = null; // paste-as-keystrokes, set up in init

  // sendMessage sends a JSON message on the console socket; false when not connected.
  function sendMessage(msg) {
    if (!ws || ws.readyState !== WebSocket.OPEN) return false;
    ws.send(JSON.stringify(msg));
    return true;
  }

  // Initialize
  // Session: the launch link carries a one-time code in its fragment (#c=...), exchanged once for
  // the per-run token. localStorage is per origin, and the origin includes the port, so other
  // localhost services cannot read it (unlike cookies, which browsers share across ports).
  const TOKEN_KEY = 'ipmi-connect-token';
  let sessionToken = null;
  let signedOut = false;

  async function ensureSession() {
    const match = window.location.hash.match(/[#&]c=([0-9a-f]+)/);
    if (match) {
      history.replaceState(null, '', window.location.pathname); // drop the code from the address bar
      const resp = await fetch('/api/session', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ code: match[1] })
      });
      if (resp.ok) {
        sessionToken = (await resp.json()).token;
        try { localStorage.setItem(TOKEN_KEY, sessionToken); } catch (e) { /* private mode: token lives for this tab */ }
        return true;
      }
    }
    try { sessionToken = localStorage.getItem(TOKEN_KEY); } catch (e) { sessionToken = null; }
    return !!sessionToken;
  }

  function showSignedOut() {
    signedOut = true;
    statusDot.className = 'status-dot';
    connectingOverlay.classList.remove('hidden');
    overlayStatus.textContent = 'Not signed in';
    overlaySubtext.textContent = 'Open the console link printed in the terminal (each link works once).';
  }

  async function apiFetch(path, options = {}) {
    const headers = Object.assign({}, options.headers, { Authorization: `Bearer ${sessionToken}` });
    const resp = await fetch(path, Object.assign({}, options, { headers }));
    if (resp.status === 401) {
      try { localStorage.removeItem(TOKEN_KEY); } catch (e) { /* ignore */ }
      showSignedOut();
    }
    return resp;
  }

  async function init() {
    if (!(await ensureSession())) {
      showSignedOut();
      return;
    }
    setupWebSocket();
    setupMouseAndKeyboard();
    setupPowerButtons();
    setupMacros();
    setupMedia(apiFetch);
    paste = setupPaste(sendMessage);
    setupFullscreenAndScreenshot();
    pollStatus();
    setInterval(pollStatus, 3000);
  }

  // Poll status API
  async function pollStatus() {
    try {
      if (signedOut) return;
      const resp = await apiFetch('/api/status');
      if (!resp.ok) return;
      const data = await resp.json();

      serverTarget.textContent = data.host || '127.0.0.1';
      vendorBadge.textContent = (data.target_type || 'IPMI').toUpperCase();
      // The server decodes the stream, so it reports the BMC's frame rate; the browser only sees
      // changes.
      statFPS.textContent = `FPS: ${data.fps || 0}`;
      showPower(data.power_state);
      // Console notices such as "No video signal" or "View only"; empty when all is well.
      const notice = document.getElementById('statNotice');
      notice.textContent = data.status_text || '';
      notice.hidden = !data.status_text;

      // Canvas size is owned by the video stream: resizing a canvas clears it, so status polls
      // must never touch it.
    } catch (e) {
      console.warn('Status poll error:', e);
    }
  }

  // Power state: a pill in the header, and buttons that make no sense in that state are disabled.
  const POWER_ONLY_WHEN_ON = ['btnPowerSoft', 'btnPowerOff', 'btnPowerReset', 'btnPowerCycle'];
  function showPower(state) {
    const pill = document.getElementById('powerPill');
    const on = state === 'ON';
    const off = state === 'OFF';
    pill.textContent = on ? 'Power On' : off ? 'Power Off' : 'Power ?';
    pill.classList.toggle('on', on);
    pill.classList.toggle('off', off);
    document.getElementById('btnPowerOn').disabled = on;
    for (const id of POWER_ONLY_WHEN_ON) document.getElementById(id).disabled = off;
  }

  // After a power action the server re-reads the state every second for 30 s; follow it.
  let fastStatusUntil = 0;
  function followPower() {
    fastStatusUntil = Date.now() + 30000;
    const tick = () => {
      pollStatus();
      if (Date.now() < fastStatusUntil) setTimeout(tick, 1000);
    };
    setTimeout(tick, 1000);
  }

  // WebSocket Setup
  function setupWebSocket() {
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${proto}//${window.location.host}/ws?t=${encodeURIComponent(sessionToken)}`;

    ws = new WebSocket(wsUrl);
    ws.binaryType = 'arraybuffer';

    ws.onopen = () => {
      isConnected = true;
      statusDot.className = 'status-dot connected';
      overlayStatus.textContent = 'Active Console Stream';
      overlaySubtext.textContent = 'Receiving video frames';
      setTimeout(() => {
        connectingOverlay.classList.add('hidden');
      }, 500);
    };

    ws.onclose = () => {
      if (signedOut) return;
      isConnected = false;
      statusDot.className = 'status-dot';
      connectingOverlay.classList.remove('hidden');
      overlayStatus.textContent = 'Connection Closed';
      overlaySubtext.textContent = 'Attempting to reconnect in 3s...';
      if (!signedOut) setTimeout(setupWebSocket, 3000);
    };

    ws.onerror = (err) => {
      console.error('WebSocket error:', err);
    };

    ws.onmessage = (event) => {
      if (typeof event.data === 'string') {
        try {
          const msg = JSON.parse(event.data);
          handleTextMessage(msg);
        } catch (e) {}
      } else if (event.data instanceof ArrayBuffer) {
        handleBinaryFrame(event.data);
      }
    };
  }

  // Handle server text message
  function handleTextMessage(msg) {
    if (msg.type === 'resize') {
      canvas.width = msg.width;
      canvas.height = msg.height;
      statRes.textContent = `Resolution: ${msg.width} x ${msg.height}`;
    } else if (msg.type === 'status') {
      overlayStatus.textContent = msg.text || 'Processing...';
    } else if (msg.type === 'typed') {
      paste.onResult(msg);
    } else if (msg.type === 'ping') {
      const now = performance.now();
      const rtt = Math.round(now - msg.client_time);
      statLatency.textContent = `Ping: ${rtt} ms`;
    }
  }

  // Video: the server decodes the BMC stream and sends RGBA.
  //   0x02 full frame: [u16 width][u16 height][RGBA]
  //   0x04 changes:    [u16 count] then per rect [u16 x][u16 y][u16 w][u16 h][RGBA]
  function handleBinaryFrame(buf) {
    const view = new DataView(buf);
    const type = view.getUint8(0);
    if (type === 0x02) {
      const w = view.getUint16(1);
      const h = view.getUint16(3);
      if (canvas.width !== w || canvas.height !== h) {
        canvas.width = w; // resizing clears the canvas; the full frame repaints it
        canvas.height = h;
        statRes.textContent = `Resolution: ${w} x ${h}`;
      }
      ctx.putImageData(new ImageData(new Uint8ClampedArray(buf, 5, w * h * 4), w, h), 0, 0);
    } else if (type === 0x04) {
      let off = 3;
      for (let n = view.getUint16(1); n > 0; n--) {
        const x = view.getUint16(off), y = view.getUint16(off + 2);
        const w = view.getUint16(off + 4), h = view.getUint16(off + 6);
        off += 8;
        ctx.putImageData(new ImageData(new Uint8ClampedArray(buf, off, w * h * 4), w, h), x, y);
        off += w * h * 4;
      }
    }
  }

  // Mouse and Keyboard Input Capture
  function setupMouseAndKeyboard() {
    canvas.tabIndex = 1000;
    let currentButtons = 0;

    function send(msg) {
      if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg));
    }

    function sendPointer(mask, clientX, clientY) {
      const rect = canvas.getBoundingClientRect();
      const canvasX = Math.round((clientX - rect.left) * (canvas.width / rect.width));
      const canvasY = Math.round((clientY - rect.top) * (canvas.height / rect.height));
      send({
        type: 'pointer',
        mask: mask,
        x: Math.max(0, Math.min(canvas.width - 1, canvasX)),
        y: Math.max(0, Math.min(canvas.height - 1, canvasY))
      });
    }

    // Ask the server to release every key and button this tab is holding on the host.
    function releaseAll() {
      currentButtons = 0;
      send({ type: 'release' });
    }

    // RFB button bits: left 1, middle 2, right 4 (the server converts per BMC).
    const BUTTON_BITS = [1, 2, 4];

    canvas.addEventListener('mousemove', (e) => {
      sendPointer(currentButtons, e.clientX, e.clientY);
    });

    canvas.addEventListener('mousedown', (e) => {
      e.preventDefault();
      canvas.focus();
      currentButtons |= BUTTON_BITS[e.button] || 0;
      sendPointer(currentButtons, e.clientX, e.clientY);
    });

    // On window, not the canvas: a button released outside the console must still be released.
    window.addEventListener('mouseup', (e) => {
      const bit = BUTTON_BITS[e.button] || 0;
      if (!(currentButtons & bit)) return;
      currentButtons &= ~bit;
      sendPointer(currentButtons, e.clientX, e.clientY);
    });

    canvas.addEventListener('contextmenu', (e) => e.preventDefault());

    function consoleFocused() {
      return document.activeElement === canvas || canvasWrapper.contains(document.activeElement);
    }

    window.addEventListener('keydown', (e) => {
      if (!consoleFocused()) return;
      // Ctrl/Cmd+Shift+V types the clipboard on the host instead of sending the chord.
      if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.code === 'KeyV') {
        e.preventDefault();
        releaseAll();
        paste.fromClipboard();
        return;
      }
      // Keys belong to the host while the console has focus: stop Backspace, Space, arrows, Tab,
      // F-keys and browser shortcuts acting on this page. (The OS still keeps a few, like Cmd+Q.)
      e.preventDefault();
      if (e.repeat) return; // the host's own key repeat handles held keys
      send({ type: 'key', down: true, hidCode: KEY_TO_HID[e.code] || 0 });
    });

    window.addEventListener('keyup', (e) => {
      if (!consoleFocused()) return;
      e.preventDefault();
      send({ type: 'key', down: false, hidCode: KEY_TO_HID[e.code] || 0 });
      // macOS browsers send no keyup for keys released while Cmd is held, so Cmd+C would leave
      // C held on the host. Releasing Cmd releases everything.
      if (e.code === 'MetaLeft' || e.code === 'MetaRight') releaseAll();
    });

    // Focus leaving the page means keyups will not arrive here: release what is held.
    window.addEventListener('blur', releaseAll);
    document.addEventListener('visibilitychange', () => {
      if (document.hidden) releaseAll();
    });
  }

  // Macro Toolbar
  function setupMacros() {
    document.querySelectorAll('.btn-macro').forEach(btn => {
      btn.addEventListener('click', () => {
        const macro = btn.getAttribute('data-macro');
        if (ws && ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({
            type: 'macro',
            name: macro
          }));
        }
      });
    });
  }

  // Power Controls
  function setupPowerButtons() {
    function openPowerConfirm(action, title, msg) {
      currentPowerAction = action;
      currentPowerTitle = title.replace(/^Confirm /, '');
      powerModalTitle.textContent = title;
      powerModalMessage.textContent = msg;
      powerModal.classList.add('open');
    }

    document.getElementById('btnPowerSoft').addEventListener('click', () => {
      openPowerConfirm('power_soft_off', 'Confirm Shut Down', 'Ask the operating system to shut down cleanly (like pressing the power button)? The host may take a while, or ignore it if no OS is running.');
    });
    document.getElementById('btnPowerOn').addEventListener('click', () => {
      openPowerConfirm('power_on', 'Confirm Power On', 'Power on host system now?');
    });
    document.getElementById('btnPowerOff').addEventListener('click', () => {
      openPowerConfirm('power_off', 'Confirm Force Power Off', 'Immediately cut power to host server? Warning: Unsaved data may be lost.');
    });
    document.getElementById('btnPowerReset').addEventListener('click', () => {
      openPowerConfirm('power_reset', 'Confirm Hard Reset', 'Issue an immediate hardware reset to the host system?');
    });
    document.getElementById('btnPowerCycle').addEventListener('click', () => {
      openPowerConfirm('power_cycle', 'Confirm Power Cycle', 'Turn off and restart the host system?');
    });

    btnClosePowerModal.addEventListener('click', () => powerModal.classList.remove('open'));
    btnCancelPower.addEventListener('click', () => powerModal.classList.remove('open'));

    btnConfirmPower.addEventListener('click', async () => {
      if (!currentPowerAction) return;
      powerModal.classList.remove('open');
      try {
        const resp = await apiFetch('/api/power', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ action: currentPowerAction })
        });
        const res = await resp.json();
        if (!resp.ok || res.error) {
          toast(`Power action failed: ${res.error || 'server error'}`, 'error', 6000);
        } else {
          toast(`${currentPowerTitle} sent`, 'success');
          followPower();
        }
      } catch (err) {
        toast(`Power action failed: ${err.message}`, 'error', 6000);
      }
    });
  }

  // Fullscreen & Screenshot
  function setupFullscreenAndScreenshot() {
    document.getElementById('btnFullscreen').addEventListener('click', () => {
      if (!document.fullscreenElement) {
        document.documentElement.requestFullscreen().catch(err => {});
      } else {
        document.exitFullscreen();
      }
    });

    document.getElementById('btnScreenshot').addEventListener('click', () => {
      const a = document.createElement('a');
      a.download = `ipmi-console-${Date.now()}.png`;
      a.href = canvas.toDataURL('image/png');
      a.click();
    });
  }

  // Start app
  window.addEventListener('DOMContentLoaded', init);
})();
