# Declaration spec v1alpha1 conformance

The tests of `README.md`'s requirements: what each one is, and how a new implementation runs them.

## Running the suite

- **Admission suite**, `conformance/`: every case in `Cases()`, each a subtest named `<requirement>/<case>`. An implementation plugs in as a `conformance.Admitter`, which admits one object into a namespace and returns its spec as admitted, and runs `conformance.Run(t, admitter)`.
  - `TestFileForm` runs it against `manifest.Parse`.
  - `TestAPIServer` runs it against a Kubernetes API server serving `crds/`, by dry-run create under strict field validation. It needs `KUBEBUILDER_ASSETS`, which `make test` sets through setup-envtest. Without it, the test skips.
- **Schema and marker tests**, in this package and `manifest/`: what admission cannot reach, run by `go test` in the `api/` module.
- A pending case is skipped with its reason. It counts as named, not as passing.

## Requirements and their tests

| Requirement | Tests |
|---|---|
| SCH-1 | `Run`: `SCH-1/*`, five kinds admitted. `TestCRDs_SCH1_DefineTheFourKindsNamespacedInOneGroupVersion` |
| SCH-2 | `Run`: `SCH-2/*` |
| SCH-3 | `Run`: `SCH-3/*`. `TestParse_SCH3_RefusesAnUnknownMetadataField` |
| SCH-4 | `Run`: `SCH-4/*`. `TestRules_EveryRuleOfTheDefinitionsHasAnImplementation`, which holds the file form to every CEL rule of `crds/` |
| SCH-5 | `Run`: `SCH-5/placement defaults to Shared and access to Exclusive`. `TestParse_ReturnsTheTypedObjectWithDefaults` |
| SCH-6 | `Run`: `SCH-6/a status in the declaration does not refuse it` |
| OWN-1 | `TestManagementOf_OWN1_AnObjectWithoutTheLabelIsTheClusters`, `TestManagementOf_OWN1_AnotherLabelValueIsRefused` |
| OWN-2 | `TestManagementOf_OWN2_AGaramOwnedObjectNamesItsDeclaration`, `TestManagementOf_OWN2_AGaramOwnedObjectWithAMalformedDeclarationIsRefused`, `TestMarkGaramOwned_*` |
| OWN-3 | `TestManagementOf_OWN3_AClusterOwnedObjectCarryingADeclarationIsRefused` |
| OWN-4 | `Run`: `OWN-4/a malformed ownership marker does not refuse the object`. The reader half: `ManagementOf` refuses a malformed marker (OWN-1 to OWN-3 above) |
| SEC-1 | `Run`: `SEC-1/*`. `TestParseSecretKeyRef_SEC1_*` |
| SEC-2 | `Run`: `SEC-2/a reference resolves in the object's namespace`, pending a reader suite. The first reader is garam-bot's runtime that runs one Bot |
| IMG-1 | `Run`: `IMG-1/*`. `TestCRDs_IMG1_NoPropertyNamesAnImage` |
| STA-1 | `Run`: `STA-1/Running is True while the bot's process runs`, pending the runtime's status contract |
| FILE-1 | `TestParse_FILE1_AdmitsOneJSONDocument`, `TestParse_FILE1_RefusesAFileThatIsNotOneDocument` |
