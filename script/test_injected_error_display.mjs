import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const errorSource = readFileSync(
  new URL("../mp_article_downloader_src/internal/interceptor/inject/src/error.js", import.meta.url),
  "utf8",
);
const utilsSource = readFileSync(
  new URL("../mp_article_downloader_src/internal/interceptor/inject/src/utils.js", import.meta.url),
  "utf8",
);

// Test the injected script as it runs on a WeChat article page. No real page
// state or credentials are needed: the URL below is an invented sentinel.
const secretUrl = "https://mp.weixin.qq.com/s?__biz=TEST&mid=123&key=private-key&pass_ticket=private-ticket";
const listeners = new Map();
const elements = [];
const timers = new Map();
let nextTimerId = 0;
let now = 10000;
const document = {
  head: { appendChild(element) { elements.push(element); } },
  body: {
    style: { overflow: "auto" },
    appendChild(element) { elements.push(element); },
  },
  createElement(tagName) {
    return {
      tagName,
      id: "",
      textContent: "",
      innerHTML: "",
      style: {},
      attributes: {},
      setAttribute(name, value) { this.attributes[name] = value; },
    };
  },
};
vm.runInNewContext(errorSource, {
  document,
  Date: { now() { return now; } },
  window: {
    addEventListener(name, callback) { listeners.set(name, callback); },
  },
  setTimeout(callback, delay) {
    const id = ++nextTimerId;
    timers.set(id, { callback, delay });
    return id;
  },
  clearTimeout(id) { timers.delete(id); },
}, { filename: "error.js" });

assert.ok(listeners.has("error"), "page errors should remain observable");
assert.ok(listeners.has("unhandledrejection"), "promise failures should remain observable");
const runtimeError = new TypeError(`Failed to fetch ${secretUrl}`);
runtimeError.stack = `TypeError: Failed to fetch ${secretUrl}\n    at Object.fetch (${secretUrl}:12:3)`;
let prevented = false;
listeners.get("error")({
  error: runtimeError,
  preventDefault() { prevented = true; },
});

const toast = elements.find((element) => element.id === "__mp_debug_error_toast");
assert.ok(toast, "an unobtrusive diagnostic toast should be shown");
assert.equal(toast.style.display, "block");
assert.match(elements.find((element) => element.tagName === "style").textContent, /position:fixed;top:16px;right:16px/);
assert.equal(prevented, false, "do not swallow the browser's normal error reporting");
assert.equal(document.body.style.overflow, "auto", "the article must stay scrollable");
assert.equal(elements.some((element) => element.id === "error-modal"), false);
assert.equal(elements.some((element) => /error-modal|rgba\(0,\s*0,\s*0,\s*0\.5\)/.test(element.textContent)), false);
const rendered = elements.map((element) => `${element.textContent}${element.innerHTML}`).join("\n");
for (const forbidden of [secretUrl, "private-key", "private-ticket", "Failed to fetch", "Object.fetch", "at Object"]) {
  assert.equal(rendered.includes(forbidden), false, `page diagnostic exposed ${forbidden}`);
}
assert.equal(timers.size, 1, "diagnostic should have one dismissal timer");
const dismissal = [...timers.values()][0];
assert.ok(dismissal.delay > 0 && dismissal.delay <= 10000, "toast should dismiss promptly");
dismissal.callback();
assert.equal(toast.style.display, "none", "toast should disappear without a click");

now += 5000;
const unexpectedError = new Error(`Promise failed at ${secretUrl}`);
unexpectedError.name = `RemoteError ${secretUrl}`;
unexpectedError.stack = `RemoteError: ${secretUrl}\n    at Object.fetch (${secretUrl})`;
let rejectionPrevented = false;
listeners.get("unhandledrejection")({
  reason: unexpectedError,
  preventDefault() { rejectionPrevented = true; },
});
assert.equal(toast.style.display, "block", "rejected promises should use the same small toast");
assert.equal(rejectionPrevented, false);
assert.equal(toast.textContent.includes(secretUrl), false, "unexpected error names must be hidden");
assert.equal(toast.textContent.includes("RemoteError"), false);

// Run the actual WXU.error function in isolation. Loading all of utils.js
// would start unrelated Channels features and hide the behavior under test.
const functionStart = utilsSource.indexOf("  function __wx_error(params) {");
const functionEnd = utilsSource.indexOf("  const script_loaded_map", functionStart);
assert.ok(functionStart >= 0 && functionEnd > functionStart, "WXU.error source was not found");
const errorFunction = utilsSource.slice(functionStart, functionEnd);
function runWxError(hostname) {
  const requests = [];
  const tips = [];
  const context = {
    location: { hostname },
    fetch(url, options) {
      requests.push({ url, options });
      return Promise.resolve({ ok: true });
    },
    weui: { topTips(text) { tips.push(text); } },
  };
  const reportError = vm.runInNewContext(`${errorFunction}\n__wx_error`, context, {
    filename: "utils.js",
  });
  reportError({ msg: `Failed to fetch ${secretUrl}` });
  return { requests, tips };
}

const articlePage = runWxError("mp.weixin.qq.com");
assert.equal(articlePage.requests.length, 0, "article pages must not call the Channels error endpoint");
assert.equal(articlePage.tips.length, 1);
assert.equal(articlePage.tips[0].includes(secretUrl), false);
assert.equal(articlePage.tips[0].includes("private-key"), false);

const channelsPage = runWxError("channels.weixin.qq.com");
assert.equal(channelsPage.requests.length, 1, "Channels diagnostics should still work");
assert.equal(channelsPage.requests[0].url, "/__wx_channels_api/error");
assert.equal(channelsPage.requests[0].options.body.includes(secretUrl), false);
assert.equal(channelsPage.requests[0].options.body.includes("private-key"), false);
assert.equal(channelsPage.tips[0].includes(secretUrl), false);

console.log("injected error display: passed");
