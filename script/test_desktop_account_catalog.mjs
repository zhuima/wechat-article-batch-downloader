import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

const html = readFileSync(new URL("../mp_article_downloader_src/internal/api/ui/desktop.html", import.meta.url), "utf8");
const script = html.match(/<script>([\s\S]*?)<\/script>/)?.[1];
assert.ok(script, "desktop page has an inline script");

class Element {
  constructor(id = "", tagName = "div") {
    this.id = id;
    this.tagName = tagName.toUpperCase();
    this.value = id === "taskFilter" ? "all" : "";
    this.textContent = "";
    this.children = [];
    this.listeners = new Map();
    this.style = {};
    this.hidden = false;
    this.disabled = false;
    this.scrollTop = 0;
    const classes = new Set();
    this.classList = {
      toggle(name, force) { if (force === undefined ? !classes.has(name) : force) classes.add(name); else classes.delete(name); },
      contains(name) { return classes.has(name); }
    };
  }
  addEventListener(name, listener) { this.listeners.set(name, listener); }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = children; }
  setAttribute(name, value) { this[name] = value; }
  removeAttribute(name) { delete this[name]; }
  focus() {}
  click() { return this.listeners.get("click")?.({ preventDefault() {} }); }
  input() { return this.listeners.get("input")?.({ preventDefault() {} }); }
}

const allNodes = (root) => [root, ...(root.children || []).flatMap(allNodes)];
const nodeText = (root) => String(root.textContent || "") + (root.children || []).map(nodeText).join("");
const tick = () => new Promise((resolve) => setImmediate(resolve));

const accounts = Array.from({ length: 435 }, (_, index) => ({
  biz: `biz-${String(index).padStart(3, "0")}`,
  nickname: [7, 206, 432].includes(index) ? `目标公众号 ${index}` : `示例公众号 ${String(index).padStart(3, "0")}`,
  is_effective: index % 2 === 0,
  history_credentials_present: index % 2 === 0 || index === 1
}));

const elements = new Map();
const get = (id) => {
  if (!elements.has(id)) elements.set(id, new Element(id));
  return elements.get(id);
};
const calls = [];
const storage = new Map([["mpArchiveSidebarCollapsed", "1"]]);
const fetch = async (path) => {
  calls.push(path);
  let envelope;
  if (path === "/api/desktop/info") envelope = { code: 0, data: { download_dir: "C:\\Downloads" } };
  else if (path.startsWith("/api/mp/list?")) {
    const query = new URL(path, "http://127.0.0.1").searchParams;
    const page = Number(query.get("page"));
    const size = Number(query.get("page_size"));
    assert.equal(size, 200, "all accounts are fetched in bounded backend pages");
    assert.ok(page >= 1 && page <= 3, "fetch only the pages required by the total");
    envelope = { code: 0, data: { list: accounts.slice((page - 1) * size, page * size), total: accounts.length } };
  }
  else if (path === "/api/desktop/scan-summaries") envelope = { code: 0, data: {
    "biz-001": { status: "complete", pages: 4, article_count: 74 },
    "biz-002": { status: "paused", pages: 2, article_count: 21 },
    "biz-003": { status: "paused", error_code: "candidate_unverified", pages: 0, article_count: 0 }
  } };
  else if (path.startsWith("/api/desktop/scan?biz=")) {
    const biz = new URL(path, "http://127.0.0.1").searchParams.get("biz");
    envelope = { code: 0, data: biz === "biz-003"
      ? { status: "paused", error_code: "candidate_unverified", message: "作者列表未确认", pages: 0, updated: 1, articles: [], options: { biz, mode: "all" } }
      : { status: "idle", message: "尚未读取", pages: 0, updated: 0, articles: [], options: null } };
  }
  else if (path.startsWith("/api/desktop/local-status?biz=")) envelope = { code: 0, data: { total: 0, downloaded: 0, article_ids: [], download_dir: "" } };
  else if (path.startsWith("/api/desktop/local-library?biz=")) {
    const biz = new URL(path, "http://127.0.0.1").searchParams.get("biz");
    envelope = { code: 0, data: { articles: biz === "biz-003" ? [
      { id: "local-one", title: "本地文章一", published: 1 },
      { id: "local-two", title: "本地文章二", published: 2 }
    ] : [], total: biz === "biz-003" ? 2 : 0 } };
  }
  else if (path === "/api/desktop/download-summary") envelope = { code: 0, data: [] };
  else if (path.startsWith("/api/task/list?")) envelope = { code: 0, data: { list: [], total: 0 } };
  else throw new Error(`unexpected request: ${path}`);
  return { ok: true, json: async () => envelope };
};

runInNewContext(script, {
  document: { getElementById: get, createElement: (tag) => new Element("", tag), createTextNode: (value) => ({ textContent: value, children: [] }), createDocumentFragment: () => new Element() },
  window: { addEventListener() {}, crypto: null, location: { origin: "http://127.0.0.1:2132" } },
  navigator: {}, localStorage: { getItem: (key) => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value) },
  fetch, URL, AbortController, setTimeout, clearTimeout, setInterval: () => 0,
  console
}, { filename: "desktop.html" });

