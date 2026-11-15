/** "console:<uuid>" as "console:<first 8>"; other actors as they are. */
export const shortActor = (actor: string) => (/^console:[0-9a-f-]{36}$/.test(actor) ? actor.slice(0, 16) : actor);
