import type { Comment } from './api'

// CommonMark refuses to close **bold** when the closer has a space before
// it (`**标题: **`) or sits between CJK punctuation and a letter
// (`**要点。**后续`) — both are everyday LLM output in Chinese minutes.
// Normalizing the spaces keeps the text valid Markdown, so rendering AND
// the copied/downloaded .md are fixed at once (code spans untouched).
export function normalizeBold(md: string): string {
  // Pair the delimiters up per line: a regex can't tell an opener from a
  // closer, so it would swallow the text sitting between two bold runs,
  // and `\s` would let a match span lines and eat list markers.
  const fixLine = (line: string): string => {
    const parts = line.split('**')
    if (parts.length < 3) return line
    const pairs = Math.floor((parts.length - 1) / 2)
    let out = parts[0]
    for (let i = 0; i < pairs; i++) {
      const body = parts[i * 2 + 1]
      const after = parts[i * 2 + 2]
      const trimmed = body.trim()
      if (!trimmed) {
        out += `**${body}**${after}`
        continue
      }
      // A closer glued to a word (CJK especially) needs a space to count.
      const gap = /^[^\s\p{P}]/u.test(after) ? ' ' : ''
      out += `**${trimmed}**${gap}${after}`
    }
    if ((parts.length - 1) % 2 === 1) out += `**${parts[parts.length - 1]}`
    return out
  }
  return md
    .split(/(```[\s\S]*?```|`[^`\n]*`)/g)
    .map((part, i) => (i % 2 === 1 ? part : part.split('\n').map(fixLine).join('\n')))
    .join('')
}


// Quotes and `occ:N` anchors are captured from the RENDERED summary, but the
// exported file is the Markdown source — so `**bold**`, `### ` and blank-line
// paragraph breaks sit between the two. projectMarkdown walks the source and
// emits the text a reader (and therefore a DOM selection) actually sees,
// keeping a map from each projected character back to its source offset, so a
// match can be spliced straight into the Markdown.
function projectMarkdown(md: string): { plain: string; map: number[] } {
  const plain: string[] = []
  const map: number[] = []
  let i = 0
  let atLineStart = true
  const pushSpace = () => {
    if (plain.length && plain[plain.length - 1] === ' ') return
    plain.push(' ')
    map.push(i)
  }
  while (i < md.length) {
    const ch = md[i]
    if (atLineStart) {
      const rest = md.slice(i)
      // At most ONE marker per line: in `### 9. Title` the `9.` is literal
      // text the heading renders, not an ordered-list marker.
      const m = rest.match(/^([ \t]*)(#{1,6}[ \t]+|[-*+][ \t]+|\d+\.[ \t]+|>[ \t]*)/)
      if (m) {
        i += m[0].length
        atLineStart = false
        continue
      }
      const lead = rest.match(/^[ \t]+/)
      if (lead) {
        i += lead[0].length
        continue
      }
      atLineStart = false
      continue
    }
    if (ch === '\n') {
      pushSpace()
      i++
      atLineStart = true
      continue
    }
    if (/\s/.test(ch)) {
      pushSpace()
      i++
      continue
    }
    if (md.startsWith('**', i) || md.startsWith('__', i) || md.startsWith('~~', i)) {
      i += 2
      continue
    }
    if (ch === '`' || ch === '|') {
      i++
      continue
    }
    if (ch === '[') {
      // [text](url) reads as just `text`.
      const close = md.indexOf('](', i)
      if (close > -1) {
        const end = md.indexOf(')', close)
        if (end > -1 && md.slice(i, end).indexOf('\n') === -1) {
          for (let k = i + 1; k < close; k++) {
            plain.push(md[k])
            map.push(k)
          }
          i = end + 1
          continue
        }
      }
    }
    plain.push(ch)
    map.push(i)
    i++
  }
  return { plain: plain.join(''), map }
}

const normWs = (s: string): string => s.replace(/\s+/g, ' ').trim()

// Same occurrence rule as applySummaryMarks: take the anchored one, fall back
// to the first when the anchor outruns the text.
function locateOccurrence(plain: string, needle: string, want: number): number {
  let idx = -1
  let from = 0
  for (let n = 0; n <= want; n++) {
    idx = plain.indexOf(needle, from)
    if (idx === -1) break
    from = idx + 1
  }
  if (idx === -1 && want > 0) idx = plain.indexOf(needle)
  return idx
}

// A partial delete can strand a list marker or half a bold pair.
function tidyMarkdown(md: string): string {
  return md
    .split('\n')
    .map((line) => ((line.split('**').length - 1) % 2 ? line.replace(/\*\*/g, '') : line))
    .filter((line) => !/^\s*(?:[-*+]|#{1,6})\s*$/.test(line))
    .join('\n')
    .replace(/\n{3,}/g, '\n\n')
}

// Fold the rewrite/delete revisions into the Markdown source. Returns the
// revisions it could not place so the caller can say so instead of handing
// over a file that is quietly missing them.
export function applySummaryRevisions(md: string, comments: Comment[]): { text: string; unapplied: Comment[] } {
  // Match the normalized source used by the on-screen Markdown renderer.
  md = normalizeBold(md)
  const { plain, map } = projectMarkdown(md)
  const edits: { start: number; end: number; text: string; comment: Comment }[] = []
  const unapplied: Comment[] = []
  for (const c of comments) {
    if (c.target !== 'summary' || !c.quote) continue
    if (c.kind !== 'rewrite' && c.kind !== 'delete') continue
    const needle = normWs(c.quote)
    if (!needle) continue
    const want = c.anchor?.startsWith('occ:') ? parseInt(c.anchor.slice(4)) || 0 : 0
    const at = locateOccurrence(plain, needle, want)
    if (at === -1) {
      unapplied.push(c)
      continue
    }
    edits.push({ start: map[at], end: map[at + needle.length - 1] + 1, text: c.kind === 'delete' ? '' : c.body, comment: c })
  }
  // Splice back-to-front so earlier offsets stay valid; skip overlaps.
  edits.sort((a, b) => b.start - a.start)
  let out = md
  let lastStart = Infinity
  for (const e of edits) {
    if (e.end > lastStart) {
      unapplied.push(e.comment)
      continue
    }
    out = out.slice(0, e.start) + e.text + out.slice(e.end)
    lastStart = e.start
  }
  // Never return partially redacted text when a revision could not be applied.
  return { text: unapplied.length ? '' : tidyMarkdown(out), unapplied }
}
