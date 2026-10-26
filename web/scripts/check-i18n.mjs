#!/usr/bin/env node
// Kiểm tra i18n kiểu CI. Thoát mã 1 nếu:
//  1. locale nào thiếu / thừa key so với vi.json (nguồn chuẩn)
//  2. placeholder ICU ({name}, plural…) hoặc thẻ rich (<mono>) khác nhau giữa các locale
//  3. code gọi t("key") với key không có trong vi.json (phân tích tĩnh useTranslations/getTranslations)
//  4. còn chuỗi tiếng Việt cứng trong code UI (app/, components/, lib/ trừ lib/mock) — bỏ qua comment
//     và dòng có "i18n-ignore".
// Dùng: pnpm i18n:check
import fs from "node:fs";
import path from "node:path";

const root = path.resolve(path.dirname(new URL(import.meta.url).pathname), "..");
const LOCALES = ["vi", "en", "zh", "ja"];
const BASE = "vi";
let fail = 0;
const err = (m) => { console.error("  ✗ " + m); fail++; };

function flatten(obj, pre = "", out = {}) {
  for (const [k, v] of Object.entries(obj)) {
    const key = pre ? `${pre}.${k}` : k;
    if (v && typeof v === "object") flatten(v, key, out);
    else out[key] = String(v);
  }
  return out;
}
const msgs = Object.fromEntries(LOCALES.map((l) => [l, flatten(JSON.parse(fs.readFileSync(path.join(root, "messages", `${l}.json`), "utf8")))]));
const base = msgs[BASE];
const baseKeys = Object.keys(base);

// Tên biến ICU cấp ngoài cùng + thẻ rich text
function tokens(s) {
  const out = new Set();
  let depth = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (c === "{") {
      if (depth === 0) {
        const m = s.slice(i + 1).match(/^\s*([A-Za-z_][\w]*)/);
        if (m) out.add(`{${m[1]}}`);
      }
      depth++;
    } else if (c === "}") depth = Math.max(0, depth - 1);
  }
  for (const m of s.matchAll(/<\/?([a-zA-Z][\w-]*)>/g)) out.add(`<${m[1]}>`);
  return [...out].sort().join(",");
}

console.log("== 1-2. Key và placeholder giữa các locale");
for (const l of LOCALES) {
  if (l === BASE) continue;
  const keys = new Set(Object.keys(msgs[l]));
  const missing = baseKeys.filter((k) => !keys.has(k));
  const extra = [...keys].filter((k) => !(k in base));
  missing.forEach((k) => err(`${l}: thiếu key ${k}`));
  extra.forEach((k) => err(`${l}: key thừa ${k}`));
  for (const k of baseKeys) {
    if (!keys.has(k)) continue;
    const a = tokens(base[k]), b = tokens(msgs[l][k]);
    if (a !== b) err(`${l}: placeholder khác ở ${k}: vi=[${a}] ${l}=[${b}]`);
    if (!msgs[l][k].trim()) err(`${l}: chuỗi rỗng ${k}`);
  }
  console.log(`  ${l}: ${keys.size} key, thiếu ${missing.length}, thừa ${extra.length}`);
}

// ---- quét code ----
function walk(dir, acc = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) {
      if (["node_modules", ".next"].includes(e.name)) continue;
      walk(p, acc);
    } else if (/\.(tsx?|mjs)$/.test(e.name)) acc.push(p);
  }
  return acc;
}
const files = ["app", "components", "lib", "i18n"].flatMap((d) => walk(path.join(root, d)));

function stripComments(src) {
  // bỏ /* */ và // (không đụng tới "//" trong chuỗi URL: chỉ cắt khi trước nó là đầu dòng/khoảng trắng/;{}()
  return src
    .replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, " "))
    .replace(/\{\/\*[\s\S]*?\*\/\}/g, (m) => m.replace(/[^\n]/g, " "))
    .split("\n")
    .map((line) => line.replace(/(^|[\s;{}(),])\/\/.*$/, "$1"))
    .join("\n");
}

