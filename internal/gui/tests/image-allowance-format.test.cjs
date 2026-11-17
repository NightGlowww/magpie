// Shipped quota renderers with a small DOM stand-in. No browser or network.
// A Codex image window keeps the API's name/display, but cards and the table
// explain that its allowance is separate and may not have been reported yet.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { test } = require("node:test");

const assets = process.env.MAGPIE_IMAGE_ALLOWANCE_ASSETS || path.resolve(__dirname, "../assets");
const source = fs.readFileSync(path.join(assets, "app.js"), "utf8");
const table = fs.readFileSync(path.join(assets, "quotatable.js"), "utf8");
const translation = fs.readFileSync(path.join(assets, "i18n.js"), "utf8");
const dict = new Function(translation.slice(0, translation.indexOf("\n};") + 3) + "; return I18N;")();
function section(start, end) {
  const a = source.indexOf(start), b = source.indexOf(end, a);
  assert(a >= 0 && b > a, `missing ${start}`);
  return source.slice(a, b);
}
class Element {
  constructor(tag, cls = "", ...content) {
    this.tagName = tag.toUpperCase();
    this.className = cls;
    this.children = [];
    this.dataset = {};
    this.style = {};
    this.attributes = {};
    this.classList = { add() {}, remove() {}, toggle() {} };
    this.append(...content);
  }
  append(...items) { this.children.push(...items.flat().filter(x => x != null)); }
  prepend(...items) { this.children.unshift(...items.flat().filter(x => x != null)); }
  setAttribute(name, value) { this.attributes[name] = value; }
  get textContent() { return this.children.map(x => x instanceof Element ? x.textContent : String(x)).join(""); }
  get lastChild() { return this.children.at(-1); }
}
const all = (el, cls) => [el, ...el.children.flatMap(x => x instanceof Element ? all(x, cls) : [])]
  .filter(x => !cls || x.className.split(" ").includes(cls));

const HINT = "Separate from chat. Status updates when an image request reports its allowance.";
const words = {
  en: ["Image allowance", "Not reported", "Used up", "Available", "Image generation"],
  zh: ["生图额度", "未获取", "已用完", "可用", "生图模型"],
  "zh-TW": ["生圖額度", "未取得", "已用完", "可用", "生圖模型"],
  ja: ["画像生成の利用枠", "未取得", "使い切り", "利用可能", "画像生成"],
  de: ["Bildkontingent", "Nicht gemeldet", "Aufgebraucht", "Verfügbar", "Bilderzeugung"],
};
const soon = new Date(Date.now() + 3 * 3600e3).toISOString();
const img = (display = "Unknown") => ({ name: "Image generation", lane: "images", text: true, used: 0, display });
function renderers(lang) {
  const t = (s, vars) => {
    let out = dict[lang]?.[s] ?? s;
    if (vars) for (const [k, v] of Object.entries(vars)) out = out.replaceAll("{" + k + "}", v);
    return out;
  };
  const ctx = vm.createContext({
    Date, t, el: (tag, cls, ...content) => new Element(tag, cls, ...content),
    resetText: () => "reset at 18:00", resetClock: () => "18:00", untilText: () => "in 3h",
    intlLang: () => "en", quotaCount: () => "", paceLine: () => null,
    runsOutText: () => "", quotaFit: { observe() {} },
    countFormat: new Intl.NumberFormat("en"), familyWindows: x => x,
    kept: () => [], quotaName: x => x.name || x.provider,
    quotaWho: () => [], trayCardID: x => x.provider + "|" + x.user,
    icon: () => new Element("i"), svg: () => "", CHEV_R: "", poolWord: x => x,
  });
  ctx.window = ctx;
  vm.runInContext(`
    let quotaLeft = false;
    ${section("function to100(", "\n")}
    ${section("function quotaNoShare(", "\n")}
    ${section("function tipLines(", "// quotaPace:")}
    ${section("function quotaWindows(", "// computerOpts")}
    ${table}
    globalThis.card = quotaWindows;
    globalThis.textOf = quotaText;
    globalThis.left = value => quotaLeft = value;
    globalThis.column = columnOf;
  `, ctx);
  return { ctx, t };
}

for (const [lang, [name, unknown, used, available, setting]] of Object.entries(words)) {
  test(`${lang}: image allowance is explained in cards and tables without changing the model setting`, () => {
    const { ctx, t } = renderers(lang);
    if (lang !== "en") for (const key of ["Image allowance", "Not reported", HINT]) assert.ok(dict[lang]?.[key], key);
    assert.equal(t("Image generation"), setting, "Settings keeps its image model label");
    for (const [state, expected] of [["Unknown", unknown], [undefined, unknown], ["Used up", used], ["Available", available]]) {
      const image = img(state);
      if (state === undefined) delete image.display;
      if (state !== "Unknown" && state !== undefined) image.resetsAt = soon;
      const q = { provider: "codex", name: "Codex", user: "test@example.com", windows: [
        { name: "5 hours", used: 40 }, { name: "7 days", used: 10 }, image,
      ] };
      const before = JSON.stringify(q);
      for (const left of [false, true]) {
        ctx.left(left);
        const card = ctx.card(q), row = all(card, "quota").at(-1);
        assert.equal(all(row, "quota-name")[0].textContent, name);
        const figure = all(row, "quota-n")[0];
        assert.equal(figure.textContent, expected);
        assert.equal(figure.disabled, true, "a text-only status cannot toggle a percentage");
        assert.ok(figure.title.includes(t(HINT)), "the card explains where the image status comes from");
        assert.doesNotMatch(figure.title, /Show how much/);
        assert.equal(all(row, "quota-track").length, 0);
        assert.doesNotMatch(row.textContent, /%/);
        assert.equal(all(card, "quota-track").length, 2, "chat windows keep their meters");
        const tab = ctx.quotaTable([q]);
        assert.ok(all(tab, "qt-sort").some(x => x.textContent === name), "the table's column says allowance");
        const cell = all(tab, "qt-win").find(x => all(x, "qt-n")[0]?.textContent === expected);
        assert.ok(cell, "the table renders the same image status");
        assert.equal(all(cell, "qt-name").length, 0, "the column name is not repeated in a single-window cell");
        assert.ok(cell.title.startsWith(name));
        assert.ok(cell.title.includes(t(HINT)), "the table explains the status too");
        assert.equal(all(cell, "quota-track").length, 0);
        assert.doesNotMatch(cell.textContent, /%/);
      }
      assert.equal(JSON.stringify(q), before, "remote/API window names and states are left intact");
    }
  });
}

test("other text rows, unlimited windows and ordinary shares keep their meaning", () => {
  const { ctx } = renderers("en");
  assert.equal(ctx.textOf({ text: true, used: 0, display: "Unknown" }), "Unknown");
  assert.equal(ctx.textOf({ text: true, used: 100, display: "active" }), "In use");
  assert.equal(ctx.textOf({ lane: "images", unlimited: true }), "Unlimited");
  assert.equal(ctx.textOf({ name: "5 hours", used: 0 }), "0% used");
  ctx.left(true);
  assert.equal(ctx.textOf({ name: "5 hours", used: 0 }), "100% left");
});
