const { chromium } = require('/Users/mahmouddahdouh/.npm/_npx/9833c18b2d85bc59/node_modules/playwright');
(async () => {
  const browser = await chromium.launch({executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true});
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  const fn = async (page) => {
  const fs = require('fs');
  const base = 'https://dash.168.235.65.204.sslip.io';
  const shots = '/Users/mahmouddahdouh/Desktop/Projects/musdash-go/docs/testing/screenshots';
  const pages = ["/", "/account", "/team", "/team/variables", "/settings", "/settings/notifications", "/settings/storages", "/sources", "/servers", "/servers/ttd6yamy3cmb/metrics", "/servers/ttd6yamy3cmb/variables", "/tags", "/tags/nightly", "/projects/cacxk3brx47t", "/projects/cacxk3brx47t/settings", "/projects/cacxk3brx47t/variables", "/projects/cacxk3brx47t/apps/new?env=cwaulzojjyso", "/projects/cacxk3brx47t/databases/new?env=cwaulzojjyso", "/projects/cacxk3brx47t/services/new?env=cwaulzojjyso", "/projects/new", "/apps/cmauyp7b5mbe", "/apps/cmauyp7b5mbe/deployments", "/apps/cmauyp7b5mbe/logs", "/apps/cmauyp7b5mbe/metrics", "/apps/cmauyp7b5mbe/terminal", "/apps/cmauyp7b5mbe/environment", "/apps/cmauyp7b5mbe/storage", "/apps/cmauyp7b5mbe/tasks", "/apps/cmauyp7b5mbe/settings", "/apps/cmauyp7b5mbe/deployments/guifuw7j5sac", "/databases/qfoenpmqqgvg", "/databases/qfoenpmqqgvg/backups", "/databases/qfoenpmqqgvg/logs", "/databases/qfoenpmqqgvg/metrics", "/databases/qfoenpmqqgvg/terminal", "/databases/qfoenpmqqgvg/settings", "/databases/amwb733wuprp", "/databases/amwb733wuprp/backups", "/databases/amwb733wuprp/logs", "/databases/amwb733wuprp/metrics", "/databases/amwb733wuprp/terminal", "/databases/amwb733wuprp/settings", "/services/krfmn5o265pd", "/services/krfmn5o265pd/compose", "/services/krfmn5o265pd/logs", "/services/krfmn5o265pd/metrics", "/services/krfmn5o265pd/terminal", "/services/krfmn5o265pd/settings", "/services/szh5dhja2rlj", "/services/szh5dhja2rlj/compose", "/services/szh5dhja2rlj/logs", "/services/szh5dhja2rlj/metrics", "/services/szh5dhja2rlj/terminal", "/services/szh5dhja2rlj/settings"];
  const out = [];
  await page.setViewportSize({width: 1280, height: 800});
  await page.goto(base + '/login');
  await page.fill('input[name=email]', 'owner@musdash.test');
  await page.fill('input[name=password]', 'Owner-Pass-2026!x');
  await Promise.all([page.waitForNavigation(), page.click('button[type=submit]')]);
  const errs = []; const failed = []; const hosts = new Set();
  page.on('console', m => { if (m.type() === 'error' || m.type() === 'warning') errs.push(m.type() + ': ' + m.text().slice(0, 200)); });
  page.on('pageerror', e => errs.push('pageerror: ' + String(e).slice(0, 200)));
  page.on('requestfailed', r => failed.push(r.url().slice(0, 120) + ' ' + (r.failure() || {}).errorText));
  page.on('request', r => { try { const h = new URL(r.url()).host; if (h !== 'dash.168.235.65.204.sslip.io') hosts.add(h); } catch (e) {} });
  let i = 0;
  for (const p of pages) {
    errs.length = 0; failed.length = 0;
    let status = 0, title = '', ov = false, ov375 = false, h1 = '';
    try {
      const r = await page.goto(base + p, {waitUntil: 'networkidle', timeout: 30000});
      status = r ? r.status() : 0;
      title = await page.title();
      h1 = await page.evaluate(() => (document.querySelector('h1') || {}).textContent || '');
      ov = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1);
      const name = p.replace(/[^a-z0-9]+/gi, '_').replace(/^_|_$/g, '') || 'home';
      if (i < 200) await page.screenshot({path: shots + '/' + String(i).padStart(2, '0') + '_' + name.slice(0, 50) + '.png', fullPage: false});
      await page.setViewportSize({width: 375, height: 812});
      await page.waitForTimeout(150);
      ov375 = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1);
      await page.setViewportSize({width: 1280, height: 800});
    } catch (e) { errs.push('nav: ' + String(e).slice(0, 150)); }
    out.push({p, status, title, h1: h1.trim().slice(0, 40), ov, ov375, errs: [...errs], failed: [...failed]});
    i++;
  }
  fs.writeFileSync('/Users/mahmouddahdouh/Desktop/Projects/musdash-go/test/vps/out/ui_sweep.json', JSON.stringify({out, hosts: [...hosts]}, null, 1));
  return {n: out.length, bad: out.filter(o => o.status !== 200 || o.errs.length || o.failed.length || o.ov || o.ov375).map(o => ({p: o.p, s: o.status, e: o.errs.slice(0, 2), f: o.failed.slice(0, 2), ov: o.ov, ov375: o.ov375})), hosts: [...hosts]};
};
  const res = await fn(page);
  console.log(JSON.stringify(res, null, 1));
  await browser.close();
})().catch(e => { console.error(e); process.exit(1); });
