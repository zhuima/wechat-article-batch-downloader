import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

const html = readFileSync(new URL("../mp_article_downloader_src/internal/api/ui/desktop.html", import.meta.url), "utf8");
const script = html.match(/<script>([\s\S]*?)<\/script>/)?.[1];
assert.ok(script, "desktop page has an inline script");
assert.match(html.match(/<div class="reader-toolbar">([\s\S]*?)<\/div>/)?.[1] || "", /id="readerCopyLink"/, "copying the source link belongs in the reader's top toolbar");

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
  submit() { return this.listeners.get("submit")?.({ preventDefault() {} }); }
}

const allNodes = (root) => [root, ...root.children.flatMap(allNodes)];
const nodeText = (root) => String(root.textContent || "") + root.children.map(nodeText).join("");
const tick = () => new Promise((resolve) => setImmediate(resolve));
const catalogCard = (get, nickname) => allNodes(get("catalogList")).find((element) => String(element.className || "").includes("catalog-card") && nodeText(element).includes(nickname));
const firstCatalogCard = (get) => catalogCard(get, "测试公众号");

async function scenario(articleResponse = { code: 0, data: { id: "article-1", title: "已保存文章", html: "<h2>正文来自本地 Markdown</h2><p>离线可读</p>", published: 1700000000 } }, fixture = {}) {
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new Element(id));
    return elements.get(id);
  };
  const calls = [];
  const requests = [];
  const articles = fixture.articles ?? [
    { id: "article-1", title: "已保存文章", url: "https://mp.weixin.qq.com/s?mid=1", published: 1700000000 },
    { id: "article-2", title: "尚未下载文章", url: "https://mp.weixin.qq.com/s?mid=2", published: 1700000001 }
  ];
  const downloadedIds = fixture.downloadedIds ?? ["article-1"];
  const localLibrary = fixture.localLibrary ?? [];
  let localLibraryRequests = 0;
  const clipboardWrites = [];
  const clipboard = fixture.clipboard ?? { writeText: async (value) => { clipboardWrites.push(value); } };
  const timers = new Map();
  let nextTimerId = 0, now = 0;
  const setTimer = (callback, delay = 0) => {
    const id = ++nextTimerId;
    timers.set(id, { callback, at: now + Number(delay) });
    return id;
  };
  const advance = async (duration) => {
    const end = now + duration;
    while (true) {
      const next = [...timers.entries()].filter(([, timer]) => timer.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
      if (!next) break;
      now = next[1].at;
      timers.delete(next[0]);
      await next[1].callback();
    }
    now = end;
  };
  const fetch = async (path, options = {}) => {
    calls.push(path);
    requests.push({ path, body: options.body });
    let envelope;
    if (path === "/api/desktop/info") envelope = { code: 0, data: { download_dir: "C:\\Downloads" } };
    else if (path.startsWith("/api/mp/list?")) envelope = { code: 0, data: { list: fixture.accounts ?? [{ biz: "test-biz", nickname: "测试公众号", is_effective: true }], total: (fixture.accounts ?? [1]).length } };
    else if (path.startsWith("/api/desktop/scan?biz=")) {
      const biz = decodeURIComponent(path.split("biz=")[1]);
      envelope = await (fixture.scanResponses?.[biz] ?? { code: 0, data: { status: "complete", message: "已读取", pages: 1, updated: 1, articles: biz === "test-biz" ? articles : [], options: { biz, mode: "all" } } });
    }
    else if (path === "/api/desktop/scan/repair") envelope = await fixture.repairResponse;
    else if (path.startsWith("/api/desktop/local-status?biz=")) {
      const biz = decodeURIComponent(path.split("biz=")[1]);
      envelope = { code: 0, data: biz === "test-biz" ? { total: articles.length, downloaded: downloadedIds.length, article_ids: downloadedIds, download_dir: "C:\\Downloads\\测试公众号" } : { total: 0, downloaded: 0, article_ids: [], download_dir: "C:\\Downloads\\其他公众号" } };
    }
    else if (path === "/api/desktop/local-library?biz=test-biz") {
      const items = typeof localLibrary === "function" ? await localLibrary(++localLibraryRequests) : localLibrary;
      envelope = { code: 0, data: { articles: items, total: items.length } };
    }
    else if (path.startsWith("/api/desktop/local-library?biz=")) envelope = { code: 0, data: { articles: [], total: 0 } };
    else if (path.startsWith("/api/desktop/imported-status?")) {
      const status = typeof fixture.importedStatus === "function" ? fixture.importedStatus(path) : fixture.importedStatus ?? { state: "none" };
      envelope = { code: 0, data: status };
    }
    else if (path === "/api/desktop/article?biz=test-biz&id=article-1") envelope = articleResponse;
    else if (fixture.localArticleId && path === "/api/desktop/article?biz=test-biz&id=" + fixture.localArticleId) envelope = fixture.localArticleResponse;
    else if (path === "/api/mp/import_url") envelope = fixture.importResponse;
    else if (path === "/api/mp/article/probe") envelope = await (fixture.probeResponse ?? { code: 0, data: { title: "已验证" } });
    else if (path === "/api/desktop/queue") envelope = await (fixture.queueResponse ?? { code: 0, data: { created: 0, skipped: 1, failed: 0, blocked: 0 } });
    else if (path === "/api/task/create2") envelope = fixture.taskCreateResponse;
    else if (path === "/api/open_download_dir") envelope = { code: 0 };
    else if (path === "/api/desktop/download-summary") envelope = { code: 0, data: [] };
    else if (path.startsWith("/api/task/list?")) {
      const tasks = typeof fixture.tasks === "function" ? fixture.tasks() : fixture.tasks ?? [];
      envelope = { code: 0, data: { list: tasks, total: tasks.length } };
    }
    else throw new Error(`unexpected request: ${path}`);
    return { ok: true, json: async () => envelope };
  };
  runInNewContext(script, {
    document: { getElementById: get, createElement: (tag) => new Element("", tag), createTextNode: (value) => ({ textContent: value, children: [] }), createDocumentFragment: () => new Element() },
    window: { addEventListener() {}, crypto: null, location: { origin: "http://127.0.0.1:2132" } },
    navigator: { clipboard }, localStorage: { getItem: () => null, setItem() {} },
    fetch, URL, URLSearchParams, AbortController, setTimeout: setTimer, clearTimeout: (id) => timers.delete(id), setInterval: () => 0,
    console
  }, { filename: "desktop.html" });
  await tick();
  await get("accountsNav").click();
  await firstCatalogCard(get).click();
  await tick();
  await tick();
  return { get, calls, requests, advance, clipboardWrites };
}

