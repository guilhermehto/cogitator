import { defineConfig } from 'vitepress'

// Deployed to GitHub Pages at https://guilhermehto.github.io/cogitator/
export default defineConfig({
  title: 'cogitator',
  description:
    'Monitor coding agents, spin up git worktrees, jump between them — all from one place.',
  base: '/cogitator/',
  lang: 'en-US',
  cleanUrls: true,
  lastUpdated: true,
  appearance: 'dark',

  head: [
    ['link', { rel: 'icon', type: 'image/svg+xml', href: '/cogitator/logo.svg' }],
    ['meta', { name: 'theme-color', content: '#5f5fff' }],
    ['meta', { property: 'og:title', content: 'cogitator' }],
    [
      'meta',
      {
        property: 'og:description',
        content:
          'A terminal dashboard for coding agents: live attention signals, git worktrees, tmux navigation.',
      },
    ],
  ],

  themeConfig: {
    logo: '/logo.svg',

    nav: [
      { text: 'Guide', link: '/guide/getting-started', activeMatch: '^/guide/' },
      { text: 'Reference', link: '/reference/live-attention', activeMatch: '^/(reference/|codex|omp)' },
    ],

    sidebar: [
      {
        text: 'Guide',
        items: [
          { text: 'Getting started', link: '/guide/getting-started' },
          { text: 'Connect your agent', link: '/guide/connect' },
          { text: 'Key bindings', link: '/guide/key-bindings' },
          { text: 'Configuration', link: '/guide/configuration' },
          { text: 'Taskwarrior', link: '/guide/taskwarrior' },
        ],
      },
      {
        text: 'Reference',
        items: [
          { text: 'Live attention', link: '/reference/live-attention' },
          { text: 'CLI & logging', link: '/reference/cli' },
          { text: 'Architecture', link: '/reference/architecture' },
          { text: 'Codex deep dive', link: '/codex' },
          { text: 'omp deep dive', link: '/omp' },
        ],
      },
    ],

    socialLinks: [{ icon: 'github', link: 'https://github.com/guilhermehto/cogitator' }],

    search: { provider: 'local' },

    editLink: {
      pattern: 'https://github.com/guilhermehto/cogitator/edit/main/docs/:path',
      text: 'Edit this page on GitHub',
    },

    footer: {
      message: 'Released under the MIT License.',
      copyright: 'Copyright © Guilherme Oliveira',
    },

    outline: { level: [2, 3] },
  },
})
