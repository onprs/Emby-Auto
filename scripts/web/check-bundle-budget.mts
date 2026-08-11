import { gzipSync } from 'node:zlib';
import { readFile, readdir } from 'node:fs/promises';
import { dirname, relative, resolve, sep } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

export const ENTRY_BUDGET_BYTES = 500 * 1024;
export const CHUNK_BUDGET_BYTES = 500 * 1024;

type BundleBudgetOptions = {
  entryBudgetBytes?: number;
  chunkBudgetBytes?: number;
  report?: (line: string) => void;
};

type BundleAsset = {
  path: string;
  bytes: number;
  gzipBytes: number;
  entry: boolean;
};

export async function checkBundleBudget(
  distDirectory: string,
  options: BundleBudgetOptions = {},
): Promise<BundleAsset[]> {
  const dist = resolve(distDirectory);
  const entryBudget = options.entryBudgetBytes ?? ENTRY_BUDGET_BYTES;
  const chunkBudget = options.chunkBudgetBytes ?? CHUNK_BUDGET_BYTES;
  const report = options.report ?? console.log;
  const indexHtml = await readFile(resolve(dist, 'index.html'), 'utf8');
  const entryRelativePath = findModuleEntry(indexHtml);
  const entryPath = resolveAssetPath(dist, entryRelativePath);
  const javascriptFiles = await listJavascriptFiles(dist);

  if (!javascriptFiles.includes(entryPath)) {
    throw new Error(`module entry does not exist in build output: ${entryRelativePath}`);
  }

  const assets = await Promise.all(
    javascriptFiles.map(async (path): Promise<BundleAsset> => {
      const content = await readFile(path);
      return {
        path: relative(dist, path).split(sep).join('/'),
        bytes: content.byteLength,
        gzipBytes: gzipSync(content).byteLength,
        entry: path === entryPath,
      };
    }),
  );
  assets.sort((left, right) => Number(right.entry) - Number(left.entry) || right.bytes - left.bytes || left.path.localeCompare(right.path));

  const failures: string[] = [];
  for (const asset of assets) {
    const budget = asset.entry ? entryBudget : chunkBudget;
    report(
      `${asset.entry ? 'entry' : 'chunk'} ${asset.path}: raw=${formatKiB(asset.bytes)} gzip=${formatKiB(asset.gzipBytes)} budget=${formatKiB(budget)}`,
    );
    if (asset.bytes > budget) {
      failures.push(`${asset.path} is ${asset.bytes} bytes; limit is ${budget} bytes`);
    }
  }

  if (failures.length > 0) {
    throw new Error(`bundle budget exceeded:\n${failures.join('\n')}`);
  }

  return assets;
}

function findModuleEntry(indexHtml: string): string {
  for (const match of indexHtml.matchAll(/<script\b[^>]*>/gi)) {
    const tag = match[0];
    const type = tag.match(/\btype=["']([^"']+)["']/i)?.[1];
    const source = tag.match(/\bsrc=["']([^"']+)["']/i)?.[1];
    if (type === 'module' && source) {
      return decodeURIComponent(new URL(source, 'https://bundle.invalid').pathname).replace(/^\/+/, '');
    }
  }
  throw new Error('index.html does not contain a module entry script');
}

function resolveAssetPath(dist: string, assetPath: string): string {
  const path = resolve(dist, assetPath);
  const relativePath = relative(dist, path);
  if (relativePath.startsWith('..') || relativePath === '') {
    throw new Error(`invalid module entry path: ${assetPath}`);
  }
  return path;
}

async function listJavascriptFiles(directory: string): Promise<string[]> {
  const files: string[] = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) {
      files.push(...await listJavascriptFiles(path));
    } else if (entry.isFile() && entry.name.endsWith('.js')) {
      files.push(path);
    }
  }
  return files;
}

function formatKiB(bytes: number): string {
  return `${(bytes / 1024).toFixed(2)} KiB`;
}

const invokedPath = process.argv[1] ? pathToFileURL(resolve(process.argv[1])).href : '';
if (import.meta.url === invokedPath) {
  const defaultDist = resolve(dirname(fileURLToPath(import.meta.url)), '../../apps/web/dist');
  const dist = process.argv[2] ? resolve(process.argv[2]) : defaultDist;
  checkBundleBudget(dist).catch((error: unknown) => {
    console.error(error instanceof Error ? error.message : error);
    process.exitCode = 1;
  });
}
