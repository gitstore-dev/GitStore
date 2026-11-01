import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import {
  PreferencesProvider,
  effectiveTheme,
  readTheme,
  usePreferences,
} from './preferences'
function Controls() {
  const p = usePreferences()
  return (
    <>
      <button onClick={() => p.setTheme('dark')}>Dark</button>
      <button onClick={() => p.setCollapsed(true)}>Collapse</button>
      <button onClick={() => p.setMotion('reduced')}>Reduce</button>
    </>
  )
}
describe('browser preferences', () => {
  it('rejects invalid stored themes and resolves system mode', () => {
    localStorage.setItem('gitstore.theme', 'invalid')
    expect(readTheme()).toBe('system')
    expect(effectiveTheme('system', true)).toBe('dark')
    expect(effectiveTheme('light', true)).toBe('light')
  })
  it('applies and persists theme, sidebar, and reduced-motion choices', () => {
    render(
      <PreferencesProvider>
        <Controls />
      </PreferencesProvider>,
    )
    fireEvent.click(screen.getByText('Dark'))
    fireEvent.click(screen.getByText('Collapse'))
    fireEvent.click(screen.getByText('Reduce'))
    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(document.documentElement.dataset.motion).toBe('reduced')
    expect(localStorage.getItem('gitstore.theme')).toBe('dark')
    expect(localStorage.getItem('gitstore.sidebar')).toBe('collapsed')
  })
})
