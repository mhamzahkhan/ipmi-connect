// Paste as keystrokes: the server types text on the host as US-layout key presses.
import { toast } from '/toast.js';

const MAX_CHARS = 16384;
const MAX_MESSAGE_BYTES = 6 * MAX_CHARS + 1024; // the server's WebSocket message limit

export function setupPaste(sendMessage) {
  const modal = document.getElementById('pasteModal');
  const text = document.getElementById('pasteText');
  const count = document.getElementById('pasteCount');
  const typeBtn = document.getElementById('btnPasteType');
  const cancelBtn = document.getElementById('btnPasteCancel');
  let typing = false;
  let currentId = ''; // the paste in flight: replies for any other id are ignored

  function setTyping(on) {
    typing = on;
    typeBtn.disabled = on;
    typeBtn.textContent = on ? 'Typing…' : 'Type It';
    cancelBtn.textContent = on ? 'Stop Typing' : 'Cancel';
  }

  function start(value) {
    if (!value || typing) return;
    if ([...value].length > MAX_CHARS) {
      toast(`That is more than ${MAX_CHARS} characters`, 'error');
      return;
    }
    currentId = Math.random().toString(36).slice(2);
    const msg = { type: 'type', id: currentId, text: value };
    if (new TextEncoder().encode(JSON.stringify(msg)).length > MAX_MESSAGE_BYTES) {
      toast('That text is too large to send in one paste', 'error');
      return;
    }
    if (!sendMessage(msg)) {
      toast('Not connected to the console', 'error');
      return;
    }
    setTyping(true);
    toast(`Typing ${[...value].length} characters on the host…`);
  }

  function updateCount() {
    count.textContent = `${[...text.value].length} / ${MAX_CHARS}`;
  }

  document.getElementById('btnPaste').addEventListener('click', async () => {
    modal.classList.add('open');
    // Prefill from the clipboard when the browser allows it; the user can edit before typing.
    if (!text.value && navigator.clipboard?.readText) {
      try { text.value = await navigator.clipboard.readText(); } catch { /* permission denied */ }
    }
    updateCount();
    text.focus();
  });
  text.addEventListener('input', updateCount);
  typeBtn.addEventListener('click', () => start(text.value));
  cancelBtn.addEventListener('click', () => {
    if (typing) sendMessage({ type: 'type-cancel' });
    else modal.classList.remove('open');
  });
  document.getElementById('btnClosePaste').addEventListener('click', () => modal.classList.remove('open'));

  return {
    // fromClipboard types the clipboard straight away (Ctrl/Cmd+Shift+V on the console).
    async fromClipboard() {
      try {
        start(await navigator.clipboard.readText());
      } catch {
        toast('The browser blocked clipboard access: use the Paste button instead', 'error');
      }
    },
    // onResult handles the server's {"type":"typed"} reply for the paste in flight.
    onResult(msg) {
      if (!typing || msg.id !== currentId) return;
      setTyping(false);
      if (msg.error) {
        toast(`Typing stopped after ${msg.count || 0} characters: ${msg.error}`, 'error', 6000);
        return;
      }
      modal.classList.remove('open');
      text.value = '';
      if (msg.skipped) toast(`Typed ${msg.count} characters; skipped ones a US keyboard cannot type: ${msg.skipped}`, 'error', 8000);
      else toast(`Typed ${msg.count} characters`, 'success');
    },
    // reset abandons a paste when the connection drops: the server has stopped typing it.
    reset() {
      if (!typing) return;
      setTyping(false);
      toast('Connection lost: typing stopped', 'error');
    },
  };
}
