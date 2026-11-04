// This classifier is only for repeatable display/observation work. Never use
// it to retry an input, navigation, click or submission whose outcome may have
// already happened in Chromium.
export function isNavigationContextError(error) {
  if (error?.code === 'LIVE_NAVIGATION_TRANSITION') return true;
  const message = typeof error?.message === 'string' ? error.message : '';
  return /Execution context was destroyed|Cannot find context with specified id|Execution context is not available in detached frame/i.test(message);
}

export function navigationObservationError(cause) {
  return Object.assign(new Error('The page changed during display preparation; waiting for the current document.'), {
    status: 409, code: 'LIVE_NAVIGATION_TRANSITION', ...(cause ? { cause } : {}),
  });
}
