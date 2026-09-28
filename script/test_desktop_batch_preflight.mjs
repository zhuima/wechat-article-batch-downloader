import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

const html = readFileSync(new URL("../mp_article_downloader_src/internal/api/ui/desktop.html", import.meta.url), "utf8");
const script = html.match(/<script>([\s\S]*?)<\/script>/)?.[1];
assert.ok(script);

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
  setAttribute() {}
  click() { return this.listeners.get("click")?.({ preventDefault() {} }); }
}

const article = (n) => ({ id: `article-${n}`, title: `文章 ${n}`, url: `https://mp.weixin.qq.com/s?mid=${n}`, published: n });
const verification = { code: 500, msg: "探测文章失败：微信要求完成访问验证，请在微信中打开文章后重试" };

async function scenario({ articles, probe, taskOutcome, queueResponse, repair, downloadedIds = [], clickQueue = true }) {
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new Element(id));
    return elements.get(id);
  };
  const probes = [], chunks = [], tasks = [], repairCalls = [];
  let nextTask = 0;
  const fetch = async (path, options = {}) => {
    let envelope;
    if (path === "/api/desktop/info") envelope = { code: 0, data: { download_dir: "C:\\Downloads" } };
    else if (path.startsWith("/api/mp/list?")) envelope = { code: 0, data: { list: [{ biz: "test-biz", nickname: "每天晒白牙", is_effective: true }], total: 1 } };
    else if (path === "/api/desktop/scan?biz=test-biz") envelope = { code: 0, data: { status: "complete", message: "已读取", pages: 1, updated: 1, articles, options: { biz: "test-biz", mode: "all" } } };
    else if (path === "/api/desktop/local-status?biz=test-biz") envelope = { code: 0, data: { total: articles.length, downloaded: downloadedIds.length, article_ids: downloadedIds, download_dir: "C:\\Downloads\\每天晒白牙" } };
    else if (path === "/api/desktop/local-library?biz=test-biz") envelope = { code: 0, data: { articles: [], total: 0 } };
    else if (path === "/api/mp/article/probe") {
      const url = JSON.parse(options.body).URL;
      probes.push(url);
      envelope = probe(url);
    } else if (path === "/api/desktop/scan/repair") {
      repairCalls.push(JSON.parse(options.body));
      envelope = { code: 0, data: repair() };
    } else if (path === "/api/desktop/queue") {
      const chunk = JSON.parse(options.body);
      chunks.push(chunk);
      const response = queueResponse?.(chunk, chunks.length) || {};
      const created = response.created ?? chunk.length;
      const ids = chunk.slice(0, created).map(() => `task-${++nextTask}`);
      chunk.slice(0, created).forEach((item, index) => {
        const outcome = taskOutcome(item, chunks.length, index);
        tasks.push({ id: ids[index], status: outcome.status, error: outcome.error || "", files_exist: outcome.files_exist ?? false, meta: { req: { labels: item.Extra } } });
      });
      envelope = { code: 0, data: { created, created_ids: ids, skipped: 0, failed: 0, blocked: 0, verification_required: false, ...response } };
    } else if (path.startsWith("/api/task/list?")) envelope = { code: 0, data: { list: tasks.slice().reverse(), total: tasks.length } };
    else if (path === "/api/desktop/download-summary") envelope = { code: 0, data: [] };
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
  await get("accountsNav").click();
  await get("catalogList").children[0].children[0].click();
  await new Promise((resolve) => setImmediate(resolve));
  if (clickQueue) await get("queueAll").click();
  return { get, probes, chunks, tasks, repairCalls };
}

{
  const articles = [1, 2, 3].map(article);
  const result = await scenario({ articles, downloadedIds: articles.map(item => item.id), clickQueue: false });
  assert.match(result.get("localStatusTitle").textContent, /3 篇文章已下载到本地/);
  assert.match(result.get("accountDetail").textContent, /本地已下载 3 \/ 3 篇/);
  assert.equal(result.get("queueAll").disabled, true);
  assert.equal(result.get("queueAll").textContent, "已全部下载 3 篇");
  const rows = result.get("articleList").children[0].children;
  assert.equal(rows.length, 3);
  assert.ok(rows.every(row => row.children[1].children.some(child => child.textContent.includes("已下载到本地"))));
}

{
  const result = await scenario({
    articles: [1, 2, 3].map(article), downloadedIds: ["article-2", "article-3"],
    probe: () => ({ code: 0, data: { mode: "full" } }),
    taskOutcome: () => ({ status: "done", files_exist: true })
  });
  assert.match(result.get("localStatusTitle").textContent, /2 \/ 3 篇文章/);
  assert.equal(result.chunks.flat().length, 1, "only a missing article is queued");
  assert.match(result.chunks[0][0].URL, /mid=1$/);
}

{
  const old = [1, 2].map((n) => ({ ...article(n), url: `https://mp.weixin.qq.com/s?__biz=MzTest&mid=${n}&idx=1&sn=signature${n}` }));
  const full = old.map((item) => ({ ...item, url: item.url + "&chksm=checksum&scene=142" }));
  const result = await scenario({
    articles: old,
    repair: () => ({ repaired: 2, source_total: 2, scan: { status: "complete", articles: full, options: { biz: "test-biz", mode: "all" } } }),
    probe: (url) => url.includes("chksm=checksum") ? { code: 0, data: { mode: "full" } } : verification,
    taskOutcome: () => ({ status: "done", files_exist: true })
  });
  assert.deepEqual(result.repairCalls, [{ biz: "test-biz" }], "cached links are repaired once before probing");
  assert.equal(result.probes.length, 2);
  assert.ok(result.probes.every((url) => url.includes("chksm=checksum")), "probes use repaired links");
  assert.ok(result.chunks.flat().every((item) => item.URL.includes("chksm=checksum")), "downloads use repaired links");
}

