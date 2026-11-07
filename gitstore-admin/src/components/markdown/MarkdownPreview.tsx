import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import styles from './markdown.module.css'
export function MarkdownPreview({ source }: { source: string }) {
  return (
    <div
      className={styles.preview}
      role="region"
      aria-label="Description preview"
    >
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        skipHtml
        components={{
          a: ({ children, ...props }) => (
            <a {...props} rel="noreferrer">
              {children}
            </a>
          ),
        }}
      >
        {source}
      </ReactMarkdown>
    </div>
  )
}
