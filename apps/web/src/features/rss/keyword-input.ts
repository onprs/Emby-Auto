export function parseKeywordInput(value: string): string[] {
  const seen = new Set<string>();
  const keywords: string[] = [];
  for (const part of value.split(/[,，\n]/)) {
    const keyword = part.trim();
    const identity = keyword.toLowerCase();
    if (!keyword || seen.has(identity)) {
      continue;
    }
    seen.add(identity);
    keywords.push(keyword);
  }
  return keywords;
}

export function formatKeywordInput(keywords: string[]): string {
  return keywords.join(', ');
}
