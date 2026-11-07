import { unified } from 'unified'
import remarkParse from 'remark-parse'
import remarkGfm from 'remark-gfm'
const parser = unified().use(remarkParse).use(remarkGfm)
/** Compare content, not serializer choices. Positions are editor diagnostics only. */
export function markdownShape(source: string): string {
  return JSON.stringify(parser.parse(source), (key, value: unknown) =>
    key === 'position' ? undefined : value,
  )
}
export function equivalentMarkdown(a: string, b: string) {
  return markdownShape(a) === markdownShape(b)
}
export function sourceOnlyReason(source: string): string | null {
  const tree = parser.parse(source)
  let html = false
  function visit(node: unknown) {
    if (!node || typeof node !== 'object') return
    if ('type' in node && node.type === 'html') html = true
    if ('children' in node && Array.isArray(node.children))
      node.children.forEach(visit)
  }
  visit(tree)
  if (html)
    return 'This description contains HTML. Use Markdown mode to preserve it; preview never executes HTML.'
  return null
}