for (let attempt = 0; attempt < 20 && !nodeText(get("sidebarAccountCount")).includes("435"); attempt++) await tick();
assert.match(nodeText(get("sidebarAccountCount")), /435/, "all backend pages are loaded");
assert.equal(get("appShell").classList.contains("sidebar-collapsed"), true, "saved sidebar state is restored");
assert.equal(get("sidebarToggle")["aria-expanded"], "false", "collapsed state is announced to assistive technology");
await get("sidebarToggle").click();
assert.equal(get("appShell").classList.contains("sidebar-collapsed"), false, "sidebar can expand");
assert.equal(storage.get("mpArchiveSidebarCollapsed"), "0", "expanded preference is saved");
await get("sidebarToggle").click();
assert.equal(get("appShell").classList.contains("sidebar-collapsed"), true, "sidebar can collapse again");
assert.deepEqual(calls.filter((path) => path.startsWith("/api/mp/list?")), [
  "/api/mp/list?page=1&page_size=200",
  "/api/mp/list?page=2&page_size=200",
  "/api/mp/list?page=3&page_size=200"
]);

await get("accountsNav").click();
assert.equal(get("accountsView").hidden, false, "dedicated catalogue view opens from the sidebar");
assert.equal(get("welcomeView").hidden, true);
assert.match(nodeText(get("catalogCount")), /435/, "the result count reflects all accounts");

const cards = () => allNodes(get("catalogList")).filter((item) => item.tagName === "BUTTON" && String(item.className || "").includes("catalog-card"));
assert.equal(cards().length, 24, "the catalogue keeps the first page manageable");
assert.equal(get("catalogPager").hidden, false);
assert.match(nodeText(get("catalogPageText")), /1.*19/, "435 accounts occupy 19 pages at 24 per page");
const completedCard = cards().find((card) => card.catalogBiz === "biz-001");
const pausedCard = cards().find((card) => card.catalogBiz === "biz-002");
const candidateCard = cards().find((card) => card.catalogBiz === "biz-003");
assert.match(nodeText(completedCard), /文章列表已保存 · 74 篇/, "saved history count takes precedence over stale session status");
assert.match(nodeText(completedCard), /当前会话待验证/, "saved history does not claim the current credentials work");
assert.doesNotMatch(nodeText(completedCard), /会话参数较旧/);
assert.match(nodeText(pausedCard), /读取未完成 · 已保存 21 篇/, "partial scan keeps its progress visible");
assert.match(nodeText(candidateCard), /作者文章列表未确认.*可打开详情查看原因/, "zero-article candidate failure overrides account freshness");
assert.doesNotMatch(nodeText(candidateCard), /会话参数较旧/);
assert.equal(calls.filter((path) => path === "/api/desktop/scan-summaries").length, 1, "hundreds of cards use one local summary request");
await candidateCard.click();
for (let attempt = 0; attempt < 20 && !nodeText(get("accountDetail")).includes("另有 2 篇本地文档"); attempt++) await tick();
assert.match(nodeText(get("accountDetail")), /作者文章列表未确认.*另有 2 篇本地文档/, "detail prioritizes the scan error and retains local document count");
assert.doesNotMatch(nodeText(get("accountDetail")), /会话参数较旧/);
await get("accountBack").click();
const firstPage = cards().map(nodeText);
await get("catalogNext").click();
assert.equal(cards().length, 24, "the next page remains bounded");
assert.ok(cards().every((card) => !firstPage.includes(nodeText(card))), "the next page contains different accounts");
assert.match(nodeText(get("catalogPageText")), /2.*19/);
await get("catalogPrev").click();
assert.deepEqual(cards().map(nodeText), firstPage, "previous page restores the same accounts");

get("catalogSearch").value = "目标公众号";
await get("catalogSearch").input();
assert.match(nodeText(get("catalogCount")), /3/, "search filters the complete 435-account collection");
assert.equal(cards().length, 3);
assert.equal(get("catalogPager").hidden, true, "one-page results do not show pagination");
assert.ok(cards().every((card) => nodeText(card).includes("目标公众号")));
const filteredCardText = nodeText(cards()[0]);
get("mainPane").scrollTop = 175;
await cards()[0].click();
assert.ok(filteredCardText.includes(nodeText(get("accountTitle"))));
await get("accountBack").click();
assert.equal(get("catalogSearch").value, "目标公众号", "returning from detail preserves the search query");
assert.equal(cards().length, 3, "returning from detail preserves filtered results");
assert.equal(get("mainPane").scrollTop, 175, "returning from detail restores the catalogue scroll position");
get("catalogSearch").value = "不存在的公众号";
await get("catalogSearch").input();
assert.equal(cards().length, 0);
assert.match(nodeText(get("catalogList")), /没有匹配|未找到|无结果/, "empty search explains that there is no match");

get("catalogSearch").value = "";
await get("catalogSearch").input();
for (let page = 1; page < 19; page++) await get("catalogNext").click();
assert.equal(cards().length, 3, "the final page contains only the remaining accounts");
assert.equal(get("catalogNext").disabled, true);
assert.match(nodeText(get("catalogPageText")), /19.*19/);

await get("catalogPrev").click();
const selectedCardText = nodeText(cards()[0]);
get("mainPane").scrollTop = 320;
await cards()[0].click();
await tick();
assert.equal(get("accountView").hidden, false, "a card opens the account detail");
assert.equal(get("accountsView").hidden, true);
assert.ok(selectedCardText.includes(nodeText(get("accountTitle"))), "detail heading names the selected account");
assert.equal(get("accountBack").hidden, false, "detail has a visible return action");
await get("accountBack").click();
assert.equal(get("accountsView").hidden, false, "return action restores the account catalogue");
assert.match(nodeText(get("catalogPageText")), /18.*19/, "returning preserves the catalogue page");
assert.equal(get("catalogSearch").value, "", "returning preserves the search query");

console.log("desktop account catalogue handles 435 accounts, search, pagination, detail, and return");
