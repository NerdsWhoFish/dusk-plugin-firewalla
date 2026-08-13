# 6. Devices attach to networks, networks run on the box

Date: 2026-08-13

## Status

Accepted

## Context and Problem Statement

The plugin emits three kinds: the box, its networks, and every device it has seen.
What edges go between them decides whether the catalog is a graph or three lists that happen to share a page.

Two facts are true and only one of them is observed.
A device is attached to a network: the box records the network's identifier on the device's own record, so this is read, not inferred.
A device reaches the internet through the box: also true, and nowhere in the data, because it follows from the network's gateway being the box rather than from anything written down per device.

There is a third temptation, which is topology.
A home network has access points, switches, wired devices and wireless ones, and a catalog that showed which access point a phone is on would be genuinely useful.
None of it is in the box's database.
The device records carry no signal for wired versus wireless, no SSID, no radio, no link rate.
That information exists in the access point layer of the vendor's app and does not reach the database this plugin reads.

## Considered Options

1. **Device to network, and device to box.** Say both facts directly.
2. **Device to network, and network to box.** Say the observed fact, let the second follow from walking.
3. **Device to box only.** Flatten the networks out.
4. **Any of the above, plus inferred topology**: guess wired versus wireless from vendor or device type, attach wireless devices to the nearest access point.

## Decision Outcome

**Option 2.** `device -[attached_to]-> network` and `network -[runs_on]-> router`.
There is deliberately no device to box edge.

The two hops already say it, and saying it again costs two hundred and fifty edges that carry no information.
Anything asking "what is behind this router" walks two steps instead of one, which is what a graph is for.
Every one of those edges would also be identical, which is a good sign an edge is describing the schema rather than the data.

Option 3 loses the only real structure in the answer.
Which network a device is on is the one piece of segmentation the box actually records, and flattening it throws away the difference between a WAN uplink and the LAN.

Option 4 is rejected on principle, and it is the important rejection.
The catalog's value is that its claims are observed.
Inventing wired versus wireless from a vendor string produces something that looks like knowledge, is right most of the time, and is indistinguishable from the real thing once it is in the graph.
A confident wrong answer in a catalog is worse than a missing one, because a missing one prompts somebody to go and look.

## Consequences

Good:

- Every edge in the batch corresponds to a field that was read.
- The edge count is devices plus networks rather than devices times two, which matters at this fleet size.
- Adding a real source for topology later, a managed switch's address table or an access point API, adds edges without contradicting any of these.

Bad:

- **"What is behind this router" is a two hop query.** Anything that walks one hop from the box sees networks and no devices, which is surprising until you know why.
- **No access points, no switches, no wired versus wireless.** The catalog's picture of the network is flat: everything is equally "on the LAN", including the three access points and the two switches, which appear as ordinary devices because that is all the box says they are.
- A device whose record names a network the plugin did not read gets no edge at all rather than a guess, so a partial read of the networks quietly produces loose devices.
