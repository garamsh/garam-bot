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
- **Garam off**: leave `connector.garamURL` and `connector.garamRoot.configMap` unset, and skip the registration token's Secret and garam's root. The connector registers nothing and serves the bots' workspaces alone, so this is the cluster mode that needs no garam. The chart refuses one of the two values without the other, and `proxy.url` with garam off.
- **`connector.enabled=false`** installs the manager alone. Its Bots get no workspace.
- **`connector.reportOnly=true`** applies and deletes nothing garam sends, and still reports.
- **`connector.naru.enabled=true`** runs the naru Machine provider in the connector, as a naru fleet's Machine API caller. Create its registration token's Secret (`connector.naru.registrationToken`) and the ConfigMap holding orch's root first, and set `connector.naru.url`, `root.configMap`, `proxyAddress`, `rootDiskBytes` and `egress`. The chart refuses a render without them.
- **`workspaces.namespace=<name>`** serves the bots' workspaces from the connector (ADR 0099). The chart renders that namespace, labelled for this release and enforcing PodSecurity `restricted`, and gives the connector the Pod provider's rights there alone. It refuses the release namespace or a watched one, and Helm refuses a second release naming the same one. Set `images.workspaceHelper`, one `images.agents.<harness>` per harness, `workspaces.pod.modelGatewayURL`, `toolProxyURL` and `egress`, the last with a rule to port 53; where `connector.naru` is on, the same three under `workspaces.naru`. The chart refuses a render without them.
  - **The Pod provider needs a network plugin that enforces NetworkPolicy egress.** Where yours does not, set `workspaces.pod.enforcesNetworkPolicy=false`, and every Pod workspace is refused.
  - **`helm uninstall` deletes the workspace namespace**, and every workspace in it.
- **Do not install with `--take-ownership`.** It adopts another release's claim of a watched namespace (`connector.md` §Declarations and ownership).

## Checks

`make chart-check` in `operator/` renders the chart with the pinned helm, checks what it renders, and installs it into an envtest API server (`operator/test/chart`). `make chart-crds` copies the CRDs from the pinned api module into `crds/`, and the check fails where the two differ.
