// Website-independent browsing preference. Keep ordinary reading in the
// current Page without replaying clicks or replacing Chromium's navigation.
// This function must remain self-contained: Playwright serializes it into
// every owned document, including cross-origin frames.
export function installLinkNavigationPolicy() {
  const installed = Symbol.for('pogo.current-sheet-links.v1');
  if (window[installed]) return;
  Object.defineProperty(window, installed, { value: true });
  const add = EventTarget.prototype.addEventListener;
  const remove = EventTarget.prototype.removeEventListener;
  const get = Element.prototype.getAttribute;
  const has = Element.prototype.hasAttribute;
  const set = Element.prototype.setAttribute;
  const unset = Element.prototype.removeAttribute;
  const later = window.setTimeout.bind(window);
  const functionalPath = /(?:^|[\/_\-.])(auth|oauth2?|authorize|authorization|authenticate|authentication|login|log-in|signin|sign-in|signup|sign-up|logout|sso|saml|openid|consent|callback|checkout|payment|payments|pay)(?:$|[\/_\-.])/i;
  const functionalLabel = /\b(?:log[ -]?in|sign[ -]?in|sign[ -]?up|authenticate|authorize|authorization|oauth|sso|saml|checkout|payment|pay)\b|登录|登入|登錄|授权|授權|认证|認證|支付|付款/i;
  const explicitNewLabel = /\bopens?\b.{0,24}\bnew (?:tab|window)\b|(?:新|另)(?:开|開|建)?(?:标签页|標籤頁|窗口|視窗)|另[开開]/i;
  const functionalParameters = /^(?:client_id|redirect_uri|response_type|code_challenge|code_challenge_method|samlrequest|samlresponse|relaystate|openid\..*|oauth_token)$/i;
  const attribute = (element, name) => get.call(element, name);
  const blankAnchor = event => {
    const anchor = event.composedPath().find(element => element instanceof HTMLAnchorElement);
    if (!anchor) return null;
    const target = attribute(anchor, 'target') ?? document.querySelector('base[target]')?.getAttribute('target') ?? '';
    return target.toLowerCase() === '_blank' ? anchor : null;
  };
  const candidate = event => {
    const anchor = blankAnchor(event);
    if (!anchor || anchor.isContentEditable || has.call(anchor, 'download')) return null;
    const rel = (attribute(anchor, 'rel') ?? '').toLowerCase().split(/\s+/);
    if (rel.includes('opener') || attribute(anchor, 'role') === 'button' || has.call(anchor, 'aria-haspopup') ||
      ['data-toggle', 'data-bs-toggle'].some(name => ['modal', 'dropdown'].includes(attribute(anchor, name)))) return null;
    let url;
    try { url = new URL(anchor.href, document.baseURI); } catch { return null; }
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) return null;
    const label = [attribute(anchor, 'aria-label'), attribute(anchor, 'title'), anchor.textContent?.slice(0, 256)].join(' ');
    let path;
    try { path = decodeURIComponent(url.pathname + '/' + url.hash.slice(1).split('?')[0]); } catch { path = url.pathname; }
    if (functionalPath.test(path) || functionalLabel.test(label) || explicitNewLabel.test(label) || [...url.searchParams.keys()].some(key => functionalParameters.test(key))) return null;
    return anchor;
  };
  add.call(window, 'click', event => {
    // Chromium pointer input (including Agent CDP clicks) is trusted. Leave
    // website-script clicks, modifier clicks, middle/right clicks and native
    // context-menu "open in new tab" behavior untouched.
    if (!event.isTrusted || event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
    if (!blankAnchor(event)) return;
    const finish = dispatched => {
      if (dispatched !== event) return;
      remove.call(window, 'click', finish);
      if (event.defaultPrevented) return;
      const anchor = candidate(event);
      if (!anchor) return;
      const original = attribute(anchor, 'target');
      set.call(anchor, 'target', '_self');
      // Native link activation runs before the next task. Never overwrite a
      // target changed by the website after its own event handling.
      later(() => {
        if (attribute(anchor, 'target') !== '_self') return;
        if (original === null) unset.call(anchor, 'target');
        else set.call(anchor, 'target', original);
      }, 0);
    };
    // Observe early, apply late: target/document handlers see the original
    // link and can prevent or replace navigation. Stopped propagation keeps
    // the site's behavior rather than bypassing its handlers.
    add.call(window, 'click', finish);
    later(() => remove.call(window, 'click', finish), 0);
  }, true);
}

// Existing Electron/CDP pages can predate scoped registration. Future frames
// receive the same idempotent init script; existing frames get it now.
export async function installLinkNavigationOnPage(page) {
  await page.addInitScript(installLinkNavigationPolicy);
  await Promise.all(page.frames().map(frame => frame.evaluate(installLinkNavigationPolicy).catch(() => {})));
}
