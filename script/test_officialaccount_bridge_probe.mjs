import assert from "node:assert/strict";
import fs from "node:fs";
import vm from "node:vm";

const requests = [];
const timers = [];
const bridgeCalls = [];
const noop = () => {};
const context = {
  URL,
  URLSearchParams,
  Promise,
  console,
  setTimeout(callback, delay) {
    timers.push({ callback, delay });
    return timers.length;
  },
  clearTimeout: noop,
  setInterval: noop,
  addEventListener: noop,
  removeEventListener: noop,
  document: {
    title: "测试公众号",
    cookie: "private-cookie",
    head: { appendChild: noop },
    body: { appendChild: noop },
    createElement() { return { style: {}, appendChild: noop }; },
    querySelector(selector) {
      if (selector === "#__mp_article_batch_panel__") return {};
      return null;
    },
    querySelectorAll() { return []; },
    getElementById() { return null; },
  },
  location: {
    hostname: "mp.weixin.qq.com",
    pathname: "/s",
    href: "https://mp.weixin.qq.com/s?__biz=MzTest&mid=1&idx=1&sn=abc",
    search: "?__biz=MzTest&mid=1&idx=1&sn=abc",
  },
  insert_channels_style: noop,
  RSSIcon: "",
  DownloadIcon8: "",
  APIServerProtocol: "http",
  FakeAPIServerAddr: "127.0.0.1:2122",
  WSServerProtocol: "ws",
  WXU: {
    config: {
      officialServerDisabled: false,
      officialServerRefreshToken: "",
      bridgeProbeEnabled: true,
      bridgeProbeTargetBiz: "MzTest",
    },
    Events: { OfficialAccountRefresh: "OfficialAccountRefresh" },
    emit: noop,
    log: noop,
    error: noop,
    observe_node: noop,
    onDOMContentLoaded: noop,
    onWindowLoaded: noop,
    async request(options) {
      requests.push(options);
      return [null, { ok: true }];
    },
  },
  WeixinJSBridge: {
    invoke(method, args, callback) {
      bridgeCalls.push({ method, args });
      callback({
        err_msg: "H5ExtTransfer:ok",
        jsapi_resp: {
          resp_json: JSON.stringify({
            MsgList: {
              Msg: [{ AppMsg: { DetailInfo: [{ Title: "private-title", ContentUrl: "private-url" }] } }],
              PagingInfo: { Offset: "private-cursor", IsEnd: 0 },
            },
          }),
        },
      });
    },
  },
  cgiDataNew: { nick_name: "测试公众号", user_name: "gh_abcdef123456" },
  nickname: "测试公众号",
  uin: "private-uin",
  key: "private-key",
};
context.window = context;
const source = fs.readFileSync(
  new URL("../mp_article_downloader_src/internal/interceptor/inject/src/officialaccount.js", import.meta.url),
  "utf8",
);
vm.runInNewContext(source, context, { filename: "officialaccount.js" });
const articleTimer = timers.find((timer) => timer.delay === 1500);
assert.ok(articleTimer, "article registration timer is missing");
articleTimer.callback();
articleTimer.callback();
await new Promise((resolve) => setImmediate(resolve));

assert.equal(bridgeCalls.length, 1, "probe must invoke the native bridge only once");
assert.equal(bridgeCalls[0].method, "H5ExtTransfer");
assert.equal(bridgeCalls[0].args.cgi_cmdid, 5814);
assert.equal(bridgeCalls[0].args.scope, "subscriptions");
assert.equal(bridgeCalls[0].args.cgi_type, 0);
const nativeRequest = JSON.parse(bridgeCalls[0].args.req_json);
assert.equal(nativeRequest.BizUserName, "gh_abcdef123456");
assert.equal(nativeRequest.PageSize, 10);
assert.equal("Offset" in nativeRequest, false, "native first page omits Offset");

const reports = requests.filter((request) => request.url.endsWith("/api/desktop/bridge-probe"));
assert.equal(reports.length, 1);
assert.equal(reports[0].body.status, "ok", "missing outer ret must be tolerated");
assert.equal(reports[0].body.article_count, 1);
assert.equal(reports[0].body.has_offset, true);
assert.equal(reports[0].body.is_end, false);
const reportText = JSON.stringify(reports[0].body);
for (const secret of ["private-title", "private-url", "private-cursor", "private-key", "private-uin", "private-cookie"]) {
  assert.equal(reportText.includes(secret), false, `diagnostic disclosed ${secret}`);
}

// The article hook may run before WeChat fills cgiDataNew.user_name.
context.__mp_article_bridge_probe_started = false;
context.cgiDataNew = { nick_name: "测试公众号" };
const beforeRetry = timers.length;
articleTimer.callback();
await new Promise((resolve) => setImmediate(resolve));
assert.equal(bridgeCalls.length, 1, "probe ran before the profile username arrived");
const usernameRetry = timers.slice(beforeRetry).find((timer) => timer.delay === 300);
assert.ok(usernameRetry, "probe did not wait for delayed page data");
context.cgiDataNew.user_name = "gh_abcdef123456";
usernameRetry.callback();
await new Promise((resolve) => setImmediate(resolve));
assert.equal(bridgeCalls.length, 2, "probe did not resume after the username arrived");
const delayedReports = requests.filter((request) => request.url.endsWith("/api/desktop/bridge-probe"));
assert.equal(delayedReports.length, 2);
assert.equal(delayedReports[1].body.status, "ok");
console.log("officialaccount bridge probe: ok");
