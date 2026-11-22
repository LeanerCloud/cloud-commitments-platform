/**
 * Shared setup for the index.html structure suites (html*.test.ts): the raw
 * markup read once at module load and the per-test DOM restore. This file is
 * not a test (jest only collects *.test.ts).
 */
import fs from 'fs';
import path from 'path';

// Read HTML content once at module load
const htmlPath = path.join(__dirname, '..', 'index.html');
export const htmlContent = fs.readFileSync(htmlPath, 'utf8');

// Extract head and body content using regex
const headMatch = htmlContent.match(/<head[^>]*>([\s\S]*?)<\/head>/i);
const bodyMatch = htmlContent.match(/<body[^>]*>([\s\S]*?)<\/body>/i);
const headContent = headMatch?.[1] || '';
export const bodyContent = bodyMatch?.[1] || '';

export function setupHtmlDom(): void {
  // Restore HTML content before each test (since afterEach in setup clears it)
  document.head.innerHTML = headContent;
  document.body.innerHTML = bodyContent;

  // Set lang attribute
  const langMatch = htmlContent.match(/<html[^>]+lang=["']([^"']+)["']/i);
  if (langMatch && langMatch[1]) {
    document.documentElement.setAttribute('lang', langMatch[1]);
  }
}
