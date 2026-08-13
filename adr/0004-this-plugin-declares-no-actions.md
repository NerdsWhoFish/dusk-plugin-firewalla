# 4. This plugin declares no actions

Date: 2026-08-13

## Status

Accepted

## Context and Problem Statement

Most Dusk plugins observe and act.
The action half is usually the interesting one: a catalog you can only read is a wiki with better indexing.

A Firewalla is full of things that look like good actions.
It can block a device, pause its internet, put it in quarantine, change a rule, reboot.
Every one of those is a verb an operator genuinely wants at three in the morning, and every one of them is a write to the single device every packet in the building passes through.

There is also a practical problem underneath the risk.
The read path this plugin has is a tunnel to a Redis ([ADR-0001](0001-reading-the-box-over-a-tunnel-to-its-redis.md)).
None of those verbs are Redis writes.
They are calls into the box's own daemons, reached either through the app's undocumented local protocol or through the MSP API, and this plugin has neither.
Implementing any of them means either building the write path this plugin deliberately does not have, or poking values into the box's live state behind its own software's back.

## Considered Options

1. **Block a device.** `ACTION_CLASS_MUTATING`, recoverable by unblocking.
2. **Pause internet for a device**, same class, time limited.
3. **Reboot the box.** `ACTION_CLASS_DESTRUCTIVE`, since it takes the network down.
4. **A read only action**, something like "show everything the box knows about this device".
5. **No actions at all.**

## Decision Outcome

**Option 5.** `Describe` returns no actions.
`DryRun` answers `supported: false` with the reason, and `Invoke` refuses with the same one, so a caller that tries anyway is told this is a decision rather than an oversight.

Options 1, 2 and 3 all require writing to the router, through an interface this plugin does not have and should not grow in order to have it.
Option 3 in particular is a destructive action whose blast radius is everybody in the building losing the internet, offered by a catalog plugin, and the fact that Dusk would ask for confirmation first is not enough of an argument for it existing.

Option 4 is the tempting one, because it is genuinely safe.
It is rejected because it is redundant: everything it would return is already on the entity, and `get` already returns it.
An action that duplicates a read adds a name to learn and nothing else.

The honest framing is that this plugin's value is entirely in the observation.
Nothing else in the catalog knows the network exists, and a device inventory that other plugins' entities can be related to is worth more than a block button.

## Consequences

Good:

- Nothing this plugin can be asked to do can take the network down.
  There is no approval prompt to get wrong and no proof token flow to reason about, because there is nothing to approve.
- It matches the standing operational rule for this box, which is read only, always.
- The plugin needs only a credential that can read.
  A future deployment could hand it an account restricted to exactly that.

Bad:

- **An operator who wants to act has to leave the catalog** and open the app.
  Dusk shows them the device and then cannot do anything to it, which is a worse experience than either extreme.
- **Adding an action later is not a small change.** It needs a write path, which means either the MSP API or the local protocol, and probably a second credential.
  The cost of that is deferred, not avoided.
- A plugin with no actions exercises less of the contract, so this repository is a weaker canary than one that declares them.
  If `Invoke` or the proof token flow breaks in the SDK, nothing here notices.
