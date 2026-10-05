const { chromium } = require('/Users/mahmouddahdouh/.npm/_npx/9833c18b2d85bc59/node_modules/playwright');
(async () => {
  const browser = await chromium.launch({executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true});
  const ctx = await browser.newContext({viewport: {width: 1280, height: 800}});
  const page = await ctx.newPage();
  const base = 'https://dash.168.235.65.204.sslip.io';
  const out = {};
  await page.goto(base + '/login');
  // keyboard: tab order and visible focus on the login form
  await page.keyboard.press('Tab'); // skip link may take first
  const seq = [];
  for (let i = 0; i < 5; i++) {
    const info = await page.evaluate(() => { const e = document.activeElement; const cs = getComputedStyle(e); return {tag: e.tagName, name: e.name || e.textContent.trim().slice(0, 20), outline: cs.outlineStyle + ' ' + cs.outlineWidth, shadow: cs.boxShadow !== 'none'}; });
    seq.push(info); await page.keyboard.press('Tab');
  }
  out.tabOrder = seq;
  await page.fill('input[name=email]', 'owner@musdash.test'); await page.fill('input[name=password]', 'Owner-Pass-2026!x');
  await Promise.all([page.waitForNavigation(), page.keyboard.press('Enter')]);
  out.loggedIn = page.url();
  // cookies over HTTPS
  const cookies = await ctx.cookies(); out.cookies = cookies.map(c => ({n: c.name, secure: c.secure, httpOnly: c.httpOnly, sameSite: c.sameSite}));
  // HTMX / toast: change profile name
  await page.goto(base + '/account');
  const nameInput = page.locator('form[action="/account/profile"] input[name=name]');
  const old = await nameInput.inputValue();
  await nameInput.fill(old + ' X');
  await page.locator('form[action="/account/profile"] button[type=submit]').click();
  await page.waitForTimeout(600);
  out.toast = await page.locator('.notice').first().textContent().catch(() => null);
  const hasDismiss = await page.locator('.notice [data-dismiss]').count();
  out.toastDismissButtons = hasDismiss;
  await page.waitForTimeout(8000);
  out.toastAfter8s = await page.locator('.notice').count();
  // restore the name
  await page.locator('form[action="/account/profile"] input[name=name]').fill(old);
  await page.locator('form[action="/account/profile"] button[type=submit]').click(); await page.waitForTimeout(500);
  // dialogs: open the project delete dialog on a project settings page
  const projLink = await page.evaluate(async () => { const r = await fetch('/'); const t = await r.text(); const m = t.match(/href="(\/projects\/[a-z2-7]{12})"/); return m && m[1]; });
  await page.goto(base + projLink + '/settings');
  const openers = await page.locator('[data-open]').count(); out.openers = openers;
  if (openers) {
    const first = page.locator('[data-open]').last();
    await first.click(); await page.waitForTimeout(300);
    const dlgVisible = await page.locator('dialog[open], [role=dialog]:visible').count();
    out.dialogOpened = dlgVisible;
    await page.keyboard.press('Escape'); await page.waitForTimeout(300);
    out.dialogAfterEscape = await page.locator('dialog[open]').count();
    await first.click(); await page.waitForTimeout(300);
    const cancel = page.locator('dialog[open] [data-close]').first();
    if (await cancel.count()) { await cancel.click(); await page.waitForTimeout(300); }
    out.dialogAfterCancel = await page.locator('dialog[open]').count();
    // focus lands inside dialog?
    await first.click(); await page.waitForTimeout(300);
    out.focusInDialog = await page.evaluate(() => !!document.activeElement.closest('dialog'));
    await page.keyboard.press('Escape');
  }
  // nav toggle on mobile
  await page.setViewportSize({width: 375, height: 812}); await page.goto(base + '/');
  const toggle = page.locator('[data-nav-toggle]'); out.navToggle = await toggle.count();
  if (out.navToggle) { const before = await page.locator('nav a').first().isVisible(); await toggle.first().click(); await page.waitForTimeout(300); out.navVisibleAfterToggle = await page.locator('nav a').first().isVisible(); out.navVisibleBefore = before; }
  // lang and landmarks
  out.lang = await page.evaluate(() => document.documentElement.lang); out.landmarks = await page.evaluate(() => ({main: !!document.querySelector('main, [role=main]'), nav: !!document.querySelector('nav'), skip: !!document.querySelector('a[href="#content"]'), h1: document.querySelectorAll('h1').length}));
  // images without alt, inputs without labels on account page
  await page.goto(base + '/account');
  out.unlabeled = await page.evaluate(() => [...document.querySelectorAll('input:not([type=hidden]), select, textarea')].filter(e => !e.labels || !e.labels.length).map(e => e.name).slice(0, 10));
  await page.screenshot({path: '/Users/mahmouddahdouh/Desktop/Projects/musdash-go/docs/testing/screenshots/90_mobile_account.png'});
  console.log(JSON.stringify(out, null, 1));
  await browser.close();
})().catch(e => { console.error(e); process.exit(1); });
