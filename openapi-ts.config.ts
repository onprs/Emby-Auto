import { defineConfig } from '@hey-api/openapi-ts';

export default defineConfig({
  input: './contracts/openapi.yaml',
  output: './apps/web/src/api/generated',
});
