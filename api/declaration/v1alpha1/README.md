# Declaration spec v1alpha1

The normative specification of garam-bot's interface 1, the declaration spec, at version `v1alpha1`: the kinds Bot, Workspace, WorkspaceClass and BotPool, read the same from a Kubernetes API server and from a file. The key words MUST, MUST NOT, SHOULD and MAY are used as RFC 2119 defines them. The conformance tests are `conformance.md` beside this file.

## Contents
- Scope and implementations
- Kinds
- Schema
- Ownership marker
- Secret references
- Images
- Status
- File form
- Versioning and compatibility
- Open questions

## Scope and implementations

An implementation of this interface admits declarations: it takes an object of a declared kind and either holds it as admitted, with the schema's defaults applied, or refuses it. A reader is a program that acts on admitted objects. Reader obligations are marked as such.

The first two implementations, which the conformance suite runs against:

| Implementation | Form | Where |
|---|---|---|
| A Kubernetes API server serving the CustomResourceDefinitions in `crds/` | CRD, written by `kubectl`, GitOps or `garamsh/garam-bot-operator`'s connector | `TestAPIServer` in `conformance/`, on envtest |
| `manifest.Parse` | File, read by garam-bot from a path | `TestFileForm` in `conformance/` |

`crds/` and the Go types in this package are the schema. `crds/` is generated from the types (`make generate`), and `garamsh/garam-bot-operator` installs it from its chart and imports the types from `github.com/garamsh/garam-bot/api/declaration/v1alpha1`.

## Kinds

- **SCH-1.** The kinds are Bot, Workspace, WorkspaceClass and BotPool, in the API group `bot.garam.sh` at version `v1alpha1`. Each MUST be namespaced. An implementation MUST admit every object the schema admits.
  - **Bot**: who the bot is (`persona`), the agent harness it works with (`agent.harness`), the workspaces it attaches (`workspaces`), what it may do (`permissions`), and where its process runs (`placement`, `pool`).
  - **Workspace**: one workspace that outlives a session, made from a WorkspaceClass and attached to bots by reference.
  - **WorkspaceClass**: how workspaces of one kind are made: the workspace provider, the resources each gets, and when an idle one sleeps.
  - **BotPool**: the pods the Shared bots that name it run in, and how many bots one pod runs.
- A Bot attaches a workspace in one of two ways, as a PersistentVolumeClaim attaches storage: `ref`, an existing Workspace held `Exclusive` (one bot reads and writes it) or `SharedRead` (any number of bots read it); or `template`, a workspace made from a WorkspaceClass for that bot alone, living for one `Session` or for as long as the `Bot` exists.
- A Bot's `placement` is `Shared` by default. `Dedicated`, a pod of its own, is the exception, and a Dedicated bot names no `pool`.
- A Bot's `permissions` are `tools`, each `{name}`, `secrets`, each a reference (SEC-1), and `egress`, each `{host}`, a DNS name whose last label begins with a letter, so never an IP address. Tools and egress are lists of objects keyed by `name` and `host`, so a later release can add a field to one entry.
- A WorkspaceClass's `cpu`, `memory` and `disk` are each more than zero.
- **WS-1.** A Workspace is held by at most one Bot with `Exclusive` access, and a Workspace held `Exclusive` is held by no other Bot. A reader that finds a second claim MUST NOT attach the Workspace to the later Bot, by `metadata.creationTimestamp` and then name, and SHOULD report the conflict on that Bot.
- Every kind is namespaced, WorkspaceClass included, unlike the cluster-scoped StorageClass it is modelled on. A program running these kinds in a user's cluster holds namespaced rights only (`garamsh/garam-bot-operator` ADR 0091), and a cluster-scoped kind would need a ClusterRole to read.
- Every reference from one object to another (`ref.name`, `template.class`, `spec.class`, `pool`) names an object in the referring object's own namespace.

## Schema

