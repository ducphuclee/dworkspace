import assert from 'node:assert/strict';
import { eventually, withFixture } from './fixture.mjs';

// A collection embedded in a document (#22, #26): the embed keeps its filters
// to itself, two embeds of one collection disagree as they please, the
// collection's own view never notices, and the embed shows a lean toolbar.
// The fixture's table view is saved with "Status is To do", so it shows one of
// the two rows.
await withFixture(async ({ page, api, base, workspace, path, read }) => {
  page.setDefaultTimeout(15000);
  const collectionId = path.split('/').pop();
  const embedIn = async (title) => {
    const doc = await api('POST', '/api/pages', { title, workspaceId: workspace.id });
    await api('PATCH', `/api/pages/${doc.id}`, {
      content: [{ id: 'db', type: 'database', props: { collectionId }, content: [], children: [] }],
    });
    return doc;
  };
  const a = await embedIn('Project A');
  const b = await embedIn('Project B');
  const rowsIn = (scope) => page.locator(`${scope} .db-table tbody tr`).filter({ hasText: /row/ });

  // Project A: the embed starts out as the collection's view says.
  await page.goto(`${base}/p/${a.id}`);
  await eventually(async () => (await rowsIn('.bn-db-embed').count()) === 1);

  // #22: a window onto the rows, not a second copy of the collection.
  const embed = page.locator('.bn-db-embed');
  assert.equal(await embed.getByRole('button', { name: 'Properties', exact: true }).count(), 0);
  assert.equal(await embed.locator('.view-add').count(), 0);
  assert.equal(await embed.getByRole('button', { name: 'View options', exact: true }).count(), 0);

  // #26: taking the filter away here shows both rows HERE...
  const before = (await read()).views;
  await embed.locator('.fs-controls button', { hasText: 'Filter' }).first().click();
  await page.locator('.fs-remove').first().click();
  await page.keyboard.press('Escape');
  await eventually(async () => (await rowsIn('.bn-db-embed').count()) === 2);
  // ...and nowhere else: the collection's saved view is untouched.
  assert.deepEqual((await read()).views, before, "the collection's views must not change");

  // It is kept with the document.
  await eventually(async () => {
    await page.reload();
    await rowsIn('.bn-db-embed').first().waitFor();
    return (await rowsIn('.bn-db-embed').count()) === 2;
  });

  // Project B embeds the same collection and still sees it filtered.
  await page.goto(`${base}/p/${b.id}`);
  await eventually(async () => (await rowsIn('.bn-db-embed').count()) === 1);

  // So does the collection's own page.
  await page.goto(`${base}${path.replace('/api/collections', '/p')}`);
  await eventually(async () => (await rowsIn('.collection-scroll').count()) === 1);

  console.log('Embeds: own filters per embed, the collection untouched, a lean toolbar.');
});
