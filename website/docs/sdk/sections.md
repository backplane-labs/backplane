# Sections: discovery the product defines

A product often needs to know which services offer something of its own: the
modules of an application, the providers of a search, the plugins of a
pipeline. A section is that, without a registry of the product's: a service
offers a proto message of the product's under a name, and any service follows
the name across the installation.

```go
// a module service
sections.Offer(root, "komeet.module", &modulev1.Manifest{Name: "media", Routes: ...})

// the platform service
modules := sections.Follow[*modulev1.Manifest](root, "komeet.module")

for service, m := range modules.Get() {
    // service is the offering service's name, m its section
}
```

Names are lower case, segments joined by `.`, `_` or `-`; the product chooses
them, so prefix them with the product's name. A service offers a name once.

## What a follower sees

The section goes into the offering service's manifest. Backplane publishes, by
name, the sections of every serving service — the highest version with a
registered, healthy, serving instance — to Consul KV
`backplane/sections/<name>`. `Follow` watches that key while the service runs:

- `Get()` is the current set by service; empty until it was read, or when no
  serving service offers the name.
- `Known()` tells an empty set from one not read yet; without Consul nothing
  is ever known.
- `Changes(ctx)` signals each change: a service starting, stopping, or
  shipping a new version with another section.

A service that offers the name with a message other than `T` is left out of
the follower's set.

Declare `Offer` and `Follow` before `Run`, like routes and hooks. A section
that does not marshal is a declaration error that `Run` fails with and
`Service.Validate` reports.