- **SCH-2.** An implementation MUST refuse an object whose `apiVersion` is not `bot.garam.sh/v1alpha1` or whose `kind` is not one of the four.
- **SCH-3.** An implementation MUST refuse an object carrying a field the schema does not name, in `metadata` or anywhere below `spec`. This is the API server's strict field validation.
- **SCH-4.** An implementation MUST refuse an object that breaks a constraint of the schema: a missing required field, a value outside an enum, a string that breaks its pattern or length, a number outside its bounds, a duplicate in a list of type `set` or `map`, or a CEL rule (`x-kubernetes-validations`) that does not hold. It MUST validate `metadata` as Kubernetes validates an ObjectMeta on a create: a name that is a DNS subdomain, label keys and values, annotation keys and their total size, finalizers and `generateName`, and no `resourceVersion`. The file form requires `metadata.name`, where the API server also takes `generateName` alone.
- **SCH-5.** An implementation MUST apply the schema's defaults before it validates: `placement` is `Shared`, and a `ref`'s `access` is `Exclusive`.
- **SCH-6.** A declaration's `status` is not part of it. For Bot and Workspace, the kinds with a status subresource, an implementation MUST NOT refuse an object for carrying a status whose fields the status schema names, and MUST NOT take that status from the declaration. A status naming a field the schema does not is refused under SCH-3. WorkspaceClass and BotPool have no `status`, so a declaration of either that carries one is refused under SCH-3.

## Ownership marker

Each object has exactly one owner, `garam` or the cluster (`garamsh/garam-bot-operator` ADR 0089). The marker is metadata:

| Key | Kind | Value |
|---|---|---|
| `bot.garam.sh/managed-by` | label | `garam` or `cluster` |
| `bot.garam.sh/cluster` | annotation | the GRN of the cluster record that applied the object, `grn:<org>/iam/cluster/<id>` |
| `bot.garam.sh/declaration` | annotation | the garam declaration's identifier: 1 to 256 visible ASCII characters |
| `bot.garam.sh/declaration-generation` | annotation | the declaration's generation, a decimal integer from 1 with no sign or leading zero |

- **OWN-1.** An object without the `bot.garam.sh/managed-by` label, or with the value `cluster`, is the cluster's. Any other value is malformed.
- **OWN-2.** A garam-owned object MUST carry all three annotations, each in the form above.
- **OWN-3.** A cluster-owned object MUST NOT carry any of the three annotations.
- **OWN-4.** Admission does not check the marker: a CEL rule cannot read labels or annotations. An implementation MUST admit an object whose marker is malformed if the rest of it is admissible. A reader that acts on ownership MUST NOT act on an object with a malformed marker as if it had either owner. `ManagementOf` in this package is the parser, and `MarkGaramOwned` writes the marker.

## Secret references

- **SEC-1.** A secret appears in a declaration only as a reference, `<secret-name>/<key>`: a Secret's name, which is a DNS subdomain of at most 253 characters, then one data key of it, `[-._a-zA-Z0-9]+`, of at most 253 characters and neither `.` nor `..`. An implementation MUST refuse any other form, including one naming a namespace. No field holds a secret value. `ParseSecretKeyRef` in this package splits a reference.
- **SEC-2.** A reader MUST resolve a reference in the namespace of the object that holds it, and nowhere else.

## Images

- **IMG-1.** No field of any kind names a container image, and an implementation MUST refuse an object that carries one (SCH-3 refuses it as an unknown field). Which images run is the configuration of what runs the objects, not of the declaration (`garamsh/garam-bot-operator` ADR 0091).

## Status

Bot and Workspace have a status subresource, written by what runs them: `observedGeneration` and `conditions`.

- **STA-1.** What runs a Bot MUST set its condition `Running` to `True` while the bot's process runs, and to `False` otherwise, with `observedGeneration` the generation of the spec it runs. How a runner observes the process is the runtime's status contract.

No Workspace condition is defined yet (Open questions).

## File form