{
  const { get, calls, advance, clipboardWrites } = await scenario();
  const rows = get("articleList").children[0].children;
  assert.equal(rows.length, 2);
  assert.equal(allNodes(get("articleList")).filter((element) => element.tagName === "BUTTON" && /复制链接/.test(nodeText(element))).length, 0, "article rows stay focused on reading and selection");
  assert.equal(allNodes(rows[0]).filter((element) => element.tagName === "BUTTON" && nodeText(element) === "阅读").length, 0, "downloaded history rows do not repeat the read action as a separate button");
  const downloadedTitle = allNodes(rows[0]).find((element) => element.tagName === "BUTTON" && String(element.className || "").includes("article-title-button"));
  assert.ok(downloadedTitle, "a downloaded history title itself opens the local reader");
  const undownloadedTitle = allNodes(rows[1]).find((element) => String(element.className || "").includes("article-title"));
  assert.ok(undownloadedTitle);
  assert.notEqual(undownloadedTitle.tagName, "BUTTON", "an undownloaded title has no local reader action");
  await undownloadedTitle.click();
  assert.equal(get("readerView").hidden, true, "clicking an undownloaded title does not open the reader");
  assert.equal(calls.filter((path) => path.startsWith("/api/desktop/article?")).length, 0);
  get("mainPane").scrollTop = 420;
  await downloadedTitle.click();
  await tick();
  assert.equal(calls.filter((path) => path === "/api/desktop/article?biz=test-biz&id=article-1").length, 1, "reader fetches the selected local article");
  assert.equal(get("readerView").hidden, false);
  assert.equal(get("accountView").hidden, true);
  assert.match(get("readerTitle").textContent, /已保存文章/);
  assert.match(get("readerFrame").srcdoc, /正文来自本地 Markdown/);
  assert.equal(get("readerCopyLink").disabled, false, "a saved history article exposes its original URL in the reader toolbar");
  await get("readerCopyLink").click();
  assert.deepEqual(clipboardWrites, ["https://mp.weixin.qq.com/s?mid=1"], "the reader copies the exact source article URL");
  assert.match(get("noticeMessage").textContent, /文章链接已复制/);
  assert.equal(get("notice").hidden, false);
  await advance(4999);
  assert.equal(get("notice").hidden, false, "success feedback stays visible briefly");
  await advance(1);
  assert.equal(get("notice").hidden, true, "success feedback disappears after five seconds");
  await get("readerBack").click();
  assert.equal(get("readerView").hidden, true);
  assert.equal(get("accountView").hidden, false);
  assert.equal(get("mainPane").scrollTop, 420, "returning from reader restores the article list position");
  assert.equal(get("articleList").children[0].children.length, 2, "article list is retained");
  get("mainPane").scrollTop = 275;
  await downloadedTitle.click();
  await get("accountsNav").click();
  assert.equal(get("readerView").hidden, true, "the account catalogue navigation closes the reader");
  assert.equal(get("accountsView").hidden, false);
  await firstCatalogCard(get).click();
  assert.equal(get("accountView").hidden, false, "the same account can be reopened from the catalogue");
}