{
  const result = await scenario({ articles: Array.from({ length: 185 }, (_, i) => article(i + 1)), probe: () => verification, taskOutcome: () => ({ status: "done", files_exist: true }) });
  assert.equal(result.probes.length, 3, "a middle article is checked before rejecting the batch");
  assert.equal(result.chunks.length, 0, "three verification failures create no tasks");
  assert.match(result.get("noticeMessage").textContent, /0 篇入队.*微信验证|微信验证.*0 篇入队/);
}

{
  const result = await scenario({
    articles: Array.from({ length: 10 }, (_, i) => article(i + 1)),
    probe: (url) => /mid=(10|1)$/.test(url) ? verification : { code: 0, data: { mode: "full" } },
    taskOutcome: () => ({ status: "done", files_exist: true })
  });
  assert.equal(result.probes.length, 3, "middle article is checked after both endpoints trigger verification");
  assert.deepEqual(result.chunks.map((chunk) => chunk.length), [3, 5], "available middle article allows the batch to proceed");
  assert.ok(result.chunks.flat().every((item) => !/mid=(10|1)$/.test(item.URL)), "blocked endpoint articles are skipped");
  assert.match(result.get("noticeMessage").textContent, /预检跳过 2/);
}

{
  const result = await scenario({
    articles: Array.from({ length: 55 }, (_, i) => article(i + 1)),
    probe: (url) => url.endsWith("mid=1") ? verification : { code: 0, data: { mode: "full" } },
    taskOutcome: () => ({ status: "done", files_exist: true })
  });
  assert.deepEqual(result.chunks.map((chunk) => chunk.length), [3, 50, 1]);
  assert.equal(result.chunks[0][0].URL, "officialaccount://https://mp.weixin.qq.com/s?mid=55", "real tasks start at newest article");
  assert.ok(result.chunks.flat().every((item) => !item.URL.endsWith("mid=1")));
  assert.match(result.get("noticeMessage").textContent, /预检跳过 1/);
  assert.match(result.get("noticeMessage").textContent, /已入队不代表下载完成/);
}

{
  const result = await scenario({
    articles: Array.from({ length: 10 }, (_, i) => article(i + 1)),
    probe: (url) => url.endsWith("mid=10") ? { code: 500, msg: "文章已被发布者删除" }
      : url.endsWith("mid=1") ? verification : { code: 0, data: { mode: "full" } },
    taskOutcome: () => ({ status: "done", files_exist: true })
  });
  assert.equal(result.probes.length, 3, "a mixed pair of endpoint failures gets one middle check");
  assert.deepEqual(result.chunks.map((chunk) => chunk.length), [3, 5]);
  assert.match(result.get("noticeMessage").textContent, /预检跳过 2/);
}

{
  const result = await scenario({
    articles: Array.from({ length: 60 }, (_, i) => article(i + 1)),
    probe: () => ({ code: 0, data: { mode: "full" } }),
    taskOutcome: (_item, chunk, index) => chunk === 1 ? index === 0
      ? { status: "error", error: "微信要求完成访问验证" }
      : { status: "pause" }
      : { status: "done", files_exist: true }
  });
  assert.deepEqual(result.chunks.map((chunk) => chunk.length), [3], "verification in the pilot stops the rest");
  assert.match(result.get("noticeMessage").textContent, /真实下载触发微信访问验证/);
  assert.match(result.get("noticeMessage").textContent, /尚未提交 57/);
}

{
  const result = await scenario({
    articles: Array.from({ length: 60 }, (_, i) => article(i + 1)),
    probe: () => ({ code: 0, data: { mode: "full" } }),
    taskOutcome: () => ({ status: "pause" }),
    queueResponse: (chunk) => ({ created: 1, blocked: chunk.length - 1, verification_required: true })
  });
  assert.deepEqual(result.chunks.map((chunk) => chunk.length), [3], "server verification blocks later chunks");
  assert.match(result.get("noticeMessage").textContent, /验证拦截 2/);
  assert.match(result.get("noticeMessage").textContent, /尚未提交 57/);
}

{
  const result = await scenario({
    articles: Array.from({ length: 10 }, (_, i) => article(i + 1)),
    probe: () => ({ code: 0, data: { mode: "full" } }),
    taskOutcome: (_item, chunk, index) => chunk === 1 && index === 0
      ? { status: "error", error: "文章已被发布者删除" }
      : { status: "done", files_exist: true }
  });
  assert.deepEqual(result.chunks.map((chunk) => chunk.length), [3, 7], "one article-specific failure does not block the batch");
}

{
  const result = await scenario({ articles: [article(1)], probe: () => ({ code: 0, data: { mode: "full" } }), taskOutcome: () => ({ status: "done", files_exist: true }) });
  assert.equal(result.probes.length, 1, "single-article selection is probed only once");
  assert.deepEqual(result.chunks.map((chunk) => chunk.length), [1]);
}

console.log("desktop batch preflight and real-task gate pass");
