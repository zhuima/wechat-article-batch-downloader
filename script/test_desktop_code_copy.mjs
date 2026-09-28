import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

const html = readFileSync(new URL("../mp_article_downloader_src/internal/api/ui/desktop.html", import.meta.url), "utf8");
const script = html.match(/<script>([\s\S]*?)<\/script>/)?.[1];
assert.ok(script, "desktop page has an inline script");
const sandbox = html.match(/<iframe\b[^>]*\bid="readerFrame"[^>]*\bsandbox="([^"]+)"/)?.[1];
assert.ok(sandbox, "reader iframe remains sandboxed");
assert.doesNotMatch(sandbox, /\ballow-scripts\b/, "code-copy controls must not enable scripts inside article HTML");

class Element {
  constructor(tagName = "div", id = "") {
    this.tagName = tagName.toUpperCase();
    this.id = id;
    this.className = "";
    this.textContent = "";
    this.value = id === "taskFilter" ? "all" : "";
    this.children = [];
    this.parentNode = null;
    this.listeners = new Map();
    this.attributes = new Map();
    this.dataset = {};
    this.style = {};
    this.hidden = false;
    this.disabled = false;
    this.scrollTop = 0;
    this.srcdoc = "";
    this.contentDocument = null;
    this.classList = {
      contains: (name) => this.className.split(/\s+/).includes(name),
      add: (...names) => { this.className = [...new Set([...this.className.split(/\s+/).filter(Boolean), ...names])].join(" "); },
      remove: (...names) => { this.className = this.className.split(/\s+/).filter((name) => name && !names.includes(name)).join(" "); },
      toggle: (name, force) => {
        const on = force === undefined ? !this.classList.contains(name) : force;
        if (on) this.classList.add(name); else this.classList.remove(name);
        return on;
      }
    };
  }
  addEventListener(name, listener) {
    if (!this.listeners.has(name)) this.listeners.set(name, []);
    this.listeners.get(name).push(listener);
  }
  async dispatch(name) {
    for (const listener of this.listeners.get(name) || []) await listener({ target: this, preventDefault() {}, stopPropagation() {} });
  }
  click() { return this.dispatch("click"); }
  append(...children) {
    for (const child of children) {
      if (child.parentNode) child.parentNode.removeChild(child);
      child.parentNode = this;
      this.children.push(child);
    }
  }
  appendChild(child) { this.append(child); return child; }
  prepend(...children) {
    for (const child of [...children].reverse()) {
      if (child.parentNode) child.parentNode.removeChild(child);
      child.parentNode = this;
      this.children.unshift(child);
    }
  }
  insertBefore(child, reference) {
    if (child.parentNode) child.parentNode.removeChild(child);
    const index = this.children.indexOf(reference);
    this.children.splice(index < 0 ? this.children.length : index, 0, child);
    child.parentNode = this;
    return child;
  }
  removeChild(child) {
    const index = this.children.indexOf(child);
    if (index >= 0) this.children.splice(index, 1);
    child.parentNode = null;
    return child;
  }
  replaceChildren(...children) {
    for (const child of this.children) child.parentNode = null;
    this.children = [];
    this.append(...children);
  }
  replaceWith(other) { this.parentNode?.insertBefore(other, this); this.parentNode?.removeChild(this); }
  setAttribute(name, value) {
    this.attributes.set(name, String(value));
    if (name.startsWith("data-")) this.dataset[name.slice(5).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase())] = String(value);
    else this[name] = String(value);
  }
  getAttribute(name) { return this.attributes.get(name) ?? null; }
  removeAttribute(name) {
    this.attributes.delete(name);
    if (name.startsWith("data-")) delete this.dataset[name.slice(5).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase())];
  }
  querySelectorAll(selector) {
    const match = (element) => selector.startsWith(".")
      ? element.classList.contains(selector.slice(1))
      : element.tagName.toLowerCase() === selector.toLowerCase();
    return this.children.flatMap((child) => [match(child) ? child : null, ...child.querySelectorAll(selector)].filter(Boolean));
  }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  focus() {}
}

class FrameDocument {
  constructor(blocks) {
    this.listeners = new Map();
    this.body = new Element("body");
    for (const block of blocks) {
      const pre = new Element("pre");
      if (block.code !== undefined) {
        const code = new Element("code");
        code.textContent = block.code;
        code.innerHTML = "<span class='token'>" + block.code + "</span>";
        pre.append(code);
        pre.textContent = "extra wrapper text should not be copied";
      } else pre.textContent = block.pre;
      this.body.append(pre);
    }
    const inline = new Element("code");
    inline.textContent = "inline code";
    this.body.append(inline);
  }
  createElement(tag) { return new Element(tag); }
  querySelectorAll(selector) { return this.body.querySelectorAll(selector); }
  querySelector(selector) { return this.body.querySelector(selector); }
  getElementById(id) { return [this.body, ...this.body.querySelectorAll("h2")].find((element) => element.id === id) || null; }
  addEventListener(name, listener) { this.listeners.set(name, listener); }
  dispatch(name, event) { return this.listeners.get(name)?.(event); }
}