console.log("== 2b. Mọi message biên dịch được (ICU + rich text) ở cả 4 locale");
{
  let createTranslator = null;
  try {
    ({ createTranslator } = await import("use-intl/core"));
  } catch {
    try {
      ({ createTranslator } = await import("next-intl"));
    } catch {
      /* noop */
    }
  }
  if (!createTranslator) console.log("  (bỏ qua: không nạp được use-intl)");
  else {
    let n = 0;
    for (const l of LOCALES) {
      const raw = JSON.parse(fs.readFileSync(path.join(root, "messages", `${l}.json`), "utf8"));
      const errs = [];
      const t = createTranslator({ locale: l, messages: raw, onError: (e) => errs.push(e.message) });
      for (const k of Object.keys(msgs[l])) {
        const vals = {};
        for (const tok of tokens(msgs[l][k]).split(",").filter(Boolean)) {
          const name = tok.slice(1, -1);
          if (tok.startsWith("<")) vals[name] = (c) => c;
          else vals[name] = 2;
        }
        errs.length = 0;
        try {
          t.rich(k, vals);
        } catch (e) {
          errs.push(String(e?.message ?? e));
        }
        n++;
        for (const m of errs) err(`${l}: ${k} không biên dịch được: ${m.slice(0, 140)}`);
        const tags = [...msgs[l][k].matchAll(/<\/?([a-zA-Z][\w-]*)>/g)].map((m) => m[1]).filter((x) => !["mono", "b", "g"].includes(x));
        if (tags.length) err(`${l}: ${k} có thẻ lạ <${tags[0]}> (dấu < > sẽ bị hiểu là rich text)`);
      }
    }
    console.log(`  đã biên dịch ${n} message`);
  }
}

console.log("== 3. Key dùng trong code có trong vi.json");
let used = 0;
for (const f of files) {
  const whole = stripComments(fs.readFileSync(f, "utf8"));
  // Phân tích theo từng hàm cấp ngoài cùng: cùng tên biến `t` có thể trỏ namespace khác nhau ở mỗi hàm.
  const chunks = whole.split(/^(?=(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s)/m);
  for (const src of chunks) {
    const vars = new Map();
    for (const m of src.matchAll(/(?:const|let)\s+(\w+)\s*=\s*(?:await\s+)?(?:useTranslations|getTranslations)\(\s*"([\w.]+)"\s*\)/g)) vars.set(m[1], m[2]);
    for (const m of src.matchAll(/(?:const|let)\s+(\w+)\s*=\s*(?:await\s+)?getTranslations\(\s*\{[^}]*namespace:\s*"([\w.]+)"[^}]*\}\s*\)/g)) vars.set(m[1], m[2]);
    for (const [v, ns] of vars) {
      const re = new RegExp(`(?<![\\w.])${v}(?:\\.rich|\\.markup|\\.raw)?\\(\\s*"([\\w.]+)"`, "g");
      for (const m of src.matchAll(re)) {
        used++;
        const key = `${ns}.${m[1]}`;
        if (!(key in base) && !baseKeys.some((k) => k.startsWith(key + "."))) err(`${path.relative(root, f)}: key không tồn tại ${key}`);
      }
    }
  }
}
console.log(`  đã kiểm ${used} lời gọi t("…") tĩnh`);

console.log("== 4. Không còn chuỗi tiếng Việt cứng trong UI");
const VI = /[ăâđêôơưạảấầẩẫậắằẳẵặẹẻẽếềểễệỉịọỏốồổỗộớờởỡợụủứừửữựỳỵỷỹáàãéèíìóòõúùýĂÂĐÊÔƠƯ]/i;
let hard = 0;
for (const f of files) {
  const rel = path.relative(root, f);
  if (rel.startsWith(path.join("lib", "mock"))) continue; // dữ liệu giả lập (nội dung của agent), không phải nhãn UI
  const lines = stripComments(fs.readFileSync(f, "utf8")).split("\n");
  const raw = fs.readFileSync(f, "utf8").split("\n");
  lines.forEach((line, i) => {
    if (VI.test(line) && !raw[i].includes("i18n-ignore")) {
      hard++;
      err(`${rel}:${i + 1}: ${line.trim().slice(0, 110)}`);
    }
  });
}
console.log(`  ${hard} dòng còn tiếng Việt cứng`);

console.log(fail ? `I18N CHECK: ${fail} LỖI` : `I18N CHECK: OK (${baseKeys.length} key × ${LOCALES.length} locale)`);
process.exit(fail ? 1 : 0);
