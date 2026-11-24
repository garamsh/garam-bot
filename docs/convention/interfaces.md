# Interface Conventions

How a boundary between separately built components is specified and tested, so no single implementation shapes it.

## Scope

The rule binds a boundary between components that are built, released or deployed separately: a plugin or driver API, a protocol between separate services, an interface that implementations outside the project take.

It does not cover a boundary inside one build and one release: a module, a layer, a package.

## Rules

- **The boundary is a versioned public interface.** It has a written specification and a version, and a consumer depends on the version, not on an implementation.
- **Each interface has a conformance test.** It runs against any implementation and passes or fails it through the interface alone, reaching into no implementation's internals.
- **More than one implementation from the start**, so the interface is not shaped by the first one. In pre-stable releases, a reference implementation the conformance test runs against, such as a fake or in-memory one, counts as the second. The first stable version (v1 under semver) needs two production implementations that pass the conformance test.
- **The specification states its compatibility rules.** What a release may change without breaking an implementation, what forces a major version, and how a deprecated element is retired.
