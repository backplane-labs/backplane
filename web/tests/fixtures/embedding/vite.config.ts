import { platformHostBuild } from '@backplane-labs/backplane-plugin-build/federation';
import { defineConfig } from 'vite';
export default defineConfig(platformHostBuild({ base: '/backplane/' }));
