import assert from 'node:assert/strict';
import { eventually, withFixture } from './fixture.mjs';

// The four small bugs reported from use: a view's tab answers a right-click
// (#24), so does a template (#20), a row menu near the bottom of the tree is
// not cut off by the section under it (#21), ↑ does not skip an empty first
// block on its way to the title (#25), and the library's shelves scroll inside
// a narrow pane instead of running off it (#19).
await withFixture(async ({ page, api, base, workspace, read }) => {
  page.setDefaultTimeout(15000);

  // #24 — the right-click acts on the tab it was made on, not on the view
  // you happen to be looking at (Table).
  await page.locator('.view-tab', { hasText: 'Board' }).click({ button: 'right' });
  await page.getByRole('button', { name: 'Remove view', exact: true }).click();
  await eventually(async () => (await read()).views.map((v) => v.id).join() === 'table');

  // #20 — a template row opens its menu on a right-click, and the menu closes
  // on a click elsewhere like every other menu in the sidebar.
  const template = await api('POST', '/api/pages', { title: 'Weekly template', workspaceId: workspace.id });
  await api('PATCH', `/api/pages/${template.id}`, { isTemplate: true });
  await page.reload();
  await page.locator('.tree-item', { hasText: 'Weekly template' }).click({ button: 'right' });
  const editTemplate = page.getByRole('button', { name: 'Edit the template itself', exact: true });
  await editTemplate.waitFor();
  await page.locator('.main').click({ position: { x: 400, y: 400 } });
  await editTemplate.waitFor({ state: 'hidden' });

  // #21 — enough collections that the last one sits at the foot of the tree,
  // right above the Templates section; its menu has to stay whole and on top.
  for (let i = 0; i < 14; i++) {
    await api('POST', '/api/pages', { title: `Shelf ${String(i).padStart(2, '0')}`, type: 'collection', workspaceId: workspace.id });
  }
  await page.reload();
  const lastRow = page.locator('.tree-item', { hasText: 'Shelf 13' });
  await lastRow.scrollIntoViewIfNeeded();
  await lastRow.click({ button: 'right' });
  const menu = lastRow.locator('.menu');
  await menu.waitFor();
  const box = await menu.boundingBox();
  const viewport = page.viewportSize();
  assert(box.y >= 0 && box.y + box.height <= viewport.height, 'the menu fits the screen');
  for (const entry of await menu.locator('button').all()) {
    const b = await entry.boundingBox();
    const onTop = await page.evaluate(
      ([x, y]) => !!document.elementFromPoint(x, y)?.closest('.menu'),
      [b.x + b.width / 2, b.y + b.height / 2],
    );
    assert(onTop, 'no entry of the menu is covered by the section below');
  }
  await page.keyboard.press('Escape');

  // #25 — an empty first block is somewhere ↑ can go.
  const doc = await api('POST', '/api/pages', { title: 'Arrows', workspaceId: workspace.id });
  await api('PATCH', `/api/pages/${doc.id}`, {
    content: [
      { id: 'empty', type: 'paragraph', props: {}, content: [], children: [] },
      { id: 'text', type: 'paragraph', props: {}, content: [{ type: 'text', text: 'Second block', styles: {} }], children: [] },
    ],
  });
  await page.goto(`${base}/p/${doc.id}`);
  const second = page.locator('[data-id="text"] .bn-inline-content');
  await second.click({ position: { x: 1, y: 6 } });
  await page.keyboard.press('ArrowUp');
  const where = await page.evaluate(() => {
    const anchor = document.getSelection()?.anchorNode;
    const el = anchor instanceof Element ? anchor : anchor?.parentElement;
    return {
      inTitle: document.activeElement?.classList.contains('page-title') ?? false,
      block: el?.closest('[data-id]')?.getAttribute('data-id') ?? null,
    };
  });
  assert.equal(where.inTitle, false, '↑ must not jump past the empty block to the title');
  assert.equal(where.block, 'empty');

  // The icon picker stays above the editor (found on the test box): over a
  // cover, the editor's side menu (+ and the drag handle) drew through it and
  // followed the pointer underneath.
  const covered = await api('POST', '/api/pages', { title: 'Covered', workspaceId: workspace.id });
  await api('PATCH', `/api/pages/${covered.id}`, {
    icon: '🤝',
    cover: 'gradient:linear-gradient(120deg,#4fa872,#2f7d4f)',
    content: [
      { id: 'b1', type: 'paragraph', props: {}, content: [{ type: 'text', text: 'First', styles: {} }], children: [] },
      { id: 'b2', type: 'paragraph', props: {}, content: [{ type: 'text', text: 'Second', styles: {} }], children: [] },
    ],
  });
  await page.goto(`${base}/p/${covered.id}`);
  await page.locator('[data-id="b2"] .bn-inline-content').waitFor();
  await page.locator('.page-icon').click();
  const picker = page.locator('.icon-picker');
  await picker.waitFor();
  const pb = await picker.boundingBox();
  for (const [fx, fy] of [[0.15, 0.5], [0.5, 0.65], [0.3, 0.85]]) {
    const x = pb.x + pb.width * fx;
    const y = pb.y + pb.height * fy;
    await page.mouse.move(x, y);
    await page.waitForTimeout(150);
    const onTop = await page.evaluate(([px, py]) => !!document.elementFromPoint(px, py)?.closest('.icon-picker'), [x, y]);
    assert(onTop, 'the picker is on top where the pointer is');
  }
  const showing = await page.locator('.bn-side-menu').evaluateAll(
    (els) => els.filter((e) => getComputedStyle(e).visibility === 'visible' && e.getBoundingClientRect().width > 0).length,
  );
  assert.equal(showing, 0, 'no side menu shows through the picker');
  await page.keyboard.press('Escape');

  // #19 — beside the sidebar, a 1000px window leaves the library a narrow pane.
  await page.setViewportSize({ width: 1000, height: 800 });
  await page.getByRole('button', { name: /^Library/ }).first().click();
  const modes = page.locator('.index-modes');
  await modes.waitFor();
  const fit = await modes.evaluate((el) => {
    const pane = el.closest('.index-view').getBoundingClientRect();
    const own = el.getBoundingClientRect();
    return { right: own.right, paneRight: pane.right, scrolls: el.scrollWidth > el.clientWidth };
  });
  assert(fit.right <= fit.paneRight + 1, 'the shelves end inside the pane');
  assert(fit.scrolls, 'the shelves that do not fit are reached by scrolling');

  console.log('Menus, view tabs, arrow keys, the icon picker and library shelves passed.');
});
