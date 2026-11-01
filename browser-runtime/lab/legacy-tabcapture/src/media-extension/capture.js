// This page stays in the isolated BrowserContext. It grants a one-use native
// tab stream to the exact trusted media renderer, never by URL/title matching.
globalThis.pogoCapture = Object.freeze({
  async streamId(targetId, consumerTargetId) {
    const targets = await chrome.debugger.getTargets();
    const target = targets.find(item => item.id === targetId && item.type === 'page');
    const consumer = targets.find(item => item.id === consumerTargetId && item.type === 'page');
    if (!Number.isSafeInteger(target?.tabId) || !Number.isSafeInteger(consumer?.tabId) || target.id === consumer.id) throw new Error('media targets are not in this browser context');
    if (target.url?.startsWith('chrome-extension:')) throw new Error('internal browser pages cannot be captured');
    return chrome.tabCapture.getMediaStreamId({ targetTabId: target.tabId, consumerTabId: consumer.tabId });
  },
});
