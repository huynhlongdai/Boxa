#!/usr/bin/env node
// Kiểm tra tràn layout theo ngôn ngữ (tuỳ chọn, cần Chromium + playwright-core):
//   BASE=http://localhost:3199 CHROMIUM_PATH=… node scripts/overflow-check.mjs
// Với mỗi locale × route × viewport (390×844, 1366×900, 1600×900): báo phần tử thò ra ngoài viewport (không nằm trong vùng cuộn
// ngang có chủ đích như bảng/tab/code) và trang có cuộn ngang. Thêm vài hộp thoại (tạo box, wizard agent, ⌘K, sửa egress).
// Tiêu đề (h1–h4) xuống dòng mà dòng cuối chỉ còn 1 từ (vi/en) bị báo là "mồ côi".
const pwPath = process.env.PLAYWRIGHT_CORE || "playwright-core";
const { chromium } = await import(pwPath).catch(() => import("playwright"));
const base = process.env.BASE || "http://localhost:3199";
const locales = (process.env.LOCALES || "vi,en,zh,ja").split(",");
const routes = (process.env.ROUTES || "/,/boxes,/boxes/box_7f3a91,/boxes/box_7f3a91?tab=timeline,/boxes/box_7f3a91?tab=logs,/boxes/box_7f3a91?tab=snapshots,/boxes/box_7f3a91?tab=config,/recommend,/agents,/agents/agt_kecon,/costs,/infra,/infra?s=health,/infra?s=catalog,/infra?s=speed,/infra?s=mesh,/templates,/secrets,/settings,/admin,/admin?s=quotas,/admin?s=jobs,/developers,/onboarding,/login").split(",");
const viewports = (process.env.VIEWPORTS || "390x844,1366x900,1600x900").split(",").map((v) => {
  const [width, height] = v.split("x").map(Number);
  return { width, height };
});
const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH || undefined, headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage", "--disable-gpu"] });

const probe = () => {
  const vw = document.documentElement.clientWidth;
  const out = [];
  const scrollable = (el) => {
    for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
      const cs = getComputedStyle(p);
      if (["auto", "scroll", "hidden", "clip"].includes(cs.overflowX)) return true;
      if (cs.position === "fixed") return false;
    }
    return false;
  };
  for (const el of document.querySelectorAll("body *")) {
    const r = el.getBoundingClientRect();
    if (!r.width || !r.height) continue;
    const cs = getComputedStyle(el);
    if (cs.visibility === "hidden" || cs.display === "none" || el.closest("[aria-hidden=true],.xterm,.recharts-wrapper,[data-sonner-toaster]")) continue;
    if ((r.right > vw + 1 || r.left < -1) && !scrollable(el)) {
      const txt = (el.innerText || el.getAttribute("aria-label") || el.tagName).trim().slice(0, 50).replace(/\s+/g, " ");
      out.push(`${el.tagName.toLowerCase()}${el.className && typeof el.className === "string" ? "." + el.className.split(" ").slice(0, 2).join(".") : ""} [${Math.round(r.left)}→${Math.round(r.right)} > ${vw}] "${txt}"`);
    }
  }
  // Tiêu đề mồ côi: dòng cuối chỉ có 1 từ (bỏ qua zh/ja, không tách từ bằng khoảng trắng)
  const orphans = [];
  if (!/^(zh|ja)/.test(document.documentElement.lang)) {
    for (const h of document.querySelectorAll("h1, h2, h3, h4")) {
      const r = h.getBoundingClientRect();
      if (!r.width || !r.height || h.closest("[aria-hidden=true]")) continue;
      const lh = parseFloat(getComputedStyle(h).lineHeight) || 20;
      if (r.height < lh * 1.5) continue;
      const tops = [];
      const walker = document.createTreeWalker(h, NodeFilter.SHOW_TEXT);
      for (let n = walker.nextNode(); n; n = walker.nextNode()) {
        const re = /\S+/g;
        let m;
        while ((m = re.exec(n.textContent))) {
          const rg = document.createRange();
          rg.setStart(n, m.index);
          rg.setEnd(n, m.index + m[0].length);
          const rr = rg.getClientRects()[0];
          if (rr) tops.push(Math.round(rr.top));
        }
      }
      if (tops.length < 3) continue;
      const last = tops[tops.length - 1];
      if (tops.filter((t) => Math.abs(t - last) < 3).length === 1) orphans.push(`"${h.innerText.replace(/\s+/g, " ").slice(0, 60)}"`);
    }
  }
  const pageScroll = document.documentElement.scrollWidth > vw + 1;
  // chỉ giữ phần tử ngoài cùng (bỏ con của phần tử đã báo)
  return { pageScroll, scrollWidth: document.documentElement.scrollWidth, vw, items: out.slice(0, 6), count: out.length, orphans };
};

