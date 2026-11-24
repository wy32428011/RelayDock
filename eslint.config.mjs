import js from '@eslint/js';
import tseslint from 'typescript-eslint';

export default [
  { ignores: ['**/dist/**','node_modules/**','build/**','artifacts/**'] },
  { files: ['**/*.mjs'], ...js.configs.recommended,
    languageOptions: { globals: { process: 'readonly', Buffer: 'readonly', console: 'readonly', URL: 'readonly', setTimeout: 'readonly', clearTimeout: 'readonly', setInterval: 'readonly', clearInterval: 'readonly' } } },
  ...tseslint.configs.recommended,
  { files: ['**/*.ts','**/*.tsx'], rules: { '@typescript-eslint/no-unused-vars': ['error',{ argsIgnorePattern: '^_', varsIgnorePattern: '^_', caughtErrorsIgnorePattern: '^_', ignoreRestSiblings: true }] } },
];
