// Historical authentication fixtures and judges remain usable offline. Their
// live credential-copying runners have no implementation in the native-only runtime.
export function retiredAuthProfile(name) {
  return Object.assign(new Error(`${name} is retired: auth_status/api_read were removed. No worker or model was started. Use eval/general-web/run.js or eval/browser-use-v2/run.js for new evaluations; eval/auth/diagnose.js still reads historical reports.`), { code: 'RETIRED_PROFILE' });
}
