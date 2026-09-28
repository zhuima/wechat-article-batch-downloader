import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

const html = readFileSync(new URL("../mp_article_downloader_src/internal/api/ui/desktop.html", import.meta.url), "utf8");
const script = html.match(/<script>([\s\S]*?)<\/script>/)?.[1];
assert.ok(script, "desktop page has an inline script");
assert.match(html, /批次概览/, "download center identifies the batch summary separately from article tasks");

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
}

const allNodes = (root) => [root, ...(root.children || []).flatMap(allNodes)];
const nodeText = (root) => String(root.textContent || "") + (root.children || []).map(nodeText).join("");
const hasClass = (node, name) => String(node.className || "").split(/\s+/).includes(name);
const childClass = (root, name) => allNodes(root).find((node) => hasClass(node, name));
const tick = () => new Promise((resolve) => setImmediate(resolve));

const sourceURL = "https://mp.weixin.qq.com/s?__biz=scan-biz&mid=1&idx=1&sn=abc";
const downloadRoot = "C:\\Downloads";
const summaries = [
  {
    path: downloadRoot + "\\每天晒白牙", account_name: "每天晒白牙", article_title: "真正的单篇文章",
    total: 1, completed: 1, started_at: 1000, finished_at: 2000
  },
  {
    path: downloadRoot + "\\批量号", account_name: "批量号", article_title: "其中一篇文章",
    total: 185, completed: 185, started_at: 3000, finished_at: 4000
  },
  {}
];
const tasks = [
  {
    id: "labeled", status: "done", files_exist: true,
    meta: {
      opts: { name: "错误旧文件名-0123456789abcdef", path: downloadRoot + "\\每天晒白牙" },
      req: { url: "officialaccount://https://mp.weixin.qq.com/s?mid=2", labels: { article_title: "真正的文章标题", account_name: "每天晒白牙" } }
    }
  },
  {
    id: "legacy", status: "done", files_exist: true,
    meta: {
      opts: { name: "旧文章标题-0123456789abcdef", path: downloadRoot + "\\旧号" },
      req: { url: "officialaccount://https://mp.weixin.qq.com/s?mid=3", labels: {} }
    }
  },
  {
    id: "scanned", status: "done", files_exist: true,
    meta: {
      opts: { name: "散列文件名-abcdefabcdefabcd", path: downloadRoot + "\\扫描号" },
      req: { url: "officialaccount://" + sourceURL, labels: { account_name: "扫描号" } }
    }
  },
  { id: "missing", status: "error", meta: {} }
];

const elements = new Map();
const get = (id) => {
  if (!elements.has(id)) elements.set(id, new Element(id));
  return elements.get(id);
};
const calls = [];
const fetch = async (path) => {
  calls.push(path);
  let data;
  if (path === "/api/desktop/info") data = { download_dir: downloadRoot };
  else if (path.startsWith("/api/mp/list?")) {
    const list = [
      { biz: "scan-biz", nickname: "扫描号", is_effective: true },
      { biz: "other-biz", nickname: "每天晒白牙", is_effective: true }
    ];
    data = { list, total: list.length };
  }
  else if (path === "/api/desktop/download-summary") data = summaries;
  else if (path.startsWith("/api/task/list?")) data = { list: tasks, total: tasks.length };
  else if (path === "/api/desktop/scan?biz=scan-biz") data = {
    status: "complete", articles: [{ id: "scan-1", title: "扫描缓存中的文章标题", url: sourceURL }],
    pages: 1, updated: 1, options: { biz: "scan-biz", mode: "all" }
  };
  else if (path.startsWith("/api/desktop/scan?biz=")) data = {
    status: "complete", articles: [], pages: 0, updated: 1, options: { biz: "other-biz", mode: "all" }
  };
  else throw new Error("unexpected request: " + path);
  return { ok: true, json: async () => ({ code: 0, data }) };
};

runInNewContext(script, {
  document: {
    getElementById: get,
    createElement: (tag) => new Element("", tag),
    createTextNode: (value) => ({ textContent: value, children: [] }),
    createDocumentFragment: () => new Element()
  },
  window: { addEventListener() {}, crypto: null, location: { origin: "http://127.0.0.1:2132" } },
  navigator: { clipboard: { writeText: async () => {} } },
  localStorage: { getItem: () => null, setItem() {} },
  fetch, URL, AbortController, setTimeout: () => 0, clearTimeout() {}, setInterval: () => 0, console
}, { filename: "desktop.html" });

await tick();
await get("downloadsNav").click();
for (let attempt = 0; attempt < 30 && !nodeText(get("taskList")).includes("扫描缓存中的文章标题"); attempt++) await tick();

const taskRows = allNodes(get("taskList")).filter((node) => hasClass(node, "task-row"));
assert.equal(taskRows.length, tasks.length, "all task rows render, including incomplete metadata");
assert.equal(nodeText(childClass(taskRows[0], "task-title")), "真正的文章标题", "explicit article title takes priority over a filename");
assert.equal(nodeText(childClass(taskRows[0], "task-account")), "公众号：每天晒白牙", "task shows its account next to the title");
assert.equal(nodeText(childClass(taskRows[1], "task-title")), "旧文章标题", "legacy hexadecimal ID suffix is hidden");
assert.equal(nodeText(childClass(taskRows[1], "task-account")), "公众号：旧号", "legacy task falls back to its account folder");
assert.ok(calls.includes("/api/desktop/scan?biz=scan-biz"), "download center loads scan metadata for tasks without a saved title");
assert.equal(nodeText(childClass(taskRows[2], "task-title")), "扫描缓存中的文章标题", "matching source URL recovers a title from the saved scan");
assert.equal(nodeText(childClass(taskRows[2], "task-account")), "公众号：扫描号");
assert.equal(nodeText(childClass(taskRows[3], "task-title")), "未命名文章", "missing optional task metadata has a readable fallback");
assert.equal(nodeText(childClass(taskRows[3], "task-account")), "公众号：未知公众号");

const summaryCards = allNodes(get("summaryList")).filter((node) => hasClass(node, "summary-card"));
assert.equal(summaryCards.length, summaries.length);
assert.equal(nodeText(childClass(summaryCards[0], "summary-title")), "真正的单篇文章", "single-article summary uses its article title");
assert.equal(nodeText(childClass(summaryCards[0], "summary-account")), "公众号：每天晒白牙");
assert.match(nodeText(childClass(summaryCards[1], "summary-title")), /批量号.*185 篇/, "multi-article summary describes the account and batch size");
assert.equal(nodeText(childClass(summaryCards[1], "summary-account")), "公众号：批量号");
assert.ok(nodeText(childClass(summaryCards[2], "summary-title")), "a summary missing optional fields still has a readable title");

console.log("desktop download title UI checks passed");
