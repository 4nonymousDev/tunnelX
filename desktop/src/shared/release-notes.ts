const BLOCK_END_TAG = /<\s*\/\s*(?:blockquote|div|h[1-6]|li|ol|p|ul)\s*>/gi
const BLOCK_START_TAG = /<\s*(?:blockquote|div|h[1-6]|ol|p|ul)(?:\s[^>]*)?>/gi
const LINE_BREAK_TAG = /<\s*br\s*\/?\s*>/gi
const LIST_ITEM_TAG = /<\s*li(?:\s[^>]*)?>/gi
const SCRIPT_OR_STYLE = /<\s*(script|style)\b[^>]*>[\s\S]*?<\s*\/\s*\1\s*>/gi
const REMAINING_TAG = /<[^>]*>/g

const NAMED_ENTITIES: Record<string, string> = {
  amp: '&',
  apos: "'",
  gt: '>',
  lt: '<',
  nbsp: ' ',
  quot: '"',
}

export function releaseNoteAsPlainText(value: string): string {
  // GitHub's feed can encode the release HTML as entities. Decode before
  // stripping markup so the decoded tags do not leak into the dialog.
  return decodeEntities(value)
    .replace(/\r\n?/g, '\n')
    .replace(/\\(?=<\/?[a-z][^>]*>)/gi, '')
    .replace(SCRIPT_OR_STYLE, '')
    .replace(LINE_BREAK_TAG, '\n')
    .replace(LIST_ITEM_TAG, '• ')
    .replace(BLOCK_END_TAG, '\n')
    .replace(BLOCK_START_TAG, '')
    .replace(REMAINING_TAG, '')
    .replace(/•[ \t]+/g, '• ')
    .replace(/[ \t]+\n/g, '\n')
    .replace(/\n[ \t]+/g, '\n')
    .replace(/\n{3,}/g, '\n\n')
    .trim()
}

function decodeEntities(value: string): string {
  return value
    .replace(/&#(x[0-9a-f]+|\d+);/gi, (entity, code: string) => {
      const numeric = code[0]?.toLowerCase() === 'x'
        ? Number.parseInt(code.slice(1), 16)
        : Number.parseInt(code, 10)
      if (!Number.isSafeInteger(numeric) || numeric < 0 || numeric > 0x10ffff || (numeric >= 0xd800 && numeric <= 0xdfff)) {
        return entity
      }
      return String.fromCodePoint(numeric)
    })
    .replace(/&([a-z]+);/gi, (entity, name: string) => NAMED_ENTITIES[name.toLowerCase()] ?? entity)
}
