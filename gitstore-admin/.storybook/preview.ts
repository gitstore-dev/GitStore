import type { Preview } from '@storybook/react-vite'
import { MINIMAL_VIEWPORTS } from 'storybook/viewport'
import '../src/styles/global.css'
const preview: Preview = {
  parameters: {
    layout: 'fullscreen',
    a11y: { test: 'error' },
    docs: { story: { inline: false } },
    controls: { expanded: true },
    viewport: { options: MINIMAL_VIEWPORTS },
  },
}
export default preview
