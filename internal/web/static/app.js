// IPMI Modern Console - Client App
import { decode } from '/aspeed/decoder.js';
import { buildContrastLUT, applyLUT, identityLUT } from '/contrast.js';
import { KEY_TO_HID } from '/keymap.js';

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
  const vmediaModal = document.getElementById('vmediaModal');
  const btnOpenVMedia = document.getElementById('btnOpenVMedia');
  const btnCloseVMedia = document.getElementById('btnCloseVMedia');
  const btnCancelVMedia = document.getElementById('btnCancelVMedia');
  const dropzone = document.getElementById('dropzone');
  const fileInput = document.getElementById('fileInput');
  const btnBrowseFile = document.getElementById('btnBrowseFile');
  const selectedFileName = document.getElementById('selectedFileName');
  const btnMount = document.getElementById('btnMount');
  const btnUnmount = document.getElementById('btnUnmount');
  const vmediaPill = document.getElementById('vmediaPill');

  const powerModal = document.getElementById('powerModal');
  const powerModalTitle = document.getElementById('powerModalTitle');
  const powerModalMessage = document.getElementById('powerModalMessage');
  const btnClosePowerModal = document.getElementById('btnClosePowerModal');
  const btnCancelPower = document.getElementById('btnCancelPower');
  const btnConfirmPower = document.getElementById('btnConfirmPower');

  // State
  let ws = null;
  let isConnected = false;
  let frameCount = 0;
  let lastFPSCheck = performance.now();
  let currentPowerAction = null;
  let selectedFile = null;
  let autoContrastEnabled = true;
  let contrastLUT = identityLUT();
  let lastCachedFrame = null;

  // Initialize
  function init() {
    setupWebSocket();
    setupMouseAndKeyboard();
    setupPowerButtons();
    setupVirtualMedia();
    setupMacros();
    setupFullscreenAndScreenshot();
    setupAutoContrast();
    pollStatus();
    setInterval(pollStatus, 3000);
  }

  // Poll status API
  async function pollStatus() {
    try {
      const resp = await fetch('/api/status');
      if (!resp.ok) return;
      const data = await resp.json();

      serverTarget.textContent = data.host || '127.0.0.1';
      vendorBadge.textContent = (data.target_type || 'IPMI').toUpperCase();

      if (data.virtual_media) {
        vmediaPill.textContent = data.virtual_media;
        vmediaPill.style.backgroundColor = 'rgba(16, 185, 129, 0.2)';
        vmediaPill.style.color = '#10b981';
        btnUnmount.disabled = false;
      } else {
        vmediaPill.textContent = 'None';
        vmediaPill.style.backgroundColor = 'var(--border-color)';
        vmediaPill.style.color = 'var(--text-muted)';
        btnUnmount.disabled = true;
      }

      // Canvas size is owned by the video stream: resizing a canvas clears it, so status polls
      // must never touch it.
    } catch (e) {
      console.warn('Status poll error:', e);
    }
  }

  // WebSocket Setup
  function setupWebSocket() {
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${proto}//${window.location.host}/ws`;

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
      isConnected = false;
      statusDot.className = 'status-dot';
      connectingOverlay.classList.remove('hidden');
      overlayStatus.textContent = 'Connection Closed';
      overlaySubtext.textContent = 'Attempting to reconnect in 3s...';
      setTimeout(setupWebSocket, 3000);
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
    } else if (msg.type === 'ping') {
      const now = performance.now();
      const rtt = Math.round(now - msg.client_time);
      statLatency.textContent = `Ping: ${rtt} ms`;
    }
  }

  // True-black calibration (see contrast.js)
  function calibrateContrast(pixels) {
    contrastLUT = autoContrastEnabled ? buildContrastLUT(pixels) : identityLUT();
  }

  function applyContrast(pixels) {
    if (!autoContrastEnabled) return;
    applyLUT(contrastLUT, pixels);
  }

  // Handle video frame
  function handleBinaryFrame(buf) {
    frameCount++;
    const now = performance.now();
    if (now - lastFPSCheck >= 1000) {
      statFPS.textContent = `FPS: ${frameCount}`;
      frameCount = 0;
      lastFPSCheck = now;
    }

    const view = new DataView(buf);
    const magic = view.getUint8(0);

    if (magic === 0x01) {
      // JPEG / PNG Frame
      const blob = new Blob([new Uint8Array(buf, 5)], { type: 'image/jpeg' });
      createImageBitmap(blob).then(bitmap => {
        ctx.drawImage(bitmap, 0, 0, canvas.width, canvas.height);
        bitmap.close();
      }).catch(err => {
        const img = new Image();
        img.onload = () => {
          ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
          URL.revokeObjectURL(img.src);
        };
        img.src = URL.createObjectURL(blob);
      });
    } else if (magic === 0x02) {
      // Raw RGBA 32-bit Frame
      const w = view.getUint16(1);
      const h = view.getUint16(3);
      if (canvas.width !== w || canvas.height !== h) {
        canvas.width = w;
        canvas.height = h;
        statRes.textContent = `Resolution: ${w} x ${h}`;
      }
      lastCachedFrame = buf.slice(0);
      const rawPixels = new Uint8ClampedArray(buf.slice(5));
      calibrateContrast(rawPixels);
      applyContrast(rawPixels);
      const imgData = new ImageData(rawPixels, w, h);
      ctx.putImageData(imgData, 0, 0);
    } else if (magic === 0x03) {
      // Aspeed Compressed Frame / Tile
      // Bytes 1: mode420, 2: selector, 3: advance_selector
      // Bytes 4..5: X, 6..7: Y, 8..9: Width, 10..11: Height, 12..: Bitstream
      const mode420 = view.getUint8(1);
      const selector = view.getUint8(2);
      const advanceSelector = view.getUint8(3);
      const x = view.getUint16(4);
      const y = view.getUint16(6);
      const w = view.getUint16(8);
      const h = view.getUint16(10);
      const bitstream = new Uint8Array(buf, 12);

      const header = {
        width: w,
        height: h,
        mode420: mode420,
        selector: selector,
        advance_selector: advanceSelector
      };

      try {
        const pixels = decode(header, bitstream);
        const isFullFrame = (x === 0 && y === 0 && w >= 320 && h >= 200);
        if (isFullFrame) {
          if (canvas.width !== w || canvas.height !== h) {
            canvas.width = w;
            canvas.height = h;
            statRes.textContent = `Resolution: ${w} x ${h}`;
          }
          lastCachedFrame = buf.slice(0);
          calibrateContrast(pixels);
        }

        applyContrast(pixels);

        const slice = (pixels instanceof Uint8ClampedArray)
          ? pixels
          : new Uint8ClampedArray(pixels.buffer, pixels.byteOffset, w * h * 4);
        const imgData = new ImageData(slice, w, h);
        ctx.putImageData(imgData, x, y);
      } catch (err) {
        console.warn('Aspeed decode error:', err);
      }
    }
  }

  // Mouse and Keyboard Input Capture
  function setupMouseAndKeyboard() {
    canvas.tabIndex = 1000;

    function sendPointer(mask, clientX, clientY) {
      if (!ws || ws.readyState !== WebSocket.OPEN) return;
      const rect = canvas.getBoundingClientRect();
      const scaleX = canvas.width / rect.width;
      const scaleY = canvas.height / rect.height;
      const canvasX = Math.round((clientX - rect.left) * scaleX);
      const canvasY = Math.round((clientY - rect.top) * scaleY);

      ws.send(JSON.stringify({
        type: 'pointer',
        mask: mask,
        x: Math.max(0, Math.min(canvas.width - 1, canvasX)),
        y: Math.max(0, Math.min(canvas.height - 1, canvasY))
      }));
    }

    let currentButtons = 0;

    canvas.addEventListener('mousemove', (e) => {
      sendPointer(currentButtons, e.clientX, e.clientY);
    });

    canvas.addEventListener('mousedown', (e) => {
      e.preventDefault();
      canvas.focus();
      if (e.button === 0) currentButtons |= 1; // Left
      if (e.button === 1) currentButtons |= 2; // Middle
      if (e.button === 2) currentButtons |= 4; // Right
      sendPointer(currentButtons, e.clientX, e.clientY);
    });

    canvas.addEventListener('mouseup', (e) => {
      e.preventDefault();
      if (e.button === 0) currentButtons &= ~1;
      if (e.button === 1) currentButtons &= ~2;
      if (e.button === 2) currentButtons &= ~4;
      sendPointer(currentButtons, e.clientX, e.clientY);
    });

    canvas.addEventListener('contextmenu', (e) => {
      e.preventDefault();
    });

    // Keyboard capture
    window.addEventListener('keydown', (e) => {
      if (document.activeElement !== canvas && !canvasWrapper.contains(document.activeElement)) {
        return;
      }
      if (e.key.startsWith('F') || e.key === 'Tab' || e.key === 'Alt') {
        e.preventDefault();
      }
      if (ws && ws.readyState === WebSocket.OPEN) {
        const hid = KEY_TO_HID[e.code] || 0;
        ws.send(JSON.stringify({
          type: 'key',
          down: true,
          key: e.key,
          code: e.code,
          keyCode: e.keyCode,
          hidCode: hid
        }));
      }
    });

    window.addEventListener('keyup', (e) => {
      if (document.activeElement !== canvas && !canvasWrapper.contains(document.activeElement)) {
        return;
      }
      if (ws && ws.readyState === WebSocket.OPEN) {
        const hid = KEY_TO_HID[e.code] || 0;
        ws.send(JSON.stringify({
          type: 'key',
          down: false,
          key: e.key,
          code: e.code,
          keyCode: e.keyCode,
          hidCode: hid
        }));
      }
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
      powerModalTitle.textContent = title;
      powerModalMessage.textContent = msg;
      powerModal.classList.add('open');
    }

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
        const resp = await fetch('/api/power', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ action: currentPowerAction })
        });
        const res = await resp.json();
        if (!resp.ok || res.error) {
          alert(`Power action failed: ${res.error || 'Server error'}`);
        } else {
          console.log(`Power action executed successfully:`, res);
        }
      } catch (err) {
        alert(`Failed to execute power action: ${err.message}`);
      }
    });
  }

  // Virtual Media
  function setupVirtualMedia() {
    btnOpenVMedia.addEventListener('click', () => {
      vmediaModal.classList.add('open');
    });

    btnCloseVMedia.addEventListener('click', () => vmediaModal.classList.remove('open'));
    btnCancelVMedia.addEventListener('click', () => vmediaModal.classList.remove('open'));

    btnBrowseFile.addEventListener('click', () => fileInput.click());

    fileInput.addEventListener('change', (e) => {
      if (e.target.files.length > 0) {
        selectedFile = e.target.files[0];
        selectedFileName.textContent = `${selectedFile.name} (${(selectedFile.size / (1024*1024)).toFixed(1)} MB)`;
        btnMount.disabled = false;
      }
    });

    // Drag and drop
    dropzone.addEventListener('dragover', (e) => {
      e.preventDefault();
      dropzone.classList.add('dragover');
    });
    dropzone.addEventListener('dragleave', () => dropzone.classList.remove('dragover'));
    dropzone.addEventListener('drop', (e) => {
      e.preventDefault();
      dropzone.classList.remove('dragover');
      if (e.dataTransfer.files.length > 0) {
        selectedFile = e.dataTransfer.files[0];
        selectedFileName.textContent = `${selectedFile.name} (${(selectedFile.size / (1024*1024)).toFixed(1)} MB)`;
        btnMount.disabled = false;
      }
    });

    btnMount.addEventListener('click', async () => {
      if (!selectedFile) return;
      btnMount.disabled = true;
      btnMount.textContent = 'Mounting...';

      const formData = new FormData();
      formData.append('file', selectedFile);
      formData.append('readonly', document.getElementById('chkReadOnly').checked);

      try {
        const resp = await fetch('/api/vmedia/mount', {
          method: 'POST',
          body: formData
        });
        const res = await resp.json();
        if (resp.ok) {
          vmediaModal.classList.remove('open');
          pollStatus();
          alert(`Virtual Media Mounted: ${selectedFile.name}`);
        } else {
          alert(`Mount error: ${res.error || 'Failed'}`);
        }
      } catch (err) {
        alert(`Error mounting ISO: ${err.message}`);
      } finally {
        btnMount.textContent = 'Mount to Server';
      }
    });

    btnUnmount.addEventListener('click', async () => {
      btnUnmount.disabled = true;
      btnUnmount.textContent = 'Ejecting...';
      try {
        const resp = await fetch('/api/vmedia/unmount', { method: 'POST' });
        if (resp.ok) {
          selectedFile = null;
          selectedFileName.textContent = 'No file selected';
          vmediaModal.classList.remove('open');
          pollStatus();
          alert('Virtual Media ejected successfully.');
        }
      } catch (err) {
        alert(`Eject error: ${err.message}`);
      } finally {
        btnUnmount.textContent = 'Eject / Unmount';
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

  // Auto-Contrast / True Black Calibration Setup
  function setupAutoContrast() {
    const btn = document.getElementById('btnAutoContrast');
    if (!btn) return;

    btn.addEventListener('click', () => {
      autoContrastEnabled = !autoContrastEnabled;
      if (autoContrastEnabled) {
        btn.classList.add('active');
      } else {
        btn.classList.remove('active');
        contrastLUT = identityLUT();
      }

      // Re-render cached frame immediately if available
      if (lastCachedFrame) {
        handleBinaryFrame(lastCachedFrame);
      }

      // Request fresh full frame from BMC
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'refresh' }));
      }
    });
  }

  // Start app
  window.addEventListener('DOMContentLoaded', init);
})();
