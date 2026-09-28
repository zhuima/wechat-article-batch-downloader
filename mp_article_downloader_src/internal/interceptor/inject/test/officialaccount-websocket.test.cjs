const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

const source = fs.readFileSync(path.join(__dirname, "../src/officialaccount.js"), "utf8");

function articlePage() {
  const sockets = [];
  const timeouts = new Map();
  const intervals = new Map();
  const events = new Map();
  const bootEvents = new Map();
  let nextTimer = 1;
  let panel = null;

  class WebSocketMock {
    constructor(url) {
      this.url = url;
      this.readyState = 0;
      this.sent = [];
      sockets.push(this);
    }
    send(data) { this.sent.push(data); }
    open() {
      this.readyState = 1;
      this.onopen();
    }
    close() {
      if (this.readyState === 3) return;
      this.readyState = 3;
      if (this.onclose) this.onclose();
    }
  }

  function element() {
    return { dataset: {}, style: {}, textContent: "", value: "", disabled: false };
  }
  const location = {
    hostname: "mp.weixin.qq.com",
    pathname: "/s",
    search: "?__biz=Mexample123",
    href: "https://mp.weixin.qq.com/s?__biz=Mexample123",
  };
  const window = {
    location,
    key: "session-key",
    uin: "session-uin",
    addEventListener(name, callback) { events.set(name, callback); },
  };
  const document = {
    title: "Example article",
    cookie: "",
    head: { appendChild() {} },
    body: { appendChild(node) { panel = node; } },
    createElement(tag) {
      if (tag !== "div") return element();
      const node = element();
      const children = new Map();
      node.querySelector = (selector) => {
        if (!children.has(selector)) children.set(selector, element());
        return children.get(selector);
      };
      return node;
    },
    querySelector(selector) {
      if (selector === "#__mp_article_batch_panel__") return panel;
      if (selector === '#__mp_article_batch_panel__ [data-role="connection"]') {
        return panel && panel.querySelector('[data-role="connection"]');
      }
      return null;
    },
  };
  const WXU = {
    config: { officialServerDisabled: false, apiServerProtocol: "http", apiServerHostname: "127.0.0.1", apiServerPort: 2122 },
    Events: { OfficialAccountRefresh: "OfficialAccountRefresh" },
    emit() {},
    request() { return Promise.resolve([null, {}]); },
    observe_node() {}, // The article has no .wx_follow_media anchor.
    onDOMContentLoaded(callback) { bootEvents.set("dom", callback); },
    onWindowLoaded(callback) { bootEvents.set("load", callback); },
    log() {},
  };
  const context = vm.createContext({
    window, location, document, WXU, WebSocket: WebSocketMock,
    insert_channels_style() {}, URLSearchParams, console,
    setTimeout(callback, delay) {
      const id = nextTimer++;
      timeouts.set(id, { callback, delay });
      return id;
    },
    clearTimeout(id) { timeouts.delete(id); },
    setInterval(callback, delay) {
      const id = nextTimer++;
      intervals.set(id, { callback, delay });
      return id;
    },
    clearInterval(id) { intervals.delete(id); },
  });
  vm.runInContext(source, context);
  return {
    sockets, timeouts, intervals, events, bootEvents, context,
    get panel() { return panel; },
    runTimeout(delay) {
      const match = [...timeouts].find(([, timer]) => timer.delay === delay);
      assert.ok(match, `expected a ${delay}ms timeout`);
      timeouts.delete(match[0]);
      match[1].callback();
    },
  };
}

async function flushPromises() {
  await new Promise((resolve) => setImmediate(resolve));
}

test("article fallback connects without the follow-media anchor and reports actual sync", async () => {
  const page = articlePage();
  page.bootEvents.get("dom")();
  page.bootEvents.get("load")();
  page.runTimeout(1500);
  assert.equal(page.sockets.length, 1, "repeated boot hooks must share one socket");
  assert.equal(page.panel.querySelector('[data-role="connection"]').dataset.state, "connecting");
  page.sockets[0].open();
  await flushPromises();
  const indicator = page.panel.querySelector('[data-role="connection"]');
  assert.equal(indicator.dataset.state, "synced");
  assert.match(indicator.textContent, /已同步/);
  assert.equal(page.panel.querySelector('[data-action="scan"]').disabled, false);
  vm.runInContext(source, page.context);
  assert.equal(page.sockets.length, 1, "duplicate injection must not add a socket");
  assert.equal([...page.intervals.values()].filter((timer) => timer.delay === 5000).length, 2,
    "one maintenance timer and one ping timer");
});

test("disconnect retries once with backoff, then stops on page unload", async () => {
  const page = articlePage();
  page.runTimeout(1500);
  page.runTimeout(2000); // Consume the panel maintenance fallback.
  const first = page.sockets[0];
  first.open();
  await flushPromises();
  first.close();
  first.onerror(); // Browsers can deliver error and close for the same failure.
  assert.equal(page.panel.querySelector('[data-role="connection"]').dataset.state, "offline");
  assert.equal([...page.timeouts.values()].filter((timer) => timer.delay === 1000).length, 1);
  assert.equal(page.panel.querySelector('[data-action="scan"]').disabled, false);
  page.runTimeout(1000);
  assert.equal(page.sockets.length, 2);
  page.sockets[1].close();
  assert.equal([...page.timeouts.values()].filter((timer) => timer.delay === 2000).length, 1);
  page.events.get("pagehide")();
  assert.equal([...page.timeouts.values()].filter((timer) => timer.delay === 2000).length, 0);
  assert.equal([...page.intervals.values()].filter((timer) => timer.delay === 5000).length, 1,
    "page unload clears the ping timer but keeps unrelated maintenance bookkeeping");
  page.events.get("pageshow")({ persisted: true });
  assert.equal(page.sockets.length, 3, "restoring a cached article page starts one fresh connection");
});