{
  const { get, advance } = await scenario({ code: 404, msg: "本地 Markdown 文件不存在" });
  const title = allNodes(get("articleList").children[0].children[0]).find((element) => element.tagName === "BUTTON" && String(element.className || "").includes("article-title-button"));
  await title.click();
  await tick();
  assert.equal(get("readerView").hidden, false, "a missing file keeps a usable reader with a back action");
  assert.match(get("readerFrame").srcdoc, /无法读取这篇文章/, "a missing file does not leave a blank reader");
  assert.match(get("noticeMessage").textContent, /本地 Markdown 文件不存在/);
  await advance(7999);
  assert.equal(get("notice").hidden, false, "an error stays visible long enough to read");
  await advance(1);
  assert.equal(get("notice").hidden, true, "an error notice also expires automatically");
  await get("readerBack").click();
  assert.equal(get("accountView").hidden, false);
}

{
  let finishCopy;
  const clipboard = { writeText: () => new Promise((resolve) => { finishCopy = resolve; }) };
  const { get } = await scenario(undefined, { clipboard });
  const title = allNodes(get("articleList")).find((element) => element.tagName === "BUTTON" && String(element.className || "").includes("article-title-button"));
  await title.click();
  const copy = get("readerCopyLink").click();
  await tick();
  await get("welcomeNav").click();
  assert.equal(get("notice").hidden, true, "navigating away clears feedback from the reader");
  finishCopy();
  await copy;
  assert.equal(get("welcomeView").hidden, false);
  assert.equal(get("notice").hidden, true, "late clipboard completion cannot post reader feedback on another tab");
}

for (const failed of [false, true]) {
  let finishProbe;
  const probeResponse = new Promise((resolve) => { finishProbe = resolve; });
  const { get, calls } = await scenario(undefined, { probeResponse });
  const queue = get("queueAll").click();
  await tick();
  assert.ok(calls.includes("/api/mp/article/probe"), "batch preflight has started");
  await get("welcomeNav").click();
  finishProbe(failed ? { code: 403, msg: "微信访问验证" } : { code: 0, data: { title: "已验证" } });
  await queue;
  await tick();
  assert.equal(get("welcomeView").hidden, false, "a finished old batch must not navigate away from the user's new tab");
  assert.equal(get("downloadsView").hidden, true);
  assert.equal(get("notice").hidden, true, "a finished old batch must not show its success or error on another tab");
}

