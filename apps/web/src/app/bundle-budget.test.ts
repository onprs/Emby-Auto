import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';

import { checkBundleBudget } from '../../../../scripts/web/check-bundle-budget.mts';

const temporaryDirectories: string[] = [];

async function buildFixture(entryBytes: number, chunkBytes: number): Promise<string> {
  const directory = await mkdtemp(join(tmpdir(), 'emby-auto-bundle-'));
  temporaryDirectories.push(directory);
  await mkdir(join(directory, 'assets'));
  await writeFile(
    join(directory, 'index.html'),
    '<!doctype html><script type="module" crossorigin src="/assets/index-hash.js"></script>',
  );
  await writeFile(join(directory, 'assets/index-hash.js'), Buffer.alloc(entryBytes, 'a'));
  await writeFile(join(directory, 'assets/route-hash.js'), Buffer.alloc(chunkBytes, 'b'));
  return directory;
}

afterEach(async () => {
  await Promise.all(temporaryDirectories.splice(0).map((directory) => rm(directory, { recursive: true, force: true })));
});

describe('bundle budget', () => {
  it('reports raw and gzip sizes when the entry and route chunks fit', async () => {
    const directory = await buildFixture(100, 80);
    const report: string[] = [];

    const assets = await checkBundleBudget(directory, {
      entryBudgetBytes: 100,
      chunkBudgetBytes: 80,
      report: (line) => report.push(line),
    });

    expect(assets).toHaveLength(2);
    expect(assets[0]).toMatchObject({ path: 'assets/index-hash.js', bytes: 100, entry: true });
    expect(report).toEqual([
      expect.stringMatching(/^entry assets\/index-hash\.js: raw=.* gzip=.* budget=/),
      expect.stringMatching(/^chunk assets\/route-hash\.js: raw=.* gzip=.* budget=/),
    ]);
  });

  it('fails when the raw entry exceeds its budget even if it compresses well', async () => {
    const directory = await buildFixture(101, 80);

    await expect(checkBundleBudget(directory, {
      entryBudgetBytes: 100,
      chunkBudgetBytes: 80,
      report: () => undefined,
    })).rejects.toThrow('assets/index-hash.js is 101 bytes; limit is 100 bytes');
  });

  it('fails when any emitted route chunk exceeds its budget', async () => {
    const directory = await buildFixture(100, 81);

    await expect(checkBundleBudget(directory, {
      entryBudgetBytes: 100,
      chunkBudgetBytes: 80,
      report: () => undefined,
    })).rejects.toThrow('assets/route-hash.js is 81 bytes; limit is 80 bytes');
  });
});
