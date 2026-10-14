import { defineConfig } from 'vite';
import { platformHostBuild } from '@backplane-labs/backplane-plugin-build/federation';
export default defineConfig(platformHostBuild({ base: '/backplane/' }));
