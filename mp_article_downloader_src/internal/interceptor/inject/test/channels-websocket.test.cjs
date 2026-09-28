const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

const source = fs.readFileSync(path.join(__dirname, "../src/utils.js"), "utf8");
const clientStart = source.indexOf("function ChannelsWebsocketClient()");
assert.notEqual(clientStart, -1);
const clientSource = source.slice(clientStart);

function startClientOn(hostname) {
  const sockets = [];
  class WebSocketMock {
    constructor(url) {
      this.url = url;
      sockets.push(this);
    }
  }
  vm.runInNewContext(clientSource, {
    location: { hostname },
    WSServerProtocol: "wss",
    FakeLocalAPIServerAddr: "kf.qq.com",
    WebSocket: WebSocketMock,
    WXU: { onInit() {}, error() {} },
  });
  return sockets;
}

test("公众号文章页不建立视频号 WebSocket", () => {
  assert.equal(startClientOn("mp.weixin.qq.com").length, 0);
});

test("视频号页面仍建立视频号 WebSocket", () => {
  const sockets = startClientOn("channels.weixin.qq.com");
  assert.equal(sockets.length, 1);
  assert.equal(sockets[0].url, "wss://kf.qq.com/ws/channels");
});
