import React, { useState } from 'react';

interface MarkdownEditorProps {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  disabled?: boolean;
  rows?: number;
}

/**
 * Markdown editor component with live preview and formatting toolbar
 * Supports basic Markdown syntax with preview toggle
 */
export function MarkdownEditor({
  value,
  onChange,
  placeholder = 'Write your content in Markdown...',
  disabled = false,
  rows = 10,
}: MarkdownEditorProps) {
  const [showPreview, setShowPreview] = useState(false);

  const insertMarkdown = (prefix: string, suffix: string = '') => {
    const textarea = document.getElementById('markdown-textarea') as HTMLTextAreaElement;
    if (!textarea) return;

    const start = textarea.selectionStart;
    const end = textarea.selectionEnd;
    const selectedText = value.substring(start, end);
    const beforeText = value.substring(0, start);
    const afterText = value.substring(end);

    const newText = beforeText + prefix + selectedText + suffix + afterText;
    onChange(newText);

    // Restore cursor position
    setTimeout(() => {
      textarea.focus();
      const newCursorPos = start + prefix.length + selectedText.length;
      textarea.setSelectionRange(newCursorPos, newCursorPos);
    }, 0);
  };

  const handleBold = () => insertMarkdown('**', '**');
  const handleItalic = () => insertMarkdown('*', '*');
  const handleHeading = () => insertMarkdown('## ');
  const handleLink = () => insertMarkdown('[', '](url)');
  const handleCode = () => insertMarkdown('`', '`');
  const handleCodeBlock = () => insertMarkdown('```\n', '\n```');
  const handleList = () => insertMarkdown('- ');
  const handleNumberedList = () => insertMarkdown('1. ');
  const handleQuote = () => insertMarkdown('> ');

  // Simple markdown to HTML converter (basic support)
  const renderMarkdown = (text: string): string => {
    let html = text;

    // Code blocks
    html = html.replace(/```([^`]+)```/g, '<pre><code>$1</code></pre>');

    // Inline code
    html = html.replace(/`([^`]+)`/g, '<code>$1</code>');

    // Bold
    html = html.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');

    // Italic
    html = html.replace(/\*([^*]+)\*/g, '<em>$1</em>');

    // Headers
    html = html.replace(/^### (.+)$/gm, '<h3>$1</h3>');
    html = html.replace(/^## (.+)$/gm, '<h2>$1</h2>');
    html = html.replace(/^# (.+)$/gm, '<h1>$1</h1>');

    // Links
    html = html.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>');

    // Unordered lists
    html = html.replace(/^\- (.+)$/gm, '<li>$1</li>');
    html = html.replace(/(<li>.*<\/li>)/s, '<ul>$1</ul>');

    // Ordered lists
    html = html.replace(/^\d+\. (.+)$/gm, '<li>$1</li>');

    // Blockquotes
    html = html.replace(/^> (.+)$/gm, '<blockquote>$1</blockquote>');

    // Line breaks
    html = html.replace(/\n/g, '<br>');

    return html;
  };

  return (
    <div style={styles.container}>
      {/* Toolbar */}
      <div style={styles.toolbar}>
        <div style={styles.toolbarGroup}>
          <button
            type="button"
            onClick={handleBold}
            style={styles.toolbarButton}
            disabled={disabled}
            title="Bold (Ctrl+B)"
          >
            <strong>B</strong>
          </button>
          <button
            type="button"
            onClick={handleItalic}
            style={styles.toolbarButton}
            disabled={disabled}
            title="Italic (Ctrl+I)"
          >
            <em>I</em>
          </button>
          <button
            type="button"
            onClick={handleHeading}
            style={styles.toolbarButton}
            disabled={disabled}
            title="Heading"
          >
            H
          </button>
        </div>

        <div style={styles.toolbarGroup}>
          <button
            type="button"
            onClick={handleLink}
            style={styles.toolbarButton}
            disabled={disabled}
            title="Link"
          >
            🔗
          </button>
          <button
            type="button"
            onClick={handleCode}
            style={styles.toolbarButton}
            disabled={disabled}
            title="Inline Code"
          >
            {'</>'}
          </button>
          <button
            type="button"
            onClick={handleCodeBlock}
            style={styles.toolbarButton}
            disabled={disabled}
            title="Code Block"
          >
            {'{ }'}
          </button>
        </div>

        <div style={styles.toolbarGroup}>
          <button
            type="button"
            onClick={handleList}
            style={styles.toolbarButton}
            disabled={disabled}
            title="Bullet List"
          >
            •
          </button>
          <button
            type="button"
            onClick={handleNumberedList}
            style={styles.toolbarButton}
            disabled={disabled}
            title="Numbered List"
          >
            1.
          </button>
          <button
            type="button"
            onClick={handleQuote}
            style={styles.toolbarButton}
            disabled={disabled}
            title="Quote"
          >
            "
          </button>
        </div>

        <div style={styles.toolbarGroup}>
          <button
            type="button"
            onClick={() => setShowPreview(!showPreview)}
            style={{
              ...styles.toolbarButton,
              ...(showPreview ? styles.toolbarButtonActive : {}),
            }}
            disabled={disabled}
            title="Toggle Preview"
          >
            👁
          </button>
        </div>
      </div>

      {/* Editor/Preview Area */}
      <div style={styles.editorContainer}>
        {!showPreview ? (
          <textarea
            id="markdown-textarea"
            value={value}
            onChange={(e) => onChange(e.target.value)}
            placeholder={placeholder}
            disabled={disabled}
            rows={rows}
            style={styles.textarea}
          />
        ) : (
          <div
            style={styles.preview}
            dangerouslySetInnerHTML={{ __html: renderMarkdown(value) }}
          />
        )}
      </div>

      {/* Help Text */}
      <div style={styles.helpText}>
        <span style={styles.helpLabel}>Markdown supported:</span>
        <span style={styles.helpItem}>**bold**</span>
        <span style={styles.helpItem}>*italic*</span>
        <span style={styles.helpItem}># heading</span>
        <span style={styles.helpItem}>[link](url)</span>
        <span style={styles.helpItem}>`code`</span>
        <span style={styles.helpItem}>- list</span>
      </div>
    </div>
  );
}

const styles = {
  container: {
    display: 'flex',
    flexDirection: 'column',
    border: '1px solid #e2e8f0',
    borderRadius: '4px',
    overflow: 'hidden',
  } as React.CSSProperties,
  toolbar: {
    display: 'flex',
    gap: '0.5rem',
    padding: '0.5rem',
    backgroundColor: '#f7fafc',
    borderBottom: '1px solid #e2e8f0',
    flexWrap: 'wrap',
  } as React.CSSProperties,
  toolbarGroup: {
    display: 'flex',
    gap: '0.25rem',
  } as React.CSSProperties,
  toolbarButton: {
    padding: '0.5rem 0.75rem',
    backgroundColor: 'white',
    color: '#4a5568',
    border: '1px solid #e2e8f0',
    borderRadius: '4px',
    fontSize: '0.875rem',
    fontWeight: 500,
    cursor: 'pointer',
    transition: 'all 0.2s',
    minWidth: '36px',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
  } as React.CSSProperties,
  toolbarButtonActive: {
    backgroundColor: '#667eea',
    color: 'white',
    borderColor: '#667eea',
  } as React.CSSProperties,
  editorContainer: {
    minHeight: '200px',
  } as React.CSSProperties,
  textarea: {
    width: '100%',
    padding: '1rem',
    border: 'none',
    outline: 'none',
    fontSize: '0.875rem',
    fontFamily: 'monospace',
    lineHeight: '1.6',
    resize: 'vertical',
  } as React.CSSProperties,
  preview: {
    padding: '1rem',
    minHeight: '200px',
    fontSize: '0.875rem',
    lineHeight: '1.6',
    color: '#1a202c',
  } as React.CSSProperties,
  helpText: {
    display: 'flex',
    flexWrap: 'wrap',
    gap: '0.5rem',
    padding: '0.5rem 1rem',
    backgroundColor: '#f7fafc',
    borderTop: '1px solid #e2e8f0',
    fontSize: '0.75rem',
    color: '#718096',
  } as React.CSSProperties,
  helpLabel: {
    fontWeight: 600,
    marginRight: '0.5rem',
  } as React.CSSProperties,
  helpItem: {
    padding: '0.125rem 0.5rem',
    backgroundColor: 'white',
    borderRadius: '4px',
    fontFamily: 'monospace',
  } as React.CSSProperties,
};

// Global styles for preview content
const previewStyles = `
  .markdown-preview h1 {
    font-size: 2rem;
    font-weight: 700;
    margin: 1.5rem 0 1rem;
  }
  .markdown-preview h2 {
    font-size: 1.5rem;
    font-weight: 600;
    margin: 1.25rem 0 0.75rem;
  }
  .markdown-preview h3 {
    font-size: 1.25rem;
    font-weight: 600;
    margin: 1rem 0 0.5rem;
  }
  .markdown-preview p {
    margin: 0.75rem 0;
  }
  .markdown-preview ul, .markdown-preview ol {
    margin: 0.75rem 0;
    padding-left: 2rem;
  }
  .markdown-preview li {
    margin: 0.25rem 0;
  }
  .markdown-preview code {
    padding: 0.125rem 0.25rem;
    background-color: #f7fafc;
    border-radius: 4px;
    font-family: monospace;
    font-size: 0.875em;
  }
  .markdown-preview pre {
    padding: 1rem;
    background-color: #1a202c;
    color: #48bb78;
    border-radius: 4px;
    overflow-x: auto;
    margin: 1rem 0;
  }
  .markdown-preview pre code {
    padding: 0;
    background-color: transparent;
    color: inherit;
  }
  .markdown-preview blockquote {
    padding-left: 1rem;
    border-left: 4px solid #e2e8f0;
    color: #718096;
    font-style: italic;
    margin: 1rem 0;
  }
  .markdown-preview a {
    color: #667eea;
    text-decoration: underline;
  }
  .markdown-preview a:hover {
    color: #5568d3;
  }
`;

// Inject preview styles
if (typeof document !== 'undefined') {
  const styleElement = document.getElementById('markdown-preview-styles');
  if (!styleElement) {
    const style = document.createElement('style');
    style.id = 'markdown-preview-styles';
    style.textContent = previewStyles;
    document.head.appendChild(style);
  }
}
