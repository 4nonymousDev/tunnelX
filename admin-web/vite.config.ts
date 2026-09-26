import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue(), {
    name: 'normalize-html-lines',
    transformIndexHtml: {
      order: 'post',
      handler: (html) => html.replace(/\r\n?/g, '\n').replace(/[\t ]+$/gm, ''),
    },
  }],
  build: {
    outDir: '../internal/adminapi/web/dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:2223',
    },
  },
})