The file form is one object in one file, as YAML or JSON, with the same schema as the CRD form. A file needs no `metadata.namespace`. A reader running it places it in a namespace of its own choosing, in which SEC-2 resolves.

- **FILE-1.** A file MUST hold exactly one YAML or JSON document. An implementation MUST refuse a file holding none, more than one, or a document that is not one object, and MUST refuse a mapping that repeats a key.

## Versioning and compatibility

- **A release of `v1alpha1` may add** an optional field, a kind, a value to an enum, or a reader obligation that every conforming reader already meets. Each leaves every object admitted before admitted, with the same meaning.
- **A new version is required** to remove or rename a field or a kind, make an optional field required, tighten a constraint, change a default, or change what a field means.
- **VER-1. Releases are numbered and visible.** Each release of `v1alpha1` has a number, `SpecRelease` in this package, from `0.1.0`. Each CustomResourceDefinition MUST carry it as the annotation `bot.garam.sh/spec-release`, and the `api/` module is tagged `api/v<SpecRelease>` at that release.
- **Skew between a writer and what a cluster installed.** The group and version string is the same across releases, so it does not tell a writer which release a cluster serves:
  - A writer MUST apply under strict field validation (`fieldValidation=Strict`), so a field the installed release does not name is refused rather than pruned. Pruned, a field that restricts a bot would leave it running with more than was declared.
  - A writer MUST NOT write a field newer than the `bot.garam.sh/spec-release` the target cluster's definitions carry.
- **VER-2.** A reader MUST NOT act on an object carrying an enum value it does not know, as if it carried a value it does know. This is what makes adding an enum value a compatible release.
- **The next version.** No program serving these kinds runs a conversion webhook (`garamsh/garam-bot-operator` ADR 0091), so a definition serving two versions uses `conversion: None`, which converts nothing but `apiVersion`.
  - **`v1beta1` beside `v1alpha1`.** `v1beta1` is served beside `v1alpha1` only while every `v1alpha1` object is also a valid `v1beta1` object with the same meaning: additive changes only.
  - **A change that needs conversion** (a rename, a type change, a tightened constraint) is a drop and rewrite:
    1. the new version is served beside the old, and becomes the storage version;
    2. writers rewrite every object to it;
    3. the old version leaves `status.storedVersions` of the definition;
    4. only then is the old version removed.
  - The next alpha is `v1alpha2`, by the same drop and rewrite.
- **A deprecated element** is marked deprecated in this specification and in its Go doc comment, stays in its version, and is removed only by a new version.
- **The first stable version, `v1`,** needs two production implementations passing the conformance suite.
- `garamsh/garam-bot-operator`'s `interfaces/cluster-connection` carries these kinds at this version. A new version of this interface reaches that protocol as its own rules state.

## Open questions

These are not decided here.

- **Triggers.** What starts a bot's work (mail, a schedule, an event), and whether the Bot declares it.
- **Tools and their credentials.** Whether a tool names the secret reference it authenticates with, beside `permissions.secrets`. Answering it adds a field to a `tools` entry.
- **Egress beyond host names.** Ports, protocols, wildcards and address ranges. Each adds a field to an `egress` entry.
- **Workspace conditions.** Which condition a workspace provider sets, such as `Ready` or `Asleep` after `idle.sleepAfter`.
- **Model selection.** Whether a Bot names a model or a model gateway, which is interface 5's.
- **Provider parameters.** Whether a WorkspaceClass carries provider-specific parameters, as a StorageClass does, and how IMG-1 holds for them.
- **BotPool membership.** Whether the pool is named by each Bot or chosen by the namespace, and how many pools a namespace holds.
- **Handover.** Moving a Workspace from one bot to another.
- **Density targets.** How many bots one pod and workspaces one host run. `maxBotsPerPod` bounds it at 1000 for now.
- **Terms of service.** Whether wrapping each agent harness is allowed; that is interface 2's question, recorded here because the harness is named in a Bot.
