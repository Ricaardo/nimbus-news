import pluginVue from 'eslint-plugin-vue'

export default [
  ...pluginVue.configs['flat/recommended'],
  {
    files: ['**/*.vue', '**/*.js'],
    rules: {
      'vue/multi-word-component-names': 'off',
      'vue/no-v-html': 'off',
      'vue/require-default-prop': 'off',
      'vue/no-unused-vars': 'error',
      'no-console': 'warn',
      'no-debugger': 'warn',
      'no-unused-vars': 'error',
      'prefer-const': 'error',
      'no-var': 'error'
    }
  },
  {
    ignores: ['node_modules/', 'dist/', '.vite/']
  }
]
