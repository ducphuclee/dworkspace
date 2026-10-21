import assert from 'node:assert/strict';
import { eventually, withFixture } from './fixture.mjs';

// Opening a page in a second tab from a collection view. A board card is a
// database ROW, and /api/pages deliberately leaves rows out — so the tab used to
// be kept only while it was active and vanished the moment you looked away.
// ⌘-click has to add it, title it with the row, and let it outlive the switch
// back to the collection's own tab.
await withFixture(async ({ page }) => {
  page.setDefaultTimeout(15000);

  const tabs = page.locator('.tab-bar .tab');
  const firstRow = page.locator('.board-card', { hasText: 'First row' });
  const boardTab = page.locator('.view-tab', { hasText: 'Board' });

  await boardTab.click();
  await firstRow.waitFor();
  // One page needs no strip.
  assert.equal(await tabs.count(), 0);

  // ⌘-click a card: a second tab, titled with the row.
  await firstRow.click({ modifiers: ['Meta'] });
  await eventually(async () => (await tabs.count()) === 2);
  await eventually(async () => (await tabs.filter({ hasText: 'First row' }).count()) === 1);

  // Back to the collection: the row's tab stays. Dropping it here is the bug —
  // the row is not in the page list, and the tab strip must keep it anyway.
  // (The collection remounts on its first view, so the board has to be picked
  // again; that is beside the point of the assertion.)
  await tabs.filter({ hasText: 'Option editing test' }).click();
  await boardTab.waitFor();
  assert.equal(await tabs.count(), 2);
  assert.equal(await tabs.filter({ hasText: 'First row' }).count(), 1);

  // The card's ⋯ menu carries the visible entry, the way the sidebar's does.
  await boardTab.click();
  await firstRow.waitFor();
  await firstRow.click({ button: 'right' });
  await page.getByRole('button', { name: 'Open in new tab', exact: true }).waitFor();

  // An ordinary click still navigates the active tab instead of adding one.
  await page.locator('.board-card', { hasText: 'Second row' }).click();
  await eventually(async () => (await tabs.filter({ hasText: 'Second row' }).count()) === 1);
  assert.equal(await tabs.count(), 2);
});
