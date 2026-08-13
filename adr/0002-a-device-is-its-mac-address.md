# 2. A device is its MAC address

Date: 2026-08-13

## Status

Accepted

## Context and Problem Statement

A ref is the one field in the catalog that is effectively permanent ([entity schema](https://github.com/NerdsWhoFish/dusk-plugin-sdk/blob/main/proto/dusk/v1alpha1/entity.proto)).
It is what correlates a thing across sources and across time, so choosing what a device's ref is built from decides whether the catalog is stable or whether it churns.

Three candidates present themselves, and two of them are traps.

An IP address is what everybody actually recognises a device by, and it is the least stable thing about it.
A DHCP lease expiring while a laptop is on holiday moves it.
Keying on the address means every renewal deletes a device and creates a new one, which is a catalog that rewrites itself weekly and a history that is worthless.

A name is worse in a different way.
The box carries two: the one it worked out from DHCP and mDNS, and the one somebody typed into the app.
Both change, the typed one on a whim, and renaming a thing must not create a thing.

## Considered Options

1. **The IPv4 address.**
2. **The device's name.**
3. **The MAC address.**
4. **The box's own internal identifier.**

## Decision Outcome

**Option 3.** The ref is `device:<namespace>/<mac>`, lower cased with the colons written as hyphens, for example `device:home/a8-bb-cc-00-00-01`.

The MAC is the only field here that belongs to the hardware rather than to the network's opinion of it.
It survives a lease, a rename, a reboot and a move to a different subnet, which is exactly the set of events that must not churn a catalog.
It is also how the box itself keys the record: the inventory lives under `host:mac:*`, so this is not a convention imposed from outside.

The address and both names still travel, as the `ipv4` attribute and the entity's title.
That is the split that matters.
The title is what a human reads and it is allowed to change; the ref is what the graph is built on and it is not.

Option 4 was rejected because the box does not expose a stable device identifier separate from the MAC.
The MAC is its identifier.

## Consequences

Good:

- A lease renewal, a rename and a subnet move all leave the ref alone.
- Correlating with anything else that knows MACs, a DHCP server or a switch's address table, is a string comparison.
- It matches the upstream's own key, so there is no mapping to keep in step.

Bad:

- **Randomised MACs.** Phones invent an address per network by default, so one phone that forgets and rejoins a network is genuinely a new device to this plugin, and there is no way from here to tell that it is the same handset.
  This is the single largest source of the idle strangers that accumulate in the catalog.
  The plugin flags what it can: an address with the locally administered bit set gets `randomised_mac`, which at least explains the pile rather than leaving it a mystery.
- **A replaced network card is a new device**, and the old one lingers as idle.
  That is correct in the strictest sense and unhelpful in practice.
- **Refs are unreadable.** Nobody searches for `a8-bb-cc-00-00-01`.
  Search has to go through the title, which is what the title is for, but it does mean the ref is machine furniture rather than something to type.
- Every guest phone that has ever associated is permanently a ref.
  The catalog accumulates strangers, which [ADR-0003](0003-staleness-is-a-status-not-a-deletion.md) has to deal with rather than solving here.
