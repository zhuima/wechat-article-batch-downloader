const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

const source = fs.readFileSync(path.join(__dirname, "../src/officialaccount.js"), "utf8");

function refreshCallsFor(pathname, key = "session-key") {
  const calls = [];
  const location = {
    pathname,
    hostname: "mp.weixin.qq.com",
    search: "?__biz=Mexample123",
    href: `https://mp.weixin.qq.com${pathname}?__biz=Mexample123`,
  };
  const window = { location, key, uin: "session-uin", addEventListener() {} };
  const WXU = {
    config: { officialServerDisabled: false, apiServerProtocol: "http", apiServerHostname: "127.0.0.1", apiServerPort: 2122 },
    Events: { OfficialAccountRefresh: "OfficialAccountRefresh" },
    emit() {},
    request(options) {
      calls.push(options);
      return Promise.resolve([null, {}]);
    },
    observe_node() {},
    onDOMContentLoaded() {},
    onWindowLoaded() {},
  };
  vm.runInNewContext(source, {
    window,
    location,
    document: { createElement: () => ({}), title: "Example article", cookie: "" },
    WXU,
    insert_channels_style() {},
    setTimeout() {},
    setInterval() {},
    URLSearchParams,
    console,
  });
  return calls.filter((call) => new URL(call.url).pathname === "/api/mp/refresh");
}

test("article routes submit page credentials, including short share paths", () => {
  for (const pathname of ["/s", "/s/", "/s/abc_123-Z"]) {
    const calls = refreshCallsFor(pathname);
    assert.equal(calls.length, 1, pathname);
    assert.equal(calls[0].method, "POST");
    assert.equal(calls[0].body.biz, "Mexample123");
    assert.equal(calls[0].body.key, "session-key");
  }
});

test("article route without a page key does not submit a refresh", () => {
  assert.equal(refreshCallsFor("/s/abc_123-Z", "").length, 0);
});

test("non-article paths do not submit page credentials", () => {
  for (const pathname of ["/", "/s/abc/extra", `/s/${"a".repeat(257)}`]) {
    assert.equal(refreshCallsFor(pathname).length, 0, pathname);
  }
});
