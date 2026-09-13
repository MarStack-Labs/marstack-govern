const playwright = await import(process.env.PLAYWRIGHT_MODULE ?? "playwright");
const { chromium } = playwright.chromium ? playwright : playwright.default;

const BASE = process.env.BASE ?? "http://127.0.0.1:8099";
const OUT = process.env.VIDEO_DIR ?? "/tmp/margov-ui/video";
const WIDTH = Number(process.env.WIDTH ?? 1360);
const HEIGHT = Number(process.env.HEIGHT ?? 860);
const VIEWS = (process.env.VIEWS ?? "Services Divisions Requests Cost Policies Topology Environments")
  .split(/\s+/)
  .filter(Boolean);

const wait = (ms) => new Promise((r) => setTimeout(r, ms));

const browser = await chromium.launch();
const context = await browser.newContext({
  viewport: { width: WIDTH, height: HEIGHT },
  deviceScaleFactor: 2,
  recordVideo: { dir: OUT, size: { width: WIDTH, height: HEIGHT } },
});
const page = await context.newPage();

await page.goto(`${BASE}/auth/login`, { waitUntil: "networkidle" });
await page.goto(`${BASE}/`, { waitUntil: "networkidle" });

try {
  await page.waitForSelector(".ms-rail", { state: "visible", timeout: 15000 });
} catch {
  console.error("the console never rendered; is margov serving on " + BASE + "?");
  await context.close();
  await browser.close();
  process.exit(1);
}

await wait(2400);

for (const view of VIEWS) {
  const target = page.locator(`.ms-rail button`, { hasText: new RegExp(`^${view}$`) });
  if ((await target.count()) === 0) {
    console.error(`no view called ${view}`);
    continue;
  }
  await target.first().click();
  await wait(2600);
}

await context.close();
await browser.close();
console.log("recorded");