{
  let finishOldScan;
  const oldScan = new Promise((resolve) => { finishOldScan = resolve; });
  const otherArticle = { id: "other-1", title: "另一个公众号文章", url: "https://mp.weixin.qq.com/s?mid=99", published: 1700001000 };
  const { get } = await scenario(undefined, {
    accounts: [{ biz: "test-biz", nickname: "测试公众号" }, { biz: "other-biz", nickname: "其他公众号" }],
    scanResponses: {
      "test-biz": oldScan,
      "other-biz": { code: 0, data: { status: "complete", message: "已读取", pages: 1, updated: 2, articles: [otherArticle], options: { biz: "other-biz", mode: "all" } } }
    }
  });
  await get("accountsNav").click();
  await catalogCard(get, "其他公众号").click();
  await tick();
  assert.match(nodeText(get("articleList")), /另一个公众号文章/);
  finishOldScan({ code: 0, data: { status: "complete", message: "已读取", pages: 1, updated: 1, articles: [{ id: "article-1", title: "旧账号迟到的文章", url: "https://mp.weixin.qq.com/s?mid=1" }], options: { biz: "test-biz", mode: "all" } } });
  await tick();
  await tick();
  assert.equal(get("accountTitle").textContent, "其他公众号");
  assert.match(nodeText(get("articleList")), /另一个公众号文章/, "a late scan response cannot replace the new account's list");
  assert.doesNotMatch(nodeText(get("articleList")), /旧账号迟到的文章/);
}

{
  let finishRepair;
  const repairResponse = new Promise((resolve) => { finishRepair = resolve; });
  const selectedURL = "https://mp.weixin.qq.com/s?__biz=test-biz&mid=2&idx=1";
  const repairedURL = selectedURL + "&chksm=verified";
  const articles = [
    { id: "article-1", title: "不选择的文章", url: "https://mp.weixin.qq.com/s?__biz=test-biz&mid=1&idx=1&chksm=verified", published: 1700000000 },
    { id: "article-2", title: "选中的文章", url: selectedURL, published: 1700000001 }
  ];
  const repaired = articles.map((article) => article.id === "article-2" ? { ...article, url: repairedURL } : article);
  const otherArticle = { id: "other-1", title: "新账号自己的文章", url: "https://mp.weixin.qq.com/s?mid=99", published: 1700001000 };
  const { get, calls, requests } = await scenario(undefined, {
    articles, repairResponse,
    accounts: [{ biz: "test-biz", nickname: "测试公众号" }, { biz: "other-biz", nickname: "其他公众号" }],
    scanResponses: {
      "other-biz": { code: 0, data: { status: "complete", message: "已读取", pages: 1, updated: 2, articles: [otherArticle], options: { biz: "other-biz", mode: "all" } } }
    }
  });
  const rows = get("articleList").children[0].children;
  rows[1].children[0].checked = true;
  rows[1].children[0].listeners.get("change")();
  const queue = get("queueSelected").click();
  await tick();
  assert.ok(calls.includes("/api/desktop/scan/repair"), "the selected article needs its unsigned link repaired");
  await get("accountsNav").click();
  await catalogCard(get, "其他公众号").click();
  await tick();
  finishRepair({ code: 0, data: { scan: { status: "complete", message: "已读取", pages: 1, updated: 3, articles: repaired, options: { biz: "test-biz", mode: "all" } } } });
  await queue;
  await tick();
  const queued = requests.filter((request) => request.path === "/api/desktop/queue");
  assert.equal(queued.length, 1, "the old batch continues after changing accounts");
  const jobs = JSON.parse(queued[0].body);
  assert.equal(jobs.length, 1, "the repaired batch retains the original one-article selection");
  assert.equal(jobs[0].URL, "officialaccount://" + repairedURL);
  assert.equal(get("accountTitle").textContent, "其他公众号");
  assert.match(nodeText(get("articleList")), /新账号自己的文章/, "old repair results must not overwrite the current account");
  assert.doesNotMatch(nodeText(get("articleList")), /选中的文章/);
  assert.equal(get("notice").hidden, true, "the old batch does not show feedback over the new account");
}

