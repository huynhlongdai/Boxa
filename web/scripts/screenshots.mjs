// Chụp màn hình các luồng chính (tuỳ chọn). Cần Chromium + playwright-core:
//   npm i -g playwright-core   (hoặc npx)  ·  CHROMIUM_PATH=/usr/bin/chromium BASE=http://localhost:3199 OUT=./docs/screenshots node scripts/screenshots.mjs
// Server phải chạy với BOXMES_MOCK=1.
const { chromium: pw } = await import("playwright-core").catch(() => import("playwright"));
const browser = await pw.launch({ executablePath: process.env.CHROMIUM_PATH || "/usr/bin/chromium", headless: true, args: ["--no-sandbox"] });
const base = process.env.BASE || "http://localhost:3199";
const out = process.env.OUT || "./docs/screenshots";
await import("node:fs").then((fs) => fs.mkdirSync(out, { recursive: true }));
async function ctx(w, h) { const c = await browser.newContext({ viewport: { width: w, height: h } }); const p = await c.newPage(); const errs=[]; p.on("pageerror", e=>errs.push(e.message)); return { c, p, errs }; }
// 1. Command palette + wake
{ const { c, p, errs } = await ctx(1366, 900);
  await p.goto(base + "/boxes", { waitUntil: "networkidle" }); await p.waitForTimeout(800);
  await p.keyboard.press("Control+k"); await p.waitForTimeout(500);
  await p.keyboard.type("wake "); await p.waitForTimeout(800);
  await p.screenshot({ path: `${out}/palette-wake.png` });
  await p.keyboard.press("Escape"); await p.waitForTimeout(300);
  await p.keyboard.press("Control+k"); await p.keyboard.type("kill all"); await p.waitForTimeout(400); await p.keyboard.press("Enter"); await p.waitForTimeout(500);
  await p.getByRole("button", { name: "Tiếp tục" }).click(); await p.waitForTimeout(300);
  await p.keyboard.type("DUNG"); await p.waitForTimeout(300);
  await p.screenshot({ path: `${out}/emergency-step2.png` });
  console.log("palette/emergency", errs.join("|") || "ok"); await c.close(); }
// 2. Agent wizard full flow
{ const { c, p, errs } = await ctx(1366, 900);
  await p.goto(base + "/agents", { waitUntil: "networkidle" }); await p.waitForTimeout(600);
  await p.getByRole("button", { name: "Tạo agent" }).click(); await p.waitForTimeout(400);
  await p.getByPlaceholder("vd: kecon-writer").fill("demo-wizard");
  await p.getByRole("button", { name: /Claude Code/ }).click();
  await p.getByRole("button", { name: "Tiếp" }).click(); await p.waitForTimeout(300);
  await p.screenshot({ path: `${out}/wizard-step2.png` });
  await p.getByRole("button", { name: /Tạo agent & key/ }).click(); await p.waitForTimeout(1500);
  await p.screenshot({ path: `${out}/wizard-step3.png` });
  console.log("wizard", errs.join("|") || "ok"); await c.close(); }
// 3. Terminal takeover
{ const { c, p, errs } = await ctx(1366, 900);
  await p.goto(base + "/boxes/box_a9d054?tab=terminal", { waitUntil: "networkidle" }); await p.waitForTimeout(1500);
  await p.getByRole("button", { name: "Chiếm quyền" }).click(); await p.waitForTimeout(800);
  await p.locator(".xterm-helper-textarea").focus();
  await p.keyboard.type("ls /work/stories"); await p.keyboard.press("Enter"); await p.waitForTimeout(800);
  await p.keyboard.type("echo xin chao"); await p.keyboard.press("Enter"); await p.waitForTimeout(800);
  await p.screenshot({ path: `${out}/terminal-takeover.png` });
  console.log("terminal", errs.join("|") || "ok"); await c.close(); }
// 4. Onboarding
{ const { c, p, errs } = await ctx(390, 844);
  await p.goto(base + "/onboarding", { waitUntil: "networkidle" }); await p.waitForTimeout(600);
  await p.getByPlaceholder("dán token 64 ký tự").fill("hz_test_token_1234567890abcdef");
  await p.getByRole("button", { name: /Kiểm tra/ }).click(); await p.waitForTimeout(1200);
  await p.getByRole("button", { name: /Tạo host/ }).click(); await p.waitForTimeout(3500);
  await p.screenshot({ path: `${out}/onboarding-host-390.png`, fullPage: true });
  console.log("onboarding", errs.join("|") || "ok"); await c.close(); }
// 5. Admin features + timeline + files
for (const [name, path, w, h] of [["admin-1366","/admin?s=features",1366,900],["admin-jobs-768","/admin?s=jobs",768,1024],["timeline-1366","/boxes/box_7f3a91?tab=timeline",1366,900],["files-1366","/boxes/box_7f3a91?tab=files",1366,900],["templates-1366","/templates",1366,900],["settings-390","/settings",390,844],["agent-detail-1366","/agents/agt_shopee",1366,900],["login-390","/login",390,844]]) {
  const { c, p, errs } = await ctx(w, h);
  await p.goto(base + path, { waitUntil: "networkidle" }); await p.waitForTimeout(1500);
  await p.screenshot({ path: `${out}/${name}.png`, fullPage: name.includes("settings") });
  console.log(name, errs.join("|") || "ok"); await c.close();
}
await browser.close();
