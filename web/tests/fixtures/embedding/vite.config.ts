import { defineConfig } from 'vite';
import { platformHostBuild } from '@gopherex/backplane-plugin-build/federation';
export default defineConfig(platformHostBuild({ base: '/backplane/' }));