{
  const localArticleId = "local_0123456789abcdef0123456789abcdef";
  const localArticle = { id: localArticleId, title: "历史以外的本地文章", published: 0 };
  const { get, calls } = await scenario(undefined, {
    articles: [], downloadedIds: [], localLibrary: [localArticle], localArticleId,
    localArticleResponse: { code: 0, data: { ...localArticle, html: "<h2>来自磁盘的 Markdown 正文</h2><p>离线可读</p>" } }
  });
  assert.equal(get("articleCount").textContent, "0 篇", "history scan remains empty");
  assert.equal(get("localLibraryCard").hidden, false, "local documents are shown even when history is empty");
  assert.match(nodeText(get("localLibraryList")), /历史以外的本地文章/);
  assert.equal(allNodes(get("localLibraryList")).filter((element) => element.tagName === "BUTTON" && nodeText(element) === "阅读").length, 0, "local document rows do not repeat the read action as a separate button");
  const title = allNodes(get("localLibraryList")).find((element) => element.tagName === "BUTTON" && String(element.className || "").includes("article-title-button"));
  assert.ok(title, "a local document title offers a read action");
  await title.click();
  await tick();
  assert.ok(calls.includes("/api/desktop/article?biz=test-biz&id=" + localArticleId));
  assert.equal(get("readerView").hidden, false);
  assert.match(get("readerFrame").srcdoc, /来自磁盘的 Markdown 正文/);
  assert.equal(get("readerCopyLink").disabled, true, "a local-only document without an original URL cannot copy an empty link");
}

{
  let finishLookup;
  const pendingLibrary = new Promise((resolve) => { finishLookup = resolve; });
  const newLocalArticle = { id: "local_0123456789abcdef0123456789abcdef", title: "后来保存的本地文档", published: 0 };
  const { get } = await scenario(undefined, { articles: [], downloadedIds: [], localLibrary: (request) => request === 1 ? pendingLibrary : [newLocalArticle] });
  assert.equal(get("localLibraryCard").hidden, false, "an empty history shows the local library while it loads");
  assert.match(nodeText(get("localLibraryList")), /正在查找本地文档/);
  assert.doesNotMatch(get("accountDetail").textContent, /0\s*\/\s*0/, "zero history is not labelled as zero downloaded files");
  finishLookup([]);
  await tick();
  await tick();
  assert.equal(get("localLibraryCard").hidden, false, "an empty local library remains visible with an explanation");
  assert.match(nodeText(get("localLibraryList")), /暂无可阅读的本地 Markdown 文档/);
  await get("accountRefresh").click();
  await tick();
  assert.match(nodeText(get("localLibraryList")), /后来保存的本地文档/, "selecting the same account refreshes newly saved local files");
}

{
  const localArticleId = "local_0123456789abcdef0123456789abcdef";
  const localArticle = { id: localArticleId, title: "历史以外的本地文章", published: 0 };
  const fixture = {
    articles: [], downloadedIds: [], localArticleId,
    localArticleResponse: { code: 0, data: { ...localArticle, html: "<p>单篇文章已在本地</p>" } },
    localLibrary: (request) => request === 1 ? [] : [localArticle],
    importResponse: { code: 0, data: { biz: "test-biz", nickname: "测试公众号", title: localArticle.title, history_available: false, history_reason: "当前链接只能下载单篇。" } }
  };
  const { get, calls } = await scenario(undefined, fixture);
  get("importUrl").value = "https://mp.weixin.qq.com/s?__biz=test-biz&mid=42&idx=1";
  await get("importForm").submit();
  assert.equal(get("accountView").hidden, false, "reimporting an existing account stays on its article page");
  assert.equal(get("localLibraryCard").hidden, false, "same-account import refreshes the local library");
  assert.equal(get("downloadImported").textContent, "阅读这篇文章");
  assert.match(get("importedArticleHint").textContent, /已保存到本机/);
  assert.doesNotMatch(get("importMessage").textContent, /只能下载/, "saved imports should not be described as download-only");
  assert.doesNotMatch(get("accountDetail").textContent, /0\s*\/\s*0/);
  assert.ok(calls.filter((path) => path === "/api/desktop/local-library?biz=test-biz").length >= 2);
  await get("downloadImported").click();
  assert.equal(calls.filter((path) => path === "/api/task/create2").length, 0, "a saved import is opened, never downloaded twice");
  assert.ok(calls.includes("/api/desktop/article?biz=test-biz&id=" + localArticleId));
  assert.match(get("readerFrame").srcdoc, /单篇文章已在本地/);
  await get("readerBack").click();
  await get("welcomeNav").click();
  assert.equal(get("welcomeDownloadImported").textContent, "阅读这篇文章", "the home shortcut retains the local read action");
  await get("welcomeDownloadImported").click();
  assert.match(get("readerFrame").srcdoc, /单篇文章已在本地/, "the home shortcut opens the local reader");
}