let problems = 0;
let checked = 0;
for (const locale of locales) {
  for (const vp of viewports) {
    const ctx = await browser.newContext({ viewport: vp, locale });
    await ctx.addCookies([{ name: "boxa_locale", value: locale, url: base }]);
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    const t0 = Date.now();
    const errs = [];
    page.on("pageerror", (e) => errs.push(e.message));
    for (const r of routes) {
      // SSE /stream giữ kết nối mở nên không dùng networkidle
      await page.goto(base + r, { waitUntil: "load", timeout: 30000 }).catch(() => {});
      await page.waitForTimeout(1300);
      const res = await page.evaluate(probe);
      checked++;
      if (res.pageScroll || res.count) {
        problems++;
        console.log(`✗ [${locale} ${vp.width}] ${r}: pageScroll=${res.pageScroll} (${res.scrollWidth}/${res.vw}) ${res.count} phần tử tràn`);
        for (const i of res.items) console.log("     " + i);
      }
      if (res.orphans.length) {
        problems++;
        console.log(`✗ [${locale} ${vp.width}] ${r}: tiêu đề mồ côi ${res.orphans.join(", ")}`);
      }
    }
    // Hộp thoại: tạo box (+ báo giá), wizard agent, bảng lệnh
    const dialogs = [
      // v1.3: tạo box theo nhu cầu (mặc định) → xếp hạng; chế độ tự chọn → báo giá
      ["/boxes", async () => { await page.locator("main button:has(svg.lucide-plus)").first().click(); await page.waitForTimeout(600); await page.locator("[role=dialog] select").first().selectOption({ index: 1 }); await page.locator("[role=dialog] form button[type=submit]").click(); await page.waitForTimeout(1200); }],
      ["/boxes", async () => { await page.locator("main button:has(svg.lucide-plus)").first().click(); await page.waitForTimeout(400); await page.locator("[role=dialog] [role=tab][aria-selected=false]").click(); await page.waitForTimeout(300); await page.locator("[role=dialog] select").first().selectOption({ index: 1 }); await page.locator("[role=dialog] button:has(svg.lucide-calculator)").click(); await page.waitForTimeout(800); }],
      ["/recommend", async () => { await page.locator("main form button[type=submit]").click(); await page.waitForTimeout(1200); await page.locator("[data-rec-rejected] summary").click().catch(() => {}); await page.waitForTimeout(300); }],
      ["/infra", async () => { await page.locator("[data-open-wizard]").click(); await page.waitForTimeout(400); await page.locator("[data-connect-wizard] [aria-pressed]").nth(2).click(); await page.locator("[data-connect-wizard] button:not([aria-label])").last().click(); await page.waitForTimeout(300); await page.locator("[data-connect-wizard] input").fill("dop_v1_0123456789abcdef0123"); await page.locator("[data-connect-wizard] button:not([aria-label])").last().click(); await page.waitForTimeout(1200); }],
      ["/infra", async () => { await page.locator("[data-open-wizard]").click(); await page.waitForTimeout(400); await page.locator("[data-connect-wizard] [aria-pressed]").first().click(); await page.locator("[data-connect-wizard] button:not([aria-label])").last().click(); await page.waitForTimeout(300); await page.locator("[data-connect-wizard] input").fill("hcloud_0123456789abcdef0123"); await page.locator("[data-connect-wizard] button:not([aria-label])").last().click(); await page.waitForTimeout(1200); await page.locator("[data-connect-wizard] button:not([aria-label])").last().click(); await page.waitForTimeout(500); }],
      ["/infra?s=health", async () => { await page.locator("[data-provider-health] button[aria-expanded]").first().click(); await page.waitForTimeout(400); }],
      ["/infra?s=mesh", async () => { await page.locator("[data-mesh-key-open]").click(); await page.waitForTimeout(400); await page.locator("[role=dialog] button:not([aria-label])").last().click(); await page.waitForTimeout(900); }],
      ["/agents", async () => { await page.locator("main button:has(svg.lucide-plus)").first().click(); await page.waitForTimeout(600); }],
      ["/boxes", async () => { await page.keyboard.press("Control+k"); await page.waitForTimeout(500); await page.keyboard.type("wake "); await page.waitForTimeout(500); }],
      ["/boxes/box_7f3a91", async () => { await page.locator("main button:has(svg.lucide-pencil)").first().click(); await page.waitForTimeout(500); }],
    ];
    for (const [r, act] of dialogs) {
      await page.goto(base + r, { waitUntil: "load" }).catch(() => {});
      await page.waitForTimeout(1300);
      try {
        await act();
        const res = await page.evaluate(probe);
        checked++;
        if (res.pageScroll || res.count || res.orphans.length) {
          problems++;
          console.log(`✗ [${locale} ${vp.width}] dialog@${r}: ${res.count} phần tử tràn ${res.orphans.join(", ")}`);
          for (const i of res.items) console.log("     " + i);
        }
      } catch (e) {
        console.log(`! [${locale} ${vp.width}] dialog@${r}: ${String(e.message).split("\n")[0]}`);
      }
      await page.keyboard.press("Escape").catch(() => {});
    }
    if (errs.length) {
      problems++;
      console.log(`✗ [${locale} ${vp.width}] lỗi JS: ${[...new Set(errs)].slice(0, 3).join(" | ")}`);
    }
    console.log(`· ${locale} ${vp.width}px xong (${Math.round((Date.now() - t0) / 1000)} s)`);
    await ctx.close();
  }
}
await browser.close();
console.log(problems ? `OVERFLOW: ${problems}/${checked} màn hình có vấn đề` : `OVERFLOW: OK (${checked} màn hình × locale × viewport)`);
process.exit(problems ? 1 : 0);
