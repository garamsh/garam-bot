# garam-connector

How to install the chart that connects a cluster to garam and runs garam-bot's bots in it. What it renders, the rights each program holds and the limits of its one-release-per-namespace refusal are `operator/docs/architecture/connector.md`.

## Installing

Each watched namespace must exist first. Create the registration token's Secret and the ConfigMap holding garam's root certificate in the release namespace, then install:

```sh
kubectl -n garam-system create secret generic garam-connector-registration --from-file=token=<token-file>
kubectl -n garam-system create configmap garam-root --from-file=ca.crt=<garam-root.pem>
helm install garam operator/deploy/charts/garam-connector --namespace garam-system \
  --set-json 'watchNamespaces=["game-team"]' \
  --set images.manager=<repository>:<tag>@sha256:<digest> \
  --set images.connector=<repository>:<tag>@sha256:<digest> \
  --set images.runtime=<repository>:<tag>@sha256:<digest> \
  --set apiServer.cidr=<the Kubernetes API's address range> \
  --set connector.garamURL=<garam's base URL> \
  --set connector.garamRoot.configMap=garam-root
```

`values.yaml` describes every value. The chart refuses an image not given as `<repository>:<tag>@sha256:<digest>`.

- **`--skip-crds`** installs without interface 1's CRDs, for a cluster whose CRDs another owner applies.
- **`connector.enabled=false`** installs the manager alone, which needs no garam connection.
- **`connector.reportOnly=true`** applies and deletes nothing garam sends, and still reports.
- **Do not install with `--take-ownership`.** It adopts another release's claim of a watched namespace (`connector.md` §Declarations and ownership).

## Checks

`make chart-check` in `operator/` renders the chart with the pinned helm, checks what it renders, and installs it into an envtest API server (`operator/test/chart`). `make chart-crds` copies the CRDs from the pinned api module into `crds/`, and the check fails where the two differ.
