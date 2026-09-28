import assert from "node:assert/strict";
import { createHash } from "node:crypto";
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
    this.srcdoc = "";
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
const rows = (get) => allNodes(get("taskList")).filter((node) => hasClass(node, "task-row"));
const taskTitle = (row) => allNodes(row).find((node) => hasClass(node, "task-title"));
const tick = () => new Promise((resolve) => setImmediate(resolve));
async function waitFor(predicate, label) {
  for (let attempt = 0; attempt < 50; attempt++) {
    if (predicate()) return;
    await tick();
  }
  assert.fail("timed out waiting for " + label);
}

const biz = "test-biz";
const title = "同名文章";
const accountName = "测试公众号";
const accountDir = "C:\\Downloads\\测试公众号";
const sourceURL = (mid) => `https://mp.weixin.qq.com/s?__biz=${biz}&mid=${mid}&idx=1&sn=article-${mid}`;
const sourceID = (mid) => createHash("sha256")
  .update(`https://mp.weixin.qq.com/s?__biz=${biz}&idx=1&mid=${mid}&sn=article-${mid}`)
  .digest("hex").slice(0, 16);
const local = (mid) => ({ id: `local_${String(mid).padStart(32, "0")}`, title, source_id: sourceID(mid), published: 1700000000 });
const localArticles = [local(101), local(102)];
const task = (id, mid, status, filesExist) => ({
  id,
  status,
  files_exist: filesExist,
  ...(status === "done" && filesExist && mid !== 103 ? { local_article: { biz, id: local(mid).id, title, published: 1700000000 } } : {}),
  meta: {
    opts: { name: `${title}-${id}`, path: accountDir },
    req: {
      url: `officialaccount://${sourceURL(mid)}`,
      labels: { article_title: title, account_name: accountName, account_biz: biz }
    }
  }
});
const tasks = [
  task("saved-101", 101, "done", true),
  task("saved-102", 102, "done", true),
  task("unmatched-103", 103, "done", true),
  task("still-running", 101, "running", false),
  task("download-failed", 101, "error", false),
  task("missing-format", 101, "done", false)
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
  if (path === "/api/desktop/info") data = { download_dir: "C:\\Downloads" };
  else if (path.startsWith("/api/mp/list?")) data = { list: [{ biz, nickname: accountName, is_effective: true }], total: 1 };
  else if (path === `/api/desktop/scan?biz=${biz}`) data = {
    status: "complete", pages: 1, updated: 1, options: { biz, mode: "all" },
    articles: [101, 102, 103].map((mid) => ({ id: sourceID(mid), title, url: sourceURL(mid), published: 1700000000 }))
  };
  else if (path === `/api/desktop/local-status?biz=${biz}`) data = {
    total: 3, downloaded: 2, article_ids: [sourceID(101), sourceID(102)], download_dir: accountDir
  };
  else if (path === `/api/desktop/local-library?biz=${biz}`) data = { articles: localArticles, total: localArticles.length };
  else if (path.startsWith("/api/desktop/article?")) {
    const parsed = new URL(path, "http://127.0.0.1");
    const id = parsed.searchParams.get("id");
    assert.ok(localArticles.some((article) => article.id === id), "reader requests an actual local-library ID");
    data = { id, title, html: `<p>本地 Markdown 正文 ${id}</p>`, published: 1700000000 };
  }
  else if (path === "/api/desktop/download-summary") data = [];
  else if (path.startsWith("/api/task/list?")) {
    const status = new URL(path, "http://127.0.0.1").searchParams.get("status");
    const list = status === "all" ? tasks : tasks.filter((item) => item.status === status);
    data = { list, total: list.length };
  }
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
  window: { addEventListener() {}, crypto: null, innerWidth: 1200, location: { origin: "http://127.0.0.1:2132" } },
  navigator: { clipboard: { writeText: async () => {} } },
  localStorage: { getItem: () => null, setItem() {} },
  fetch, URL, AbortController, setTimeout: () => 0, clearTimeout() {}, setInterval: () => 0, console
}, { filename: "desktop.html" });

await tick();
await get("downloadsNav").click();
await waitFor(() => rows(get).length === tasks.length, "download task rows");
await waitFor(() => rows(get).slice(0, 2).every((row) => taskTitle(row)?.tagName === "BUTTON"), "local read links");

const rendered = rows(get);
assert.equal(rendered.length, tasks.length);
assert.equal(taskTitle(rendered[0]).tagName, "BUTTON", "completed article 101 has a clickable title");
assert.equal(taskTitle(rendered[1]).tagName, "BUTTON", "a second same-title article retains its own read link");
assert.equal(calls.filter((path) => path.startsWith("/api/desktop/article?")).length, 0, "rendering task rows does not open a file");
for (const index of [2, 3, 4, 5]) {
  await taskTitle(rows(get)[index]).click();
  await tick();
  assert.equal(get("readerView").hidden, true, `task ${tasks[index].id} cannot open the reader`);
  if (get("downloadsView").hidden) {
    await get("downloadsNav").click();
    await waitFor(() => rows(get).length === tasks.length, "download tasks after unverified task");
  }
}
assert.equal(calls.filter((path) => path.startsWith("/api/desktop/article?")).length, 0, "unverified tasks cannot request an unrelated local file");

get("taskFilter").value = "done";
await get("taskFilter").listeners.get("change")();
await waitFor(() => rows(get).length === 4, "completed-task filter");
get("mainPane").scrollTop = 540;
await taskTitle(rows(get)[0]).click();
await waitFor(() => get("readerFrame").srcdoc.includes(localArticles[0].id), "article 101 Markdown reader");
assert.ok(calls.includes(`/api/desktop/article?biz=${biz}&id=${localArticles[0].id}`), "first task reads its verified local document");
assert.equal(get("readerBack").textContent, "← 返回下载中心", "reader back action names its origin");
await get("readerBack").click();
assert.equal(get("downloadsView").hidden, false, "back returns directly to download center");
assert.equal(get("readerView").hidden, true, "back closes local reader");
assert.equal(get("taskFilter").value, "done", "back preserves the download status filter");
assert.equal(get("mainPane").scrollTop, 540, "back restores the task list scroll position");
await waitFor(() => rows(get).length === 4, "filtered download tasks after returning from reader");
await waitFor(() => taskTitle(rows(get)[1])?.tagName === "BUTTON", "second article read link");
await taskTitle(rows(get)[1]).click();
await waitFor(() => get("readerFrame").srcdoc.includes(localArticles[1].id), "article 102 Markdown reader");
assert.ok(calls.includes(`/api/desktop/article?biz=${biz}&id=${localArticles[1].id}`), "same-title article opens its own local document");
assert.equal(calls.filter((path) => path.startsWith("/api/desktop/article?")).length, 2, "no unrelated local file was opened");

console.log("desktop download task local reader flow passes");
