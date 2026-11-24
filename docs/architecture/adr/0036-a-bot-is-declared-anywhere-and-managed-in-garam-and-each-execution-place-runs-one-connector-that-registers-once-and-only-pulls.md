# ADR 0036: A bot is declared anywhere and managed in garam, and each execution place runs one connector that registers once and only pulls

> Status: accepted
> Date: 2026-11-24

## Context

- **The structure sauce approved on 2026-11-24** (`garamsh/discussions` discussion 6, comment 19856107):
  - a bot runs in a Kubernetes cluster, on a person's PC or on a naru host;
  - it is declared in garam's dashboard or at its place, by file or CRD, and either way garam shows and manages it;
  - each place runs one connector, registered once like a GitHub app or runner, with a one-time token and a key made at the place that never leaves it;
  - garam never dials into a place;
  - goal ④'s "cluster" widens to an execution place.
- **What exists for a cluster:**
  - garam's connected-cluster record and its registration, exchange and rotation (garam ADR-0184, ADR-0185; garam#2045, garam#2046);
  - declarations placed on a cluster and long-polled (garam ADR-0187, garam#2060);
  - the connector (garam-bot-operator ADR 0087 to ADR 0093) and its wire, `cluster-connection/v1alpha1` (garam-bot-operator ADR 0092).
- **The earlier rule for a cluster** was that a cluster connector dials garam over HTTPS long poll and opens no inbound port (garam-bot-operator ADR 0087, ADR 0091).
- **The repository layout** (comment 19857166, garam-bot#1205) puts the cluster connector, the PC daemon and the PC app in this repository.

## Decision

1. **A bot is declared in one of three places, with interface 1's schema each time:**
   - in garam, where garam owns it;
   - as a CRD in a cluster, which the cluster owns;
   - as a file at a PC or a naru-host place, which that place owns.

   garam shows every bot of every place, and changes only those it owns. Interface 1's ownership marker value `cluster` names the place's own objects at every kind of place, by a wording release of interface 1's OWN-1, which today calls such an object "the cluster's".
2. **An execution place is a cluster, a PC or a naru-host place, and runs one connector:**
   - in a cluster, the connector of `operator/`, installed by its Helm chart;
   - on a PC, or beside a naru fleet as a use of it, `garam-bot daemon`, one process that runs several bots.
3. **A connector registers once,** with a one-time registration token and a key it makes at the place, which never leaves it. It rotates the key itself. Revoking or deleting the place in garam ends its access at its next request.
4. **Every connection is opened by the place.** Declarations, messages to bots and reports travel by long poll or stream over HTTPS that the connector or the bot's process opens. garam opens no connection to a place, and a place opens no inbound port.
5. **One protocol for every place.** Interface 4 is the cluster-connection protocol, widened from a cluster to a place. A PC or a naru-host place holds one namespace and no claim. garam's record of a place is one record kind for every kind of place.
6. **Only a bot garam declared and placed gets a garam token.** garam ADR-0184's decision "A cluster's token reaches the cluster-connection routes, and the mint of the tokens of a bot placed on that cluster by a live declaration, and nothing else", and the protocol's AUTH-5, hold at every place. A bot declared at its place runs with no garam token, and whether it gets a garam identity is open with garam.

## Consequences

- The cluster connector, the PC daemon and the naru-host connector share registration, rotation, the declaration feed, reports and the bot-token exchange. The daemon is one binary for a PC and a naru-host place, with different workspace providers.
- Interface 1 takes a wording release for OWN-1 before a place other than a cluster reads a file.
- garam's connected-cluster record and its GRN widen to a place, which is garam's to design. The interface's next version carries the place's kind.
- A place that loses garam keeps running what it runs. A bot declared at the place needs no garam at all (ADR 0031).
- A bot behind a NAT or a firewall needs only outbound HTTPS.

## Signs the decision was wrong

- A place needs garam to reach it, for latency or for an action the long poll cannot carry, often enough to want an inbound path.
- The PC and the cluster need protocols different enough that the shared version carries a branch per kind on most requests.
- People declare bots at a place and never want them in garam, so the place-owned objects garam shows go unread.
