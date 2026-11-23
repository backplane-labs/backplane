/** A navigation group is named by a translation key in the plugin's own namespace. */
export function assertGroupLabelKey(key: string | undefined) {
  if (key !== undefined && (typeof key !== 'string' || !key.trim())) {
    throw new Error('Invalid navigation group translation key');
  }
}
