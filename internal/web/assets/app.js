// app.js — small client-only helpers. Most surface state is HTMX
// OOB-swap driven; this file holds the few things that can't be
// expressed declaratively. Alpine adoption is explicitly deferred
// per PLAN-5 / C8.

// Status-banner fallback. The 'connected' SSE event from the server
// is the primary mechanism; these listeners cover the disconnected
// case (when the server stops emitting and the EventSource errors).
(function () {
  function setStatus(text, cls) {
    var el = document.getElementById('status');
    if (!el) return;
    el.textContent = text;
    el.className = cls;
  }
  document.addEventListener('htmx:sseError', function () {
    setStatus('disconnected', 'disconnected');
  });
  document.addEventListener('htmx:sse-error', function () {
    setStatus('disconnected', 'disconnected');
  });
})();

// Auto-scroll the activity feed to the bottom as new entries arrive.
// HTMX OOB-appends each <li> via hx-swap-oob='beforeend:#hooks'; we
// observe child additions and keep the scroll pinned to the latest
// entry unless the user has scrolled up to read history (then we
// respect their position).
(function () {
  document.addEventListener('DOMContentLoaded', function () {
    var hooks = document.getElementById('hooks');
    if (!hooks) return;
    var stickToBottom = true;
    hooks.addEventListener('scroll', function () {
      // 12 px slop — small scroll-up doesn't count as "reading".
      var atBottom = hooks.scrollHeight - hooks.scrollTop - hooks.clientHeight < 12;
      stickToBottom = atBottom;
    });
    var observer = new MutationObserver(function () {
      if (stickToBottom) {
        hooks.scrollTop = hooks.scrollHeight;
      }
    });
    observer.observe(hooks, { childList: true });
  });
})();
