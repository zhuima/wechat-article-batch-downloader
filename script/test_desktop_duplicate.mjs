import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

const html = readFileSync(new URL("../mp_article_downloader_src/internal/api/ui/desktop.html", import.meta.url), "utf8");
const script = html.match(/<script>([\s\S]*?)<\/script>/)?.[1];
assert.ok(script, "desktop page has an inline script");

class Element {
  constructor(id = "") {
    this.id = id;
    this.value = id === "taskFilter" ? "all" : "";
    this.hidden = false;
    this.disabled = false;
    this.textContent = "";
    this.children = [];
    this.style = {};
    this.listeners = new Map();
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
  click() { return this.listeners.get("click")?.({ preventDefault() {} }); }
  submit() { return this.listeners.get("submit")?.({ preventDefault() {} }); }
}

async function scenario(createResponse) {
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new Element(id));
    return elements.get(id);
  };
  const calls = [];
  const fetch = async (path, options) => {
    calls.push({ path, options });
    let envelope;
    if (path === "/api/desktop/info") envelope = { code: 0, data: { download_dir: "C:\\Downloads" } };
    else if (path.startsWith("/api/mp/list?")) envelope = { code: 0, data: { list: [], total: 0 } };
    else if (path.startsWith("/api/desktop/local-status?")) envelope = { code: 0, data: { total: 0, downloaded: 0, article_ids: [], download_dir: "" } };
    else if (path === "/api/desktop/local-library?biz=test-biz") envelope = { code: 0, data: { articles: [], total: 0 } };
    else if (path === "/api/mp/import_url") envelope = { code: 0, data: { biz: "test-biz", nickname: "测试公众号", title: "测试文章", history_available: false } };
    else if (path === "/api/task/create2") envelope = createResponse;
    else if (path === "/api/desktop/download-summary") envelope = { code: 0, data: [] };
    else if (path.startsWith("/api/task/list?")) envelope = { code: 0, data: { list: [], total: 0 } };
    else throw new Error(`unexpected request: ${path}`);
    return { ok: true, json: async () => envelope };
  };
  runInNewContext(script, {
    document: { getElementById: get, createElement: () => new Element(), createTextNode: (value) => ({ textContent: value }), createDocumentFragment: () => new Element() },
    window: { addEventListener() {}, crypto: null },
    navigator: {}, localStorage: { getItem: () => null, setItem() {} },
    fetch, URL, AbortController, setTimeout, clearTimeout, setInterval: () => 0,
    console
  }, { filename: "desktop.html" });
  await new Promise((resolve) => setImmediate(resolve));
  get("importUrl").value = "https://mp.weixin.qq.com/s?__biz=test-biz&mid=123&idx=1";
  await get("importForm").submit();
  await get("welcomeDownloadImported").click();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(calls.filter(({ path }) => path === "/api/task/create2").length, 1);
  return { get };
}

async function identityScenario({ localArticles = [], afterDuplicateArticles = localArticles, createResponse = { code: 0, data: { id: "task-1" } } }) {
  const sourceId = "source-for-imported-article";
  const articleURL = "https://mp.weixin.qq.com/s?__biz=test-biz&mid=123&idx=1";
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new Element(id));
    return elements.get(id);
  };
  const calls = [];
  let libraryArticles = localArticles;
  const fetch = async (path, options) => {
    calls.push({ path, options });
    let envelope;
    if (path === "/api/desktop/info") envelope = { code: 0, data: { download_dir: "C:\\Downloads" } };
    else if (path.startsWith("/api/mp/list?")) envelope = { code: 0, data: { list: [{ biz: "test-biz", nickname: "测试公众号" }], total: 1 } };
    else if (path === "/api/desktop/scan?biz=test-biz") envelope = { code: 0, data: { status: "idle", articles: [], pages: 0 } };
    else if (path === "/api/desktop/local-status?biz=test-biz") envelope = { code: 0, data: { total: 0, downloaded: 0, article_ids: [], download_dir: "" } };
    else if (path === "/api/desktop/local-library?biz=test-biz") envelope = { code: 0, data: { articles: libraryArticles, total: libraryArticles.length } };
    else if (path === "/api/mp/import_url") envelope = { code: 0, data: { biz: "test-biz", nickname: "测试公众号", title: "测试文章", source_id: sourceId, history_available: false } };
    else if (path === "/api/task/create2") {
      envelope = createResponse;
      libraryArticles = afterDuplicateArticles;
    }
    else if (path.startsWith("/api/desktop/article?")) envelope = { code: 0, data: { title: "测试文章", html: "<p>来自本地 Markdown</p>" } };
    else throw new Error(`unexpected request: ${path}`);
    return { ok: true, json: async () => envelope };
  };
  runInNewContext(script, {
    document: { getElementById: get, createElement: () => new Element(), createTextNode: (value) => ({ textContent: value }), createDocumentFragment: () => new Element() },
    window: { addEventListener() {}, crypto: null, location: { origin: "http://127.0.0.1:2132" } },
    navigator: {}, localStorage: { getItem: () => null, setItem() {} },
    fetch, URL, AbortController, setTimeout, clearTimeout, setInterval: () => 0,
    console
  }, { filename: "desktop.html" });
  await new Promise((resolve) => setImmediate(resolve));
  get("importUrl").value = articleURL;
  await get("importForm").submit();
  await new Promise((resolve) => setImmediate(resolve));
  return { get, calls, sourceId };
}

