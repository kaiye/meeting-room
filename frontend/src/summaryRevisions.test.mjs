import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'
import ts from 'typescript'

// Use the existing compiler so this test also runs on Node versions without
// native TypeScript support. The module has no runtime imports.
const source = readFileSync(new URL('./summaryRevisions.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.ESNext },
})
const { applySummaryRevisions } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)

const revision = (kind, quote, body = '', anchor = 'occ:0') => ({
  id: `${kind}:${quote}`, target: 'summary', kind, quote, body, anchor,
})

test('overlapping deletion is reported instead of silently omitted', () => {
  const deletion = revision('delete', 'Example Person, phone 555-0100')
  const rewrite = revision('rewrite', '555-0100', '555-0199')
  for (const comments of [[deletion, rewrite], [rewrite, deletion]]) {
    const result = applySummaryRevisions('Contact: Example Person, phone 555-0100', comments)
    assert.ok(result.unapplied.includes(deletion))
    assert.equal(result.text, '', 'conflicts must not expose partially redacted text')
  }
})

test('non-overlapping edits honor repeated occurrence anchors', () => {
  const result = applySummaryRevisions('### Example\n\nAlice met Alice.\n\n**Delete me**', [
    revision('rewrite', 'Alice', 'Bob', 'occ:1'),
    revision('delete', 'Delete me'),
  ])
  assert.match(result.text, /Alice met Bob/)
  assert.doesNotMatch(result.text, /Delete me/)
  assert.equal(result.unapplied.length, 0)
})

test('deletion matches the same normalized bold text shown on screen', () => {
  const result = applySummaryRevisions('**项目：**完成', [revision('delete', '项目： 完成')])
  assert.equal(result.unapplied.length, 0)
  assert.doesNotMatch(result.text, /项目|完成/)
})

test('missing quotes are reported', () => {
  const missing = revision('delete', 'outdated text')
  const result = applySummaryRevisions('new summary', [missing])
  assert.deepEqual(result.unapplied, [missing])
  assert.equal(result.text, '', 'unmatched deletions must not expose the original text')
})
