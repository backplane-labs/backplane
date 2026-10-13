const base = process.env.BACKPLANE_DEMO_URL ?? 'http://127.0.0.1:8081/backplane';
const deadline = Date.now() + 120000;
while (true) {
  try {
    const session = await fetch(`${base}/auth/session`, { signal: AbortSignal.timeout(2000) });
    const services = await Promise.all(['backplane', 'hello', 'formatter'].map(async (name) => {
      const response = await fetch(`http://127.0.0.1:8500/v1/health/service/${name}?passing`, { signal: AbortSignal.timeout(2000) });
      const entries = await response.json();
      return Array.isArray(entries) && entries.some((entry) => entry.Service?.ID === `${name}-dev`);
    }));
    if (session.status === 401 && services.every(Boolean)) { console.log('Development console and all three services are ready'); break; }
  } catch { /* Bounded readiness wait while the registry converges. */ }
  if (Date.now() >= deadline) throw new Error('Development services did not become ready; inspect make dev-logs');
  await new Promise((resolve) => setTimeout(resolve, 1000));
}
