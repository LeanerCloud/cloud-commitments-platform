export function clearChildren(el: HTMLElement): void {
  while (el.firstChild) el.removeChild(el.firstChild);
}

export function renderEmptyParagraph(container: HTMLElement, message: string): void {
  clearChildren(container);
  const p = document.createElement('p');
  p.className = 'empty';
  p.textContent = message;
  container.appendChild(p);
}

// numeric right-aligns money and count cells with tabular digits (td.num,
// issue #739) so amounts line up for comparison.
export function appendCell(tr: HTMLTableRowElement, text: string, numeric = false): void {
  const td = document.createElement('td');
  td.textContent = text;
  if (numeric) td.className = 'num';
  tr.appendChild(td);
}
