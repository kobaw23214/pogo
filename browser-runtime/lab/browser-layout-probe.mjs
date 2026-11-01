// Read-only client geometry probe shared by component and deployed acceptance.
// The image's CSS box can contain letterboxing: track the drawn pixels too.
export async function startLayoutProbe(page) {
  await page.evaluate(() => {
    globalThis.__browserLayoutProbe?.stop?.();
    const samples = []; let active = true, raf;
    const rect = el => { const r = el?.getBoundingClientRect(); return r && { x: r.x, y: r.y, width: r.width, height: r.height }; };
    const sample = () => {
      const pane = [...document.querySelectorAll('.native-browser')].find(el => el.getBoundingClientRect().width > 0);
      const surface = pane?.querySelector('.native-page'), img = surface?.querySelector('img');
      if (surface && img?.complete && img.naturalWidth) {
        const image = rect(img), scale = Math.min(image.width / img.naturalWidth, image.height / img.naturalHeight);
        const drawn = { x: image.x + (image.width - img.naturalWidth * scale) / 2, y: image.y + (image.height - img.naturalHeight * scale) / 2, width: img.naturalWidth * scale, height: img.naturalHeight * scale };
        samples.push({ t: performance.now(), page: rect(surface), image, drawn, pixels: { width: img.naturalWidth, height: img.naturalHeight }, status: !!pane.querySelector('.native-activity,.native-loading-status,.native-reset,.native-error,.native-dialog'), status_text: pane.querySelector('.native-statusbar')?.textContent?.trim().slice(0, 120) || '' });
      }
      if (active) raf = requestAnimationFrame(sample);
    };
    globalThis.__browserLayoutProbe = { samples, stop() { active = false; cancelAnimationFrame(raf); sample(); return samples; } }; sample();
  });
}
export async function finishLayoutProbe(page) {
  const samples = await page.evaluate(() => globalThis.__browserLayoutProbe.stop());
  const fields = ['page', 'image', 'drawn', 'pixels'];
  const deltas = Object.fromEntries(fields.map(field => [field, samples.length ? Math.max(...Object.keys(samples[0][field]).map(key => Math.max(...samples.map(s => s[field][key])) - Math.min(...samples.map(s => s[field][key])))) : Infinity]));
  return { count: samples.length, deltas, max_delta: Math.max(...Object.values(deltas)), status_samples: samples.filter(s => s.status).length, first: samples[0], last: samples.at(-1), samples };
}
