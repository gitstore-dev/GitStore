import type { StorybookConfig } from '@storybook/react-vite'
const config: StorybookConfig = {
  stories: ['../src/**/*.stories.tsx'],
  addons: ['@storybook/addon-docs', '@storybook/addon-a11y'],
  framework: '@storybook/react-vite',
  staticDirs: ['../public'],
  core: { disableTelemetry: true },
}
export default config