function loadedArticleDocument(frame, blocks) {
  const version = frame.srcdoc.match(/<body data-reader-version="([0-9]+)">/)?.[1];
  assert.ok(version, "reader document records its active version");
  const document = new FrameDocument(blocks);
  document.body.setAttribute("data-reader-version", version);
  frame.contentDocument = document;
  return document;
}

const tick = () => new Promise((resolve) => setImmediate(resolve));
const articles = [
  { id: "article-1", title: "代码文章", url: "https://mp.weixin.qq.com/s?mid=1", published: 1700000000 },
  { id: "article-2", title: "另一篇文章", url: "https://mp.weixin.qq.com/s?mid=2", published: 1700000001 }
];

async function scenario(clipboard, articleTitle = "代码文章", options = {}) {
  const elements = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new Element("div", id));
    return elements.get(id);
  };
  const timers = new Map();
  let nextTimer = 0, now = 0;
  const setTimer = (callback, delay = 0) => {
    const id = ++nextTimer;
    timers.set(id, { callback, at: now + Number(delay) });
    return id;
  };
  const advance = async (milliseconds) => {
    const end = now + milliseconds;
    while (true) {
      const next = [...timers.entries()].filter(([, timer]) => timer.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
      if (!next) break;
      now = next[1].at;
      timers.delete(next[0]);
      await next[1].callback();
    }
    now = end;
  };
  const fetch = async (path) => {
    let data;
    if (path === "/api/desktop/info") data = { download_dir: "C:\\Downloads" };
    else if (path.startsWith("/api/mp/list?")) data = { list: [{ biz: "test-biz", nickname: "测试公众号", is_effective: true }], total: 1 };
    else if (path === "/api/desktop/scan?biz=test-biz") data = { status: "complete", articles: [{ ...articles[0], title: articleTitle, url: options.listURL === undefined ? articles[0].url : options.listURL }, articles[1]], pages: 1, updated: 1, options: { biz: "test-biz", mode: "all" } };
    else if (path === "/api/desktop/local-status?biz=test-biz") data = { total: 2, downloaded: 2, article_ids: ["article-1", "article-2"], download_dir: "C:\\Downloads\\测试公众号" };
    else if (path === "/api/desktop/local-library?biz=test-biz") data = { articles: [], total: 0 };
    else if (path.startsWith("/api/desktop/article?biz=test-biz&id=")) data = { title: articleTitle, html: "<pre><code>const x = 1;</code></pre>", published: 1700000000, url: options.sourceURL };
    else throw new Error("unexpected request: " + path);
    return { ok: true, json: async () => ({ code: 0, data }) };
  };
  runInNewContext(script, {
    document: {
      getElementById: get,
      createElement: (tag) => new Element(tag),
      createTextNode: (value) => { const text = new Element("#text"); text.textContent = String(value); return text; },
      createDocumentFragment: () => new Element("#fragment")
    },
    window: { addEventListener() {}, crypto: null, location: { origin: "http://127.0.0.1:2132" } },
    navigator: { clipboard }, localStorage: { getItem: () => null, setItem() {} },
    fetch, URL, AbortController, setTimeout: setTimer, clearTimeout: (id) => timers.delete(id), setInterval: () => 0, console
  }, { filename: "desktop.html" });
  await tick();
  await get("accountsNav").click();
  await get("catalogList").querySelector(".catalog-card").click();
  await tick();
  await tick();
  const row = get("articleList").querySelector(".article-row");
  await row.querySelector(".article-title-button").click();
  await tick();
  return { get, advance };
}

{
  const writes = [];
  const { get, advance } = await scenario({ writeText: async (value) => { writes.push(value); } });
  const frame = get("readerFrame");
  const firstDocument = loadedArticleDocument(frame, [{ code: "const α = 1;\n" }, { code: "print('second')\n" }, { pre: "plain block\n" }]);
  await frame.dispatch("load");
  const buttons = firstDocument.querySelectorAll(".reader-code-copy");
  assert.equal(buttons.length, 3, "each fenced code block gets its own copy button; inline code does not");
  await buttons[0].click();
  await buttons[1].click();
  await buttons[2].click();
  assert.deepEqual(writes, ["const α = 1;\n", "print('second')\n", "plain block\n"], "each button copies only its own code block's plain text");
  assert.equal(buttons[0].textContent, "已复制");
  assert.equal(buttons[1].textContent, "已复制");
  assert.equal(buttons[2].textContent, "已复制");
  await advance(10000);
  assert.equal(buttons[0].textContent, "复制", "success feedback expires");
  assert.equal(buttons[1].textContent, "复制");
}

