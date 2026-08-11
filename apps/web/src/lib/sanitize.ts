/** Removes common credentials and shortens absolute server paths to basenames. */
export function sanitizeTechnicalDetails(value: string): string {
  let output = value.slice(0, 8_000);
  output = output.replace(/(authorization\s*[:=]\s*(?:bearer|basic)\s+)[^\s,;]+/gi, '$1[已隐藏]');
  output = output.replace(/(bearer\s+)[a-z0-9._~+/=-]+/gi, '$1[已隐藏]');
  output = output.replace(/((?:password|passwd|token|api[_-]?key|cookie|set-cookie)\s*[:=]\s*)[^\s,;]+/gi, '$1[已隐藏]');
  output = output.replace(/(["'](?:password|passwd|token|api[_-]?key|cookie|authorization)["']\s*:\s*)["'][^"']*["']/gi, '$1"[已隐藏]"');
  output = output.replace(/(https?:\/\/)[^/@\s]+@/gi, '$1[凭据已隐藏]@');
  output = output.replace(/[a-z]:\\(?:[^\\\r\n]+\\)+([^\\\r\n\s"']+)/gi, '[服务器路径]\\$1');
  output = output.replace(/(^|[\s"'=(])\/(?:[^/\s"'<>]+\/)+([^/\s"'<>]+)/gm, '$1[服务器路径]/$2');
  return output;
}