{
  const localArticleId = "local_0123456789abcdef0123456789abcdef";
  const localArticle = { id: localArticleId, title: "历史以外的本地文章", published: 0 };
  const { get, calls } = await scenario(undefined, {
    articles: [], downloadedIds: [], localArticleId,
    localArticleResponse: { code: 0, data: { ...localArticle, html: "<p>重复任务对应的本地正文</p>" } },
    localLibrary: (request) => request <= 2 ? [] : [localArticle],
    importResponse: { code: 0, data: { biz: "test-biz", nickname: "测试公众号", title: localArticle.title, history_available: false } },
    taskCreateResponse: { code: 409, msg: "目标目录已存在该文章文件" }
  });
  get("importUrl").value = "https://mp.weixin.qq.com/s?__biz=test-biz&mid=42&idx=1";
  await get("importForm").submit();
  assert.equal(get("downloadImported").textContent, "下载这篇文章");
  await get("downloadImported").click();
  assert.equal(get("downloadsView").hidden, true, "an existing file does not send the user to the download queue");
  assert.equal(get("downloadImported").textContent, "阅读这篇文章", "an existing-file response refreshes the reader action");
  assert.match(get("noticeMessage").textContent, /未重复下载.*阅读这篇文章/);
  await get("downloadImported").click();
  assert.equal(calls.filter((path) => path === "/api/task/create2").length, 1, "the read action does not retry the download");
  assert.match(get("readerFrame").srcdoc, /重复任务对应的本地正文/);
}

{
  const articleURL = "https://mp.weixin.qq.com/s?__biz=test-biz&mid=42&idx=1";
  const localArticleId = "local_11111111111111111111111111111111";
  const sourceId = "0123456789abcdef";
  const localArticle = { id: localArticleId, title: "Markdown 正文中的另一标题", source_id: sourceId, published: 0 };
  let saved = false;
  const { get, calls } = await scenario(undefined, {
    articles: [], downloadedIds: [],
    localArticleId,
    localArticleResponse: { code: 0, data: { ...localArticle, html: "<p>下载完成后读取的本地 Markdown</p>" } },
    importedStatus: () => saved
      ? { state: "readable", local_article: { biz: "test-biz", id: localArticleId, title: localArticle.title, published: 0 } }
      : { state: "none" },
    localLibrary: () => saved ? [localArticle] : [],
    importResponse: { code: 0, data: { biz: "test-biz", nickname: "测试公众号", title: "导入时识别的标题", source_id: sourceId, history_available: false } },
    tasks: () => [{
      id: "one-article-task", status: saved ? "done" : "running", files_exist: saved,
      ...(saved ? { local_article: { biz: "test-biz", id: localArticleId, title: localArticle.title, published: 0 } } : {}),
      meta: { opts: { name: "导入时识别的标题", path: "C:\\Downloads\\测试公众号" }, req: { url: "officialaccount://" + articleURL, labels: { account_biz: "test-biz", account_name: "测试公众号", article_title: "导入时识别的标题" } } }
    }]
  });
  await get("welcomeNav").click();
  get("importUrl").value = articleURL;
  await get("importForm").submit();
  assert.equal(get("downloadImported").textContent, "下载这篇文章", "a newly imported unsaved article still offers download");
  await get("downloadsNav").click();
  for (let attempt = 0; attempt < 20 && !nodeText(get("taskList")).includes("下载中"); attempt++) await tick();
  assert.match(nodeText(get("taskList")), /下载中/, "the download center first observes the running task");
  saved = true;
  await get("downloadsRefresh").click();
  for (let attempt = 0; attempt < 20 && get("welcomeDownloadImported").textContent !== "阅读这篇文章"; attempt++) await tick();
  assert.ok(calls.some((path) => path.startsWith("/api/desktop/imported-status?") && path.includes("source_id=" + sourceId)), "imported status is checked by article identity");
  assert.equal(get("welcomeDownloadImported").textContent, "阅读这篇文章", "the completed task refreshes the imported article action");
  await get("welcomeNav").click();
  assert.equal(get("welcomeDownloadImported").textContent, "阅读这篇文章", "switching tabs retains the local read action");
  await get("welcomeDownloadImported").click();
  assert.equal(calls.filter((path) => path === "/api/task/create2").length, 0, "reading after download must not queue the article again");
  assert.ok(calls.includes("/api/desktop/article?biz=test-biz&id=" + localArticleId));
  assert.match(get("readerFrame").srcdoc, /下载完成后读取的本地 Markdown/);
}

