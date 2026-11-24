# Declaration spec v1alpha1 conformance

The tests of `README.md`'s requirements: what each one is, and how a new implementation runs them.

## Running the suite

- **Admission suite**, `conformance/`: every case in `Cases()`, each a subtest named `<requirement>/<case>`. An implementation plugs in as a `conformance.Admitter`, which admits one object into a namespace and returns its spec as admitted, and runs `conformance.Run(t, admitter)`.
  - `TestFileForm` runs it against `manifest.Parse`.
  - `TestAPIServer` runs it against a Kubernetes API server serving `crds/`, by dry-run create under strict field validation. It needs `KUBEBUILDER_ASSETS`, which `make test` sets through setup-envtest. Without it, the test skips.
- **Schema and marker tests**, in this package and `manifest/`: what admission cannot reach, run by `go test` in the `api/` module.
- **Every admitted case** also asserts that the admitted object kept no status from the declaration (SCH-6), so an Admitter returns the whole object as admitted.
- A pending case is skipped with its reason. It counts as named, not as passing.
- **Reader conformance** (SEC-2, WS-1, VER-2, STA-1, and the reader half of OWN-4) has no harness here yet. garam-bot's runtime that runs one Bot (#1198, #1199) reads a Bot but resolves no secret, attaches no workspace, branches on no enum and writes no condition, so it meets none of these obligations yet. The `Reader` interface arrives with the first reader that acts on one of them: the runtime once it attaches workspaces or resolves secrets, or garam-bot-operator's Bot reconciler for STA-1. Until then those cases are pending names.

## Requirements and their tests

| Requirement | Tests |
|---|---|
| SCH-1 | `Run`: `SCH-1/*`, five kinds admitted. `TestCRDs_SCH1_DefineTheFourKindsNamespacedInOneGroupVersion` |
| SCH-2 | `Run`: `SCH-2/*` |
| SCH-3 | `Run`: `SCH-3/*`. `TestParse_SCH3_RefusesAnUnknownMetadataField` |
| SCH-4 | `Run`: `SCH-4/*`, metadata cases included. `TestRules_EveryRuleOfTheDefinitionsHasAnImplementation`, which holds the file form to every CEL rule of `crds/` |
| SCH-5 | `Run`: `SCH-5/placement defaults to Shared and access to Exclusive`. `TestParse_ReturnsTheTypedObjectWithDefaults` |
| SCH-6 | `Run`: `SCH-6/*`, and the status assertion on every admitted case |
| OWN-1 | `TestManagementOf_OWN1_AnObjectWithoutTheLabelIsTheClusters`, `TestManagementOf_OWN1_AnotherLabelValueIsRefused` |
| OWN-2 | `TestManagementOf_OWN2_AGaramOwnedObjectNamesItsDeclaration`, `TestManagementOf_OWN2_AGaramOwnedObjectWithAMalformedDeclarationIsRefused`, `TestMarkGaramOwned_*` |
| OWN-3 | `TestManagementOf_OWN3_AClusterOwnedObjectCarryingADeclarationIsRefused` |
| OWN-4 | `Run`: `OWN-4/a malformed ownership marker does not refuse the object`. The reader half: `ManagementOf` refuses a malformed marker (OWN-1 to OWN-3 above) |
| SEC-1 | `Run`: `SEC-1/*`. `TestParseSecretKeyRef_SEC1_*` |
| SEC-2 | `Run`: `SEC-2/a reference resolves in the object's namespace`, pending a reader suite. The first reader is garam-bot's runtime that runs one Bot |
| WS-1 | `Run`: `WS-1/a second Exclusive claim on a Workspace does not attach it`, pending a reader suite |
| VER-1 | `TestCRDs_VER1_EachNamesTheSpecRelease` |
| VER-2 | `Run`: `VER-2/a reader does not act on an enum value it does not know`, pending a reader suite |
| IMG-1 | `Run`: `IMG-1/*`. `TestCRDs_IMG1_NoPropertyNamesAnImage` |
| STA-1 | `Run`: `STA-1/Running is True while the bot's process runs`, pending a runner suite: the runtime's status contract is `docs/architecture/runtime.md`, and the manager that writes the condition from it is garam-bot-operator's Bot reconciler |
| FILE-1 | `TestParse_FILE1_AdmitsOneJSONDocument`, `TestParse_FILE1_RefusesAFileThatIsNotOneDocument` |
