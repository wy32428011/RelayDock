import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    include: ['tests/**/*.test.tsx', 'apps/desktop/src/main/**/*.test.ts', 'apps/desktop/src/renderer/**/*.test.tsx'],
    restoreMocks: true,
  },
});
