import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { grafanaAssets } from '@backplane-labs/backplane-plugin-build';

export default defineConfig({ plugins: [grafanaAssets(), react()], base: './', resolve: { dedupe: ['react', 'react-dom'] } });
