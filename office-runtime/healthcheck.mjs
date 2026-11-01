try {
  // Docker samples liveness only. Real document-open requests keep retrying
  // transient samples in Pogo's separate five-minute readiness stage.
  const response = await fetch('http://127.0.0.1:8080/pogo-office/healthz', { redirect: 'error', signal: AbortSignal.timeout(3000) });
  const result = await response.json();
  process.exit(response.ok && result.ready === true && result.adapter === 'pogo-office-history' && result.engineVersion === '9.4.0 (build:129)' && result.verifiedAssets === 9 && /^[a-f0-9]{64}$/.test(result.extensionSha256) ? 0 : 1);
} catch { process.exit(1); }
