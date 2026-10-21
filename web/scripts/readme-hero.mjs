// Renders .github/hero.png, the picture at the top of README.md: the board in
// both themes, light left of a slanted cut and dark right of it.
//
//   node scripts/readme-hero.mjs
//
// shoot-wiki.mjs runs this whenever it retakes the board, so the README cannot
// fall behind the wiki: the board's shots are retaken when the gate reports
// them stale, and this picture is made from them. What it replaced was a GIF
// recorded once by hand, which still showed the old design two releases after
// it was gone.
//
// The banner above it is not made here. It is the website's hero scene with the
// headline set the way the site sets it: a picture that changes when the brand
// does, not when the app does.
//
// Like shoot-wiki.mjs it needs playwright, which is deliberately not a
// dependency: npm i --no-save playwright && npx playwright install chromium
// (or PLAYWRIGHT_CHANNEL=chrome to use an installed Chrome).

import { readFileSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, '../..');

let chromium;
try {
  ({ chromium } = await import('playwright'));
} catch {
  console.error(
    '\n  playwright is not installed — it is deliberately not a dependency.\n' +
      '  npm i --no-save playwright && npx playwright install chromium\n',
  );
  process.exit(1);
}

const RAINBOW = ['#ff2d60 0%', '#ff8a2d 25%', '#ffd12d 45%', '#22c55e 62%', '#3b82f6 80%', '#9333ea 100%'];

// Where the cut crosses the top and the bottom edge, as a share of the width.
// Light keeps the sidebar, dark takes the larger part: most people reading a
// README on GitHub read it dark.
const CUT_TOP = 0.5;
const CUT_BOTTOM = 0.34;

// Both shots come from the same seeded instance at the same size, so they line
// up to the pixel and the cut is the only thing that changes across the seam.
const shot = (name) =>
  `data:image/png;base64,${readFileSync(join(repo, 'wiki/img', name)).toString('base64')}`;
const [w, h] = [1280, 800];
const [x1, x2] = [CUT_TOP * w, CUT_BOTTOM * w];

const html = `<!doctype html>
<style>
  html, body { margin: 0; background: transparent; }
  .hero { position: relative; width: ${w}px; height: ${h}px; border-radius: 18px; overflow: hidden; }
  .hero img { position: absolute; inset: 0; width: 100%; height: 100%; }
  .dark { clip-path: polygon(${x1}px 0, 100% 0, 100% 100%, ${x2}px 100%); }
  .hero svg { position: absolute; inset: 0; }
  /* A hairline that reads on GitHub's white page and on its dark one alike. */
  .hero::after { content: ''; position: absolute; inset: 0; border-radius: 18px;
    box-shadow: inset 0 0 0 1px rgba(128, 128, 128, 0.3); }
</style>
<div class="hero">
  <img src="${shot('collection-board.png')}" alt="">
  <img class="dark" src="${shot('collection-board-dark.png')}" alt="">
  <svg viewBox="0 0 ${w} ${h}" width="${w}" height="${h}">
    <defs>
      <linearGradient id="seam" gradientUnits="userSpaceOnUse" x1="${x1}" y1="0" x2="${x2}" y2="${h}">
        ${RAINBOW.map((s) => s.split(' ')).map(([c, o]) => `<stop offset="${o}" stop-color="${c}"/>`).join('')}
      </linearGradient>
      <filter id="glow" x="-50%" y="-10%" width="200%" height="120%"><feGaussianBlur stdDeviation="7"/></filter>
    </defs>
    <line x1="${x1}" y1="0" x2="${x2}" y2="${h}" stroke="url(#seam)" stroke-width="14" opacity="0.5" filter="url(#glow)"/>
    <line x1="${x1}" y1="0" x2="${x2}" y2="${h}" stroke="url(#seam)" stroke-width="2"/>
  </svg>
</div>`;

const browser = await chromium.launch(
  process.env.PLAYWRIGHT_CHANNEL ? { channel: process.env.PLAYWRIGHT_CHANNEL } : {},
);
const page = await browser.newPage({ deviceScaleFactor: 2, viewport: { width: w, height: h } });
await page.setContent(html, { waitUntil: 'load' });
await page.locator('.hero').screenshot({ path: join(repo, '.github/hero.png'), omitBackground: true });
await browser.close();
console.log('  ok    .github/hero.png');
