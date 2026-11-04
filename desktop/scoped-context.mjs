import { EventEmitter } from 'node:events';

const PAGE_EVENTS = ['request', 'response', 'requestfinished', 'requestfailed'];

// An automation scope over visible Electron pages, not a second browser or a
// Playwright BrowserContext owner. The main-process host must register only
// pages from its own Electron partition and destroy its own Views separately.
export class ScopedContext extends EventEmitter {
  constructor({ browserContext, electronSession }) {
    super();
    if (typeof browserContext?.newCDPSession !== 'function' || typeof electronSession?.cookies?.get !== 'function') {
      throw new Error('ScopedContext requires a CDP browser context and an owned Electron session');
    }
    this.browserContext = browserContext;
    this.electronSession = electronSession;
    this.entries = new Map();
    this.scripts = [];
    this.closed = false;
  }

  assertLive() { if (this.closed) throw new Error('scoped browser context is closed'); }
  pages() { return [...this.entries.keys()]; }

  async applyScripts(entry) {
    if (!entry.scriptsFlight) {
      const flight = (async () => {
        while (entry.appliedScripts < this.scripts.length) {
          this.assertRegistered(entry);
          await entry.page.addInitScript(...this.scripts[entry.appliedScripts]);
          entry.appliedScripts++;
        }
      })();
      entry.scriptsFlight = flight;
      flight.finally(() => { if (entry.scriptsFlight === flight) entry.scriptsFlight = null; }).catch(() => {});
    }
    await entry.scriptsFlight;
    this.assertRegistered(entry);
    // A script can be appended while the previous flight is settling.
    if (entry.appliedScripts < this.scripts.length) await this.applyScripts(entry);
  }

  assertRegistered(entry) {
    this.assertLive();
    if (this.entries.get(entry.page) !== entry || entry.page.isClosed()) throw new Error('page is no longer registered in this browser scope');
  }

  register(page) {
    this.assertLive();
    const existing = this.entries.get(page);
    if (existing) return existing.ready;
    if (!page || typeof page.addInitScript !== 'function' || typeof page.isClosed !== 'function' || page.isClosed()) {
      return Promise.reject(new Error('cannot register an unavailable browser page'));
    }
    const entry = { page, listeners: [], appliedScripts: 0, scriptsFlight: null, ready: null };
    this.entries.set(page, entry);
    for (const event of PAGE_EVENTS) {
      const listener = value => { if (!this.closed && this.entries.get(page) === entry) this.emit(event, value); };
      page.on(event, listener); entry.listeners.push([event, listener]);
    }
    const onClose = () => this.unregister(page);
    page.on('close', onClose); entry.listeners.push(['close', onClose]);
    entry.ready = (async () => {
      try {
        if (this.defaultTimeout !== undefined) page.setDefaultTimeout(this.defaultTimeout);
        if (this.defaultNavigationTimeout !== undefined) page.setDefaultNavigationTimeout(this.defaultNavigationTimeout);
        await this.applyScripts(entry);
        this.assertRegistered(entry);
        this.emit('page', page);
        return page;
      } catch (error) {
        if (this.entries.get(page) === entry) this.unregister(page);
        throw error;
      }
    })();
    return entry.ready;
  }

  unregister(page) {
    const entry = this.entries.get(page);
    if (!entry) return;
    this.entries.delete(page);
    for (const [event, listener] of entry.listeners) page.off(event, listener);
  }

  async addInitScript(script, arg) {
    this.assertLive();
    this.scripts.push([script, arg]);
    await Promise.all([...this.entries.values()].map(entry => this.applyScripts(entry)));
  }

  setDefaultTimeout(timeout) {
    this.assertLive(); this.defaultTimeout = timeout;
    for (const page of this.entries.keys()) page.setDefaultTimeout(timeout);
  }

  setDefaultNavigationTimeout(timeout) {
    this.assertLive(); this.defaultNavigationTimeout = timeout;
    for (const page of this.entries.keys()) page.setDefaultNavigationTimeout(timeout);
  }

  async newCDPSession(target) {
    this.assertLive();
    const page = this.entries.has(target) ? target : typeof target?.page === 'function' ? target.page() : null;
    const entry = this.entries.get(page);
    if (!entry) throw new Error('CDP target is outside this browser scope');
    this.assertRegistered(entry);
    // Electron partitions need not share the first Playwright context. Resolve
    // through the already-owned Page, never through a guessed global context.
    const client = await page.context().newCDPSession(target);
    try { this.assertRegistered(entry); }
    catch (error) { await client.detach().catch(() => {}); throw error; }
    return client;
  }

  async cookies() {
    this.assertLive();
    const cookies = await this.electronSession.cookies.get({});
    this.assertLive();
    return cookies.map(cookie => ({
      name: cookie.name, value: cookie.value, domain: cookie.domain ?? '', path: cookie.path ?? '/',
      expires: cookie.session || !Number.isFinite(cookie.expirationDate) ? -1 : cookie.expirationDate,
      httpOnly: cookie.httpOnly === true, secure: cookie.secure === true,
      sameSite: { strict: 'Strict', lax: 'Lax', no_restriction: 'None', unspecified: 'Lax' }[cookie.sameSite] ?? 'Lax',
    }));
  }

  async close() {
    if (this.closed) return;
    this.closed = true;
    for (const page of this.pages()) this.unregister(page);
    this.scripts.length = 0;
    this.emit('close');
  }
}
