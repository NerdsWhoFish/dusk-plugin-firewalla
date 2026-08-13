# Architecture Decision Records

Decisions are recorded as [MADR](https://adr.github.io/madr/) records: context, considered options, decision, and consequences both good and bad.

**The value is the rejected alternatives and the reasoning**, not the decision itself.
That is what stops the same argument happening again in six months.

A decision that is replaced gets a new record superseding the old one, which stays.
A decision that no longer applies is retired, never deleted.

## Index

| # | Decision | In one line |
| --- | --- | --- |
| [0001](0001-reading-the-box-over-a-tunnel-to-its-redis.md) | Read the box over an SSH tunnel to its Redis | Its database is loopback only, so the choice is how to get in: a tunnel plus a codec that can build two read commands beats a shell the box would interpret |
| [0002](0002-a-device-is-its-mac-address.md) | A device is its MAC address | The address is a lease and the name is an opinion; only the MAC survives the events that must not churn the catalog |
| [0003](0003-staleness-is-a-status-not-a-deletion.md) | Staleness is a status, forgetting is a separate knob that is off | A last seen cutoff is a deletion mechanism, so the tidy view and the delete are different fields and only one of them is dangerous |
| [0004](0004-this-plugin-declares-no-actions.md) | This plugin declares no actions | Every verb worth having is a write to the device every packet passes through, over an interface this plugin deliberately does not have |
| [0005](0005-the-host-key-must-be-pinned.md) | The host key must be pinned | The credential goes to whatever answers, so pinning is required and the mismatch error quotes the key you need to paste |
| [0006](0006-devices-attach-to-networks-networks-run-on-the-box.md) | Devices attach to networks, networks run on the box | Only edges that were read: no device to box edge because two hops say it, and no guessed wired or wireless because the box does not know |
