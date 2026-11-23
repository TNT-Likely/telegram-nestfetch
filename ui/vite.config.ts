import { defineConfig } from 'vite';
import { version } from '../src-tauri/tauri.conf.json';

export default defineConfig({
  clearScreen: false,
  define: { 'import.meta.env.VITE_APP_VERSION': JSON.stringify(version) },
  server: { host: '127.0.0.1', port: 1420, strictPort: true },
  build: { target: ['es2022', 'safari16'] },
});
