import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

// Standalone widget regressions using the typed bridge and synthetic fixtures only.
export default defineConfig({
  plugins: [react()],
  test: { environment: 'jsdom', include: ['apps/desktop/src/renderer/widgets/widgets.test.tsx'], restoreMocks: true },
});
