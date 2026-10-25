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

`Offer` and `Follow` are declarations: call them while the service is
declared (in a constructor), not in a component's goroutine. The followed
names are read when the service starts, so `Follow` after `Run` panics
instead of silently following nothing.

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

## Live sections

A section the manifest holds is fixed for a version. When the value follows
something that changes while the service runs — its live configuration, say —
offer it live and set it whenever that changes:

```go
login := sections.OfferLive[*kratosv1.LoginSection](root, "kratos.login")

// on start and on every settings change
if err := login.Set(section(settings)); err != nil { ... }
```

The manifest declares the name with `T`'s empty message. Each instance
publishes the value it set with its instance state, at once; Backplane takes
the value of a serving instance of the version it publishes and, when it
changes, rewrites the name's key, so followers see `Changes`. Until an
instance sets a value, followers see the empty message. Setting an equal
value changes nothing.

Declare `Offer` and `Follow` before `Run`, like routes and hooks. A section
that does not marshal is a declaration error that `Run` fails with and
`Service.Validate` reports.

In tests, `backplanetest.Offered(h, name)` shows what a service offers (its
live value when one is set, else the manifest's), and
`backplanetest.SetSections(h, name, services)` publishes a set to the
harness's followers as Backplane would (see [Testing](testing.md)).
