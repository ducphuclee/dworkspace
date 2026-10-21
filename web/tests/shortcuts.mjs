import assert from 'node:assert/strict';
import { eventually, withFixture } from './fixture.mjs';

// The single-letter shortcuts (#23): each one acts when nothing is being
// typed, stays a plain letter while something is, and shows in the sheet.
await withFixture(async ({ page, api, base, workspace }) => {
  page.setDefaultTimeout(15000);
  const doc = await api('POST', '/api/pages', { title: 'Shortcut test', workspaceId: workspace.id });
  await api('PATCH', `/api/pages/${doc.id}`, {
    content: [{ id: 'p1', type: 'paragraph', props: {}, content: [{ type: 'text', text: 'Hello', styles: {} }], children: [] }],
  });
  await page.goto(`${base}/p/${doc.id}`);
  await page.locator('[data-id="p1"] .bn-inline-content').waitFor();
  const away = () => page.evaluate(() => (document.activeElement instanceof HTMLElement ? document.activeElement.blur() : null));

  // While typing, a letter is a letter.
  await page.locator('[data-id="p1"] .bn-inline-content').click();
  await page.keyboard.type('l');
  // Wherever the click left the caret, the letter has to land in the text.
  await eventually(async () => (await page.locator('[data-id="p1"] .bn-inline-content').innerText()).length === 'Hello'.length + 1);
  assert.equal(await page.locator('.index-view').count(), 0, 'typing l must not open the library');

  // f: favourite on, and off again.
  await away();
  await page.keyboard.press('f');
  await eventually(async () => (await api('GET', '/api/favorites')).some((f) => (f.id ?? f) === doc.id));
  await page.keyboard.press('f');
  await eventually(async () => !(await api('GET', '/api/favorites')).some((f) => (f.id ?? f) === doc.id));

  // c: the comment panel.
  await page.keyboard.press('c');
  await page.locator('.comments-panel').waitFor();
  // The panel puts the caret in its compose box, where c is a letter again.
  await away();
  await page.keyboard.press('c');
  await page.locator('.comments-panel').waitFor({ state: 'hidden' });

  // m: notes mode, which has a column of its own on a wide screen.
  await page.keyboard.press('m');
  await page.locator('.notes-list').waitFor();
  await page.keyboard.press('m');
  await page.locator('.notes-list').waitFor({ state: 'hidden' });

  // ?: the sheet names every one of them.
  await page.keyboard.press('Shift+?');
  const sheet = page.locator('.shortcut-sheet');
  await sheet.waitFor();
  const text = await sheet.innerText();
  for (const label of ['Library', 'Notes mode', 'New collection', 'Add or remove favorite', 'Comments', 'New page from a template']) {
    assert(text.includes(label), `the sheet lists "${label}"`);
  }
  await page.keyboard.press('Escape');
  await sheet.waitFor({ state: 'hidden' });

  // t: the template gallery.
  await away();
  await page.keyboard.press('t');
  await page.locator('.tpl-dialog').waitFor();
  await page.keyboard.press('Escape');
  await page.locator('.tpl-dialog').waitFor({ state: 'hidden' });

  // l: the library.
  await away();
  await page.keyboard.press('l');
  await page.locator('.index-view').waitFor();

  // Shift+N: a new collection, opened.
  await away();
  const before = (await api('GET', '/api/pages')).filter((p) => p.type === 'collection').length;
  await page.keyboard.press('Shift+N');
  await eventually(async () => (await api('GET', '/api/pages')).filter((p) => p.type === 'collection').length === before + 1);

  console.log('Shortcuts: letters act when nothing is typed, stay letters while typing, and are all in the sheet.');
});
