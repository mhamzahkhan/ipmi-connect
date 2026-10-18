// Small transient notifications in the corner, instead of blocking alert() dialogs.
const DEFAULT_MS = 4000;

export function toast(text, kind = 'info', ms = DEFAULT_MS) {
  let box = document.getElementById('toasts');
  if (!box) {
    box = document.createElement('div');
    box.id = 'toasts';
    box.setAttribute('role', 'status');
    box.setAttribute('aria-live', 'polite');
    document.body.append(box);
  }
  const item = document.createElement('div');
  item.className = `toast toast-${kind}`;
  item.textContent = text;
  item.addEventListener('click', () => item.remove());
  box.append(item);
  setTimeout(() => item.classList.add('leaving'), ms);
  setTimeout(() => item.remove(), ms + 300);
}
