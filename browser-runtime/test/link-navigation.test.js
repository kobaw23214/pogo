import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { chromium } from 'playwright';
import { installLinkNavigationPolicy, installLinkNavigationOnPage } from '../src/link-navigation.js';

let browser, site, origin;
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
before(async () => {
  site = http.createServer((request, response) => {
    response.writeHead(200, { 'Content-Type': 'text/html;charset=utf-8' });
    if (request.url.startsWith('/fixture')) {
      response.end(`<!doctype html><title>Reading fixture</title><style>a,button{display:block;margin:10px;width:350px;height:28px}</style>
        <a id=plain href='/destination?plain'>normal link</a>
        <a id=blank target=_blank href='/destination?blank'><span>Read article</span></a>
        <a id=handled target=_blank href='/destination?handled' onclick="window.observedTarget=this.target;sessionStorage.setItem('observedTarget',this.target)">handled article</a>
        <a id=prevented target=_blank href='/destination?prevented' onclick="event.preventDefault();window.preventedTarget=this.target">prevented article</a>
        <a id=changed target=_blank href='/destination?changed' onclick="event.preventDefault();this.target='named-window'">changed article</a>
        <a id=stopped target=_blank href='/destination?stopped' onclick="event.stopPropagation()">stopped article</a>
        <a id=named target=article-window href='/destination?named'>named article</a>
        <a id=authpath target=_blank href='/oauth/authorize'>Connect account</a>
        <a id=authquery target=_blank href='/destination?client_id=123&redirect_uri=http://example.invalid'>Connect</a>
        <a id=authlabel target=_blank href='/destination?authlabel'>登录授权</a>
        <a id=authhash target=_blank href='/destination#/authorize'>Connect</a>
        <a id=payment target=_blank href='/checkout'>Complete order</a>
        <a id=explicit target=_blank href='/destination?explicit' aria-label='Open in new tab'>Read article</a>
        <a id=opener target=_blank rel=opener href='/destination?opener'>opener article</a>
        <a id=dialog target=_blank aria-haspopup=dialog href='/destination?dialog'>Connect</a>
        <a id=download download=article.html target=_blank href='/destination?download'>Download article</a>
        <button id=popup onclick="const w=window.open('/destination?script','_blank','popup,width=420,height=400');w.name='functional';">Functional popup</button>
        <button id=blankpopup onclick="const w=window.open('about:blank','_blank');w.document.write('<title>Written popup</title><p>opener</p>');w.payload='native';">Written popup</button>
        <form target=_blank action='/submitted' method=POST><input name=payload value=exact-native-post><button id=form>Submit form</button></form>
        <iframe id=frame src='/frame'></iframe><div id=shadow></div>
        <script>shadow.attachShadow({mode:'open'}).innerHTML='<a id=shadowlink target=_blank href="/destination?shadow">Shadow article</a>';</script>`);
    } else if (request.url === '/frame') {
      response.end(`<a id=framelink target=_blank href='/destination?frame'>Frame article</a>`);
    } else if (request.url === '/base') {
      response.end(`<base target=_blank><a id=base href='/destination?base'>Base target article</a>`);
    } else {
      let body = '';
      request.on('data', chunk => { body += chunk; });
      request.on('end', () => response.end(`<!doctype html><title>Destination</title><p id=request>${request.method} ${request.url} ${body}</p><script>window.hasOpener=!!window.opener;</script>`));
    }
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  origin = `http://127.0.0.1:${site.address().port}`;
  browser = await chromium.launch({ headless: true });
});
after(async () => { await browser?.close(); site?.closeAllConnections(); if (site) await new Promise(resolve => site.close(resolve)); });

async function fixture(t, path = '/fixture', install = true) {
  const context = await browser.newContext();
  t.after(() => context.close());
  if (install) await context.addInitScript(installLinkNavigationPolicy);
  const page = await context.newPage();
  await page.goto(origin + path);
  return { context, page };
}

test('ordinary and simple target=_blank reading stay on the same real Page with native Back history', async t => {
  for (const id of ['plain', 'blank', 'handled']) {
    const { context, page } = await fixture(t);
    await page.locator('#' + id).click();
    await page.waitForURL(origin + '/destination?' + id);
    assert.equal(context.pages().length, 1, id + ' does not open a Page');
    if (id === 'handled') assert.equal(await page.evaluate(() => sessionStorage.getItem('observedTarget')), '_blank', 'site handler sees original target');
    await page.goBack();
    assert.equal(page.url(), origin + '/fixture');
    assert.equal(await page.locator('#' + id).getAttribute('target'), id === 'plain' ? null : '_blank', 'original target survives Back');
  }
});

test('prevented and changed click handlers keep their behavior and their target attributes', async t => {
  const { context, page } = await fixture(t);
  await page.locator('#prevented').click();
  await pause(40);
  assert.equal(page.url(), origin + '/fixture'); assert.equal(context.pages().length, 1);
  assert.equal(await page.evaluate(() => window.preventedTarget), '_blank');
  assert.equal(await page.locator('#prevented').getAttribute('target'), '_blank');
  await page.locator('#changed').click();
  assert.equal(await page.locator('#changed').getAttribute('target'), 'named-window');
});

test('explicit modifier and middle clicks keep opening separate Pages', async t => {
  for (const [id, options] of [['plain', { modifiers: ['ControlOrMeta'] }], ['blank', { button: 'middle' }], ['blank', { modifiers: ['Shift'] }]]) {
    const { context, page } = await fixture(t);
    const opened = context.waitForEvent('page');
    await page.locator('#' + id).click(options);
    const child = await opened; await child.waitForLoadState();
    assert.equal(page.url(), origin + '/fixture');
    assert.equal(context.pages().length, 2);
    assert.equal(child.url(), origin + '/destination?' + id);
  }
});

test('named targets, authentication, payment, opener and dialog links preserve native new-page intent', async t => {
  for (const id of ['named', 'authpath', 'authquery', 'authlabel', 'authhash', 'payment', 'explicit', 'opener', 'dialog', 'stopped']) {
    const { context, page } = await fixture(t);
    const opened = context.waitForEvent('page'); await page.locator('#' + id).click();
    const child = await opened; await child.waitForLoadState();
    assert.equal(page.url(), origin + '/fixture', id + ' preserves opener');
    assert.equal(context.pages().length, 2);
    if (id === 'opener' || id === 'named') assert.equal(await child.evaluate(() => window.hasOpener), true);
  }
});

test('functional window.open retains opener, synchronous document writes and targeted POST form payload', async t => {
  const { context, page } = await fixture(t);
  let opened = context.waitForEvent('page'); await page.locator('#popup').click();
  const popup = await opened; await popup.waitForLoadState();
  assert.deepEqual(await popup.evaluate(() => ({ opener: !!window.opener, name: window.name })), { opener: true, name: 'functional' });
  opened = context.waitForEvent('page'); await page.locator('#blankpopup').click();
  const written = await opened;
  assert.deepEqual(await written.evaluate(() => ({ opener: !!window.opener, payload: window.payload, title: document.title })), { opener: true, payload: 'native', title: 'Written popup' });
  opened = context.waitForEvent('page'); await page.locator('#form').click();
  const submitted = await opened; await submitted.waitForLoadState();
  assert.equal(await submitted.locator('#request').textContent(), 'POST /submitted payload=exact-native-post');
  assert.equal(page.url(), origin + '/fixture');
});

test('downloads and website-script clicks retain their native behavior', async t => {
  const { context, page } = await fixture(t);
  const downloading = page.waitForEvent('download'); await page.locator('#download').click();
  const download = await downloading;
  assert.equal(download.suggestedFilename(), 'article.html'); assert.equal(context.pages().length, 1);
  assert.equal(page.url(), origin + '/fixture');
  const opened = context.waitForEvent('page');
  await page.locator('#blank').evaluate(anchor => anchor.click());
  const child = await opened; await child.waitForLoadState();
  assert.equal(child.url(), origin + '/destination?blank'); assert.equal(page.url(), origin + '/fixture');
});

test('base target, shadow anchor and frame anchor use native current context without creating another Page', async t => {
  const base = await fixture(t, '/base'); await base.page.locator('#base').click();
  await base.page.waitForURL(origin + '/destination?base'); assert.equal(base.context.pages().length, 1);
  const shadow = await fixture(t); await shadow.page.locator('#shadowlink').click();
  await shadow.page.waitForURL(origin + '/destination?shadow'); assert.equal(shadow.context.pages().length, 1);
  const framed = await fixture(t); const frame = framed.page.frameLocator('#frame');
  await frame.locator('#framelink').click();
  await framed.page.frames()[1].waitForURL(origin + '/destination?frame');
  assert.equal(framed.page.url(), origin + '/fixture'); assert.equal(framed.context.pages().length, 1);
});

test('existing pages install once and future documents keep the browsing policy', async t => {
  const { context, page } = await fixture(t, '/fixture', false);
  await installLinkNavigationOnPage(page); await installLinkNavigationOnPage(page);
  await page.locator('#blank').click(); await page.waitForURL(origin + '/destination?blank');
  assert.equal(context.pages().length, 1);
  await page.goto(origin + '/fixture'); await page.locator('#blank').click();
  await page.waitForURL(origin + '/destination?blank'); assert.equal(context.pages().length, 1);
});

test('existing cross-origin frame documents receive the same scoped policy', async t => {
  const crossSite = http.createServer((_request, response) => {
    response.writeHead(200, { 'Content-Type': 'text/html;charset=utf-8' });
    response.end(`<a id=cross target=_blank href='${origin}/destination?cross'>Cross-origin article</a>`);
  });
  await new Promise(resolve => crossSite.listen(0, '127.0.0.1', resolve));
  t.after(async () => { crossSite.closeAllConnections(); await new Promise(resolve => crossSite.close(resolve)); });
  const { context, page } = await fixture(t, '/fixture', false);
  const crossOrigin = `http://127.0.0.1:${crossSite.address().port}`;
  const navigated = page.waitForEvent('framenavigated', { predicate: frame => frame.url() === crossOrigin + '/' });
  await page.locator('#frame').evaluate((element, url) => { element.src = url; }, crossOrigin);
  const frame = await navigated;
  await frame.waitForLoadState(); await installLinkNavigationOnPage(page);
  await frame.locator('#cross').click(); await frame.waitForURL(origin + '/destination?cross');
  assert.equal(page.url(), origin + '/fixture'); assert.equal(context.pages().length, 1);
});