for (const [status, action] of [["downloading", "查看下载进度"], ["saved", "打开下载目录"]]) {
  const { get, calls } = await scenario(undefined, {
    articles: [], downloadedIds: [], localLibrary: [],
    importResponse: { code: 0, data: { biz: "test-biz", nickname: "测试公众号", title: "状态待核对的文章", source_id: "fedcba9876543210", history_available: false } },
    importedStatus: { state: status }
  });
  await get("welcomeNav").click();
  get("importUrl").value = "https://mp.weixin.qq.com/s?__biz=test-biz&mid=42&idx=1";
  await get("importForm").submit();
  assert.equal(get("downloadImported").textContent, action, `${status} uses its own action on the account page`);
  assert.equal(get("welcomeDownloadImported").textContent, action, `${status} uses the same action on the home page`);
  await get("downloadImported").click();
  assert.equal(calls.filter((path) => path === "/api/task/create2").length, 0, `${status} never creates a duplicate task`);
  if (status === "downloading") {
    assert.equal(get("downloadsView").hidden, false, "a running import opens download progress");
  } else {
    assert.equal(get("downloadsView").hidden, true, "a saved import stays on its current page");
    assert.equal(calls.filter((path) => path === "/api/open_download_dir").length, 1, "a saved import opens the download directory");
  }
}

{
  const localArticleId = "local_22222222222222222222222222222222";
  const sourceURL = "https://mp.weixin.qq.com/s?__biz=test-biz&idx=1&mid=42&sn=source42&chksm=check42&scene=1";
  const responseURL = "https://mp.weixin.qq.com/s?__biz=test-biz&mid=42&idx=1&sn=source42&scene=1&chksm=check42&key=dummy-key&pass_ticket=dummy-test-value";
  const localArticle = { id: localArticleId, title: "已保存的单篇文章", source_id: "0123456789abcdef", published: 0 };
  const { get, calls, clipboardWrites } = await scenario(undefined, {
    articles: [], downloadedIds: [], localLibrary: [localArticle], localArticleId,
    localArticleResponse: { code: 0, data: { ...localArticle, html: "<p>本地 Markdown 正文</p>", url: responseURL } }
  });
  const title = allNodes(get("localLibraryList")).find((element) => element.tagName === "BUTTON" && String(element.className || "").includes("article-title-button"));
  assert.ok(title, "the local document title is readable without a current import");
  await title.click();
  assert.ok(calls.includes("/api/desktop/article?biz=test-biz&id=" + localArticleId));
  assert.equal(get("readerCopyLink").disabled, false, "the reader enables copying when the local article API supplies its source URL");
  await get("readerCopyLink").click();
  assert.deepEqual(clipboardWrites, [sourceURL], "the reader retains chksm and scene but excludes key and pass_ticket");
}

console.log("desktop local Markdown reader flow passes");