for (const message of ["文章已在下载队列中", "已存在该下载内容", "目标目录已存在该文章文件"]) {
  const { get } = await scenario({ code: 409, msg: message });
  if (message === "文章已在下载队列中") {
    assert.equal(get("notice").classList.contains("error"), false, message);
    assert.equal(get("downloadsView").hidden, false, message);
    assert.match(get("noticeMessage").textContent, /未重复添加.*查看进度/);
  } else {
    assert.equal(get("downloadsView").hidden, true, "an existing local file does not send the user to the queue");
    assert.match(get("noticeMessage").textContent, /已在本机，未重复下载/);
    assert.match(get("noticeMessage").textContent, /刷新本地文档|打开公众号文件夹/);
  }
}

{
  const { get } = await scenario({ code: 0, data: { id: "task-1" } });
  assert.equal(get("notice").classList.contains("error"), false);
  assert.equal(get("downloadsView").hidden, false);
  assert.match(get("noticeMessage").textContent, /已加入下载中心/);
}

for (const response of [{ code: 500, msg: "磁盘写入失败" }, { code: 409, msg: "其他冲突" }]) {
  const { get } = await scenario(response);
  assert.equal(get("notice").classList.contains("error"), true, response.msg);
  assert.equal(get("downloadsView").hidden, true, response.msg);
  assert.match(get("noticeMessage").textContent, /添加单篇文章任务失败/);
}

{
  const article = { id: "local-same-source", title: "代码 12-1　正文首标题", source_id: "source-for-imported-article", published: 0 };
  const { get, calls } = await identityScenario({ localArticles: [article] });
  assert.equal(get("downloadImported").textContent, "阅读这篇文章", "a matching source opens locally even when Markdown's first heading differs");
  assert.equal(get("welcomeDownloadImported").textContent, "阅读这篇文章");
  await get("downloadImported").click();
  assert.equal(calls.filter(({ path }) => path === "/api/task/create2").length, 0, "reading never starts another download");
  assert.equal(calls.filter(({ path }) => path === "/api/desktop/article?biz=test-biz&id=local-same-source").length, 1);
  assert.equal(get("readerView").hidden, false);
  assert.match(get("readerFrame").srcdoc, /来自本地 Markdown/);
}

{
  const article = { id: "local-different-source", title: "测试文章", source_id: "another-article-source", published: 0 };
  const { get, calls } = await identityScenario({ localArticles: [article] });
  assert.equal(get("downloadImported").textContent, "下载这篇文章", "the same title cannot identify a different article");
  assert.equal(get("welcomeDownloadImported").textContent, "下载这篇文章");
  assert.equal(calls.filter(({ path }) => path.startsWith("/api/desktop/article?")).length, 0);
}

{
  const article = { id: "local-after-409", title: "代码 12-1　正文首标题", source_id: "source-for-imported-article", published: 0 };
  const { get, calls } = await identityScenario({
    localArticles: [], afterDuplicateArticles: [article], createResponse: { code: 409, msg: "已存在该下载内容" }
  });
  assert.equal(get("downloadImported").textContent, "下载这篇文章", "before refresh the local file is not known");
  await get("downloadImported").click();
  assert.equal(calls.filter(({ path }) => path === "/api/task/create2").length, 1);
  assert.equal(get("downloadImported").textContent, "阅读这篇文章", "409 refresh discovers the local file");
  assert.equal(get("welcomeDownloadImported").textContent, "阅读这篇文章");
  assert.match(get("noticeMessage").textContent, /阅读这篇文章/);
  await get("downloadImported").click();
  assert.equal(calls.filter(({ path }) => path === "/api/task/create2").length, 1, "reading after refresh never retries the download");
  assert.equal(calls.filter(({ path }) => path === "/api/desktop/article?biz=test-biz&id=local-after-409").length, 1);
}

console.log("desktop single-article duplicate, source identity, and failure states pass");