{
  const encodedTitle = "Qwen3.8：从&amp;quot;编造数字&amp;quot;到&#x201c;零差错&#x201d;";
  const { get } = await scenario({ writeText: async () => {} }, encodedTitle);
  assert.equal(get("readerTitle").textContent, "Qwen3.8：从\"编造数字\"到“零差错”", "reader heading decodes HTML entities from saved article titles");
  const listTitle = get("articleList").querySelector(".article-title-button");
  assert.equal(listTitle.textContent, get("readerTitle").textContent, "the article list and reader use the same visible title");
  const frame = get("readerFrame");
  assert.match(frame.srcdoc, /pre\{[^}]*white-space:pre-wrap;overflow-wrap:anywhere/, "long code lines wrap inside the reader instead of hiding behind horizontal scrolling");
  assert.match(frame.srcdoc, /pre code\{[^}]*overflow-wrap:inherit/, "nested code keeps the wrapping rule");
}

{
  const shortLink = "https://mp.weixin.qq.com/s/OH1sYvZny-kMj41reDe0Ww";
  const writes = [];
  const { get } = await scenario({ writeText: async (value) => { writes.push(value); } }, "短链接文章", { listURL: "", sourceURL: shortLink });
  const copy = get("readerCopyLink");
  assert.equal(copy.disabled, false, "the reader enables the source link after the article API returns a short WeChat URL");
  assert.equal(copy.textContent, "复制文章链接");
  await copy.click();
  assert.deepEqual(writes, [shortLink]);
}

{
  const { get } = await scenario({ writeText: async () => {} });
  const frame = get("readerFrame");
  assert.doesNotMatch(frame.srcdoc, /<base\b/i, "article fragments do not inherit a base target that sends them outside the reader");
  const document = loadedArticleDocument(frame, []);
  const table = new Element("table");
  const heading = new Element("h2");
  heading.id = "三条线的约束对照";
  heading.scrollIntoView = () => { heading.scrolled = true; };
  document.body.append(table, heading);
  await frame.dispatch("load");
  assert.equal(table.parentNode.className, "reader-table-scroll", "wide tables get their own horizontal scroll container");
  const link = { getAttribute: () => "#%E4%B8%89%E6%9D%A1%E7%BA%BF%E7%9A%84%E7%BA%A6%E6%9D%9F%E5%AF%B9%E7%85%A7" };
  let prevented = false;
  document.dispatch("click", { target: { closest: () => link }, preventDefault() { prevented = true; } });
  assert.equal(prevented, true, "fragment links are handled without navigating the iframe away from the article");
  assert.equal(heading.scrolled, true, "encoded Chinese fragment links scroll to their heading");
}

{
  const { get, advance } = await scenario({ writeText: async () => { throw new Error("denied"); } });
  const frame = get("readerFrame");
  const document = loadedArticleDocument(frame, [{ code: "failed copy" }]);
  await frame.dispatch("load");
  const button = document.querySelector(".reader-code-copy");
  await button.click();
  assert.equal(button.textContent, "复制失败", "clipboard failure is visible next to the failed code block");
  await advance(10000);
  assert.equal(button.textContent, "复制", "failure feedback also expires");
}

{
  const writes = [];
  const { get, advance } = await scenario({ writeText: async (value) => { writes.push(value); } });
  const frame = get("readerFrame");
  const oldDocument = loadedArticleDocument(frame, [{ code: "old code" }]);
  await frame.dispatch("load");
  const oldButton = oldDocument.querySelector(".reader-code-copy");
  await oldButton.click();
  assert.equal(oldButton.textContent, "已复制");
  await get("readerBack").click();
  const secondRow = get("articleList").querySelectorAll(".article-row")[1];
  await secondRow.querySelector(".article-title-button").click();
  await tick();
  const newDocument = loadedArticleDocument(frame, [{ code: "new code" }]);
  await frame.dispatch("load");
  const newButton = newDocument.querySelector(".reader-code-copy");
  assert.equal(newButton.textContent, "复制", "opening another article starts without the previous copy feedback");
  await oldButton.click();
  assert.deepEqual(writes, ["old code"], "an obsolete article's copy button cannot act on the new reader");
  await newButton.click();
  assert.deepEqual(writes, ["old code", "new code"]);
  await advance(10000);
  assert.equal(newButton.textContent, "复制", "the new document owns its own feedback timer");
}

{
  let finishWrite;
  const { get, advance } = await scenario({ writeText: () => new Promise((resolve) => { finishWrite = resolve; }) });
  const frame = get("readerFrame");
  const oldDocument = loadedArticleDocument(frame, [{ code: "old code" }]);
  await frame.dispatch("load");
  const oldButton = oldDocument.querySelector(".reader-code-copy");
  const pending = oldButton.click();
  await tick();
  await get("readerBack").click();
  await get("welcomeNav").click();
  finishWrite();
  await pending;
  await advance(10000);
  assert.equal(get("welcomeView").hidden, false, "leaving the reader stays on the chosen tab");
  assert.equal(get("notice").hidden, true, "old code-copy feedback cannot appear as a global notice on another tab");
  assert.notEqual(oldButton.textContent, "已复制", "late clipboard completion does not mark a closed reader as copied");
  const newDocument = new FrameDocument([{ code: "new code" }]);
  frame.contentDocument = newDocument;
  await frame.dispatch("load");
  assert.equal(newDocument.querySelectorAll(".reader-code-copy").length, 0, "a late iframe load after navigation adds no controls");
}

console.log("desktop reader code-copy UI checks passed");
