import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MarkdownPreview } from './MarkdownPreview'
import { equivalentMarkdown, sourceOnlyReason } from './model'
describe('Markdown source boundary', () => {
  it('compares semantic structure without source offsets or normalizing caller bytes', () => {
    expect(equivalentMarkdown('# Title\n\n*one*', '# Title\n\n_one_\n')).toBe(
      true,
    )
    expect(equivalentMarkdown('- [x] Task', '- [ ] Task')).toBe(false)
    expect(
      equivalentMarkdown(
        '[one](https://example.com)',
        '[two](https://example.com)',
      ),
    ).toBe(false)
    expect(equivalentMarkdown('| A |\n| - |\n| B |', 'A B')).toBe(false)
  })
  it('uses source mode for HTML and never executes raw HTML or unsafe preview links', () => {
    expect(sourceOnlyReason('<script>alert(1)</script>')).toMatch(/HTML/)
    expect(sourceOnlyReason('## Product\n\n- Natural clay')).toBeNull()
    const output = renderToStaticMarkup(
      <MarkdownPreview
        source={
          '<script>alert(1)</script>\n\n[link](javascript:alert%281%29)\n\n![image](javascript:alert%281%29)'
        }
      />,
    )
    expect(output).not.toContain('<script>')
    expect(output).not.toContain('javascript:')
  })
})
