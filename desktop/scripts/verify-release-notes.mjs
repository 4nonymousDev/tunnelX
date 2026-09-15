import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import ts from 'typescript'

const source = await readFile(new URL('../src/shared/release-notes.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
})
const { releaseNoteAsPlainText } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)

const lines = [
  '修正停止再启用端口显示被占用问题',
  '导出列表显示对端名称与ID',
  'Fixed the issue where a port is displayed as occupied after being stopped and re-enabled.',
  'The export list displays the peer name and ID.',
]
const html = `<ul> ${lines.map(line => `<li> <p>${line}</p> </li>`).join(' ')} </ul>`
const expected = lines.map(line => `• ${line}`).join('\n\n')

assert.equal(releaseNoteAsPlainText(html), expected)
assert.equal(releaseNoteAsPlainText(html.replaceAll('<', '&lt;').replaceAll('>', '&gt;')), expected)
assert.equal(releaseNoteAsPlainText(html.replaceAll('<', '&#60;').replaceAll('>', '&#x3e;')), expected)
assert.equal(releaseNoteAsPlainText(html.replaceAll('<', '\\<')), expected)
assert.equal(releaseNoteAsPlainText('- 修复端口\n- Export peer ID'), '- 修复端口\n- Export peer ID')
assert.equal(releaseNoteAsPlainText('<p>A &amp; B<br>版本 &#x1f680;</p>'), 'A & B\n版本 🚀')
assert.equal(releaseNoteAsPlainText('&lt;script&gt;ignored()&lt;/script&gt;&lt;p&gt;修复&lt;/p&gt;'), '修复')
assert.equal(releaseNoteAsPlainText('&#1114112; &#55296;'), '&#1114112; &#55296;')
console.log('release notes verification passed')
