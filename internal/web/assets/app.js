// app.js — small client-only helpers. Most surface state is HTMX
// OOB-swap driven; this file holds the few things that can't be
// expressed declaratively. Alpine adoption is explicitly deferred
// per PLAN-5 / C8.

// Status-banner flip. The hx-on::sse-{open,error} attributes on <body>
// are the primary path, but htmx event-dispatch semantics vary slightly
// between SSE-extension versions. Listening on document covers both
// paths so the banner reliably flips from 'connecting…' to 'connected'.
(function () {
  function setStatus(text, cls) {
    var el = document.getElementById('status');
    if (!el) return;
    el.textContent = text;
    el.className = cls;
  }
  document.addEventListener('htmx:sseOpen', function () {
    setStatus('connected', 'connected');
  });
  document.addEventListener('htmx:sseError', function () {
    setStatus('disconnected', 'disconnected');
  });
  // Fallback for older naming.
  document.addEventListener('htmx:sse-open', function () {
    setStatus('connected', 'connected');
  });
  document.addEventListener('htmx:sse-error', function () {
    setStatus('disconnected', 'disconnected');
  });
})();
