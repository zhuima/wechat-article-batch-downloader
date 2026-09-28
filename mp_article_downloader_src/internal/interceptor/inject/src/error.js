/**
 * Optional page diagnostics. WeChat page errors can contain signed article
 * URLs, so never render their message, stack, source filename, or query string.
 */
(function () {
  var toast;
  var dismissTimer;
  var lastShownAt = 0;

  function ensureToast() {
    if (toast) return toast;
    var style = document.createElement("style");
    style.textContent =
      "#__mp_debug_error_toast{position:fixed;top:16px;right:16px;z-index:2147483647;" +
      "max-width:min(360px,calc(100vw - 32px));padding:10px 14px;border-radius:8px;" +
      "background:#323845;color:#fff;font:14px/1.5 system-ui,sans-serif;" +
      "box-shadow:0 8px 24px rgba(0,0,0,.18);display:none}";
    document.head.appendChild(style);
    toast = document.createElement("div");
    toast.id = "__mp_debug_error_toast";
    toast.setAttribute("role", "status");
    toast.setAttribute("aria-live", "polite");
    document.body.appendChild(toast);
    return toast;
  }

  function show(error) {
    var now = Date.now();
    if (now - lastShownAt < 3000) return;
    lastShownAt = now;
    var kind = error && error.name;
    var safeKind = /^(TypeError|ReferenceError|SyntaxError|RangeError)$/.test(kind)
      ? kind
      : "Error";
    var element = ensureToast();
    element.textContent = "页面脚本异常（" + safeKind + "），详情请查看开发者工具";
    element.style.display = "block";
    clearTimeout(dismissTimer);
    dismissTimer = setTimeout(function () {
      element.style.display = "none";
    }, 5000);
  }

  window.addEventListener("error", function (event) {
    // Ignore failed image/script resources; they are not executable errors.
    if (!event.error) return;
    show(event.error);
  });
  window.addEventListener("unhandledrejection", function (event) {
    show(event.reason);
  });
})();
