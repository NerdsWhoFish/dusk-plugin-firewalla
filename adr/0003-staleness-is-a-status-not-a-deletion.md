# 3. Staleness is a status, and forgetting is a separate knob that is off

Date: 2026-08-13

## Status

Accepted

## Context and Problem Statement

A Firewalla remembers everything it has ever seen.
On a real household network that is roughly two hundred and fifty devices, of which around half have not been seen for months: guests' phones, a replaced printer, every randomised MAC a visitor's handset has ever invented ([ADR-0002](0002-a-device-is-its-mac-address.md)).

Emitting all of them as plain entities makes the catalog wrong in a specific way.
It is not that the data is false, every one of those devices really was on the network.
It is that a catalog which cannot distinguish "this is on your network" from "this was on your network in March" answers the question nobody asked.

The obvious fix is a last seen cutoff: stop emitting anything older than some window.
That fix is a trap, and it is worth being precise about why.

Dusk deletes an entity when an ingester **succeeds** and no longer reports it ([ADR-0011](https://github.com/NerdsWhoFish/dusk/blob/main/adr/0011-ingester-scheduling.md)).
A cutoff turns "the plugin chose not to mention it" into exactly that signal.
So a cutoff is not a display policy, it is a deletion mechanism, and it fires on a device that merely went quiet.
Turn the window down to have a tidier catalog and you have silently deleted a third of your network's history.

## Considered Options

1. **Emit everything, say nothing about staleness.** Correct and useless.
2. **One cutoff.** Emit only devices seen within the window; drop the rest.
3. **One cutoff, with an opt out.** As above, plus a flag to disable it.
4. **Two knobs: a status window and a separate forget horizon, with forgetting off by default.**

## Decision Outcome

**Option 4.**

- `active_within` (default 336h, a fortnight) decides the `status` attribute: `active`, `idle`, or `unknown` when the box never recorded a time.
  It never removes anything.
- `forget_after` (default empty, meaning never) is the only thing in this plugin that stops emitting a device.

The point of two knobs rather than one is that they are different decisions and only one of them is dangerous.
Wanting a tidier "what is on my network right now" view is a display preference and should not be spelled the same way as "delete two years of history".
With one knob, an operator adjusting the first inevitably does the second.

Three properties make the dangerous knob safe to have at all:

- It is **off by default**, so the plugin never removes anything unless somebody typed a number.
- It is **ignored entirely when the batch is partial**, because half an enumeration plus a cutoff is a deletion mechanism wearing a policy's hat.
- It **cannot be shorter than `active_within`**, checked at config time.
  Otherwise a device is idle on one run and gone on the next, which is a loop rather than a policy.

A device with no recorded activity at all is never forgotten.
Never seen and seen long ago are different facts, and guessing turns a box that failed to record a timestamp into a deletion.

## Consequences

Good:

- Nothing is deleted by accident.
  The only deletion path is explicit, named for what it does, and disabled.
- The catalog can answer both questions: everything ever seen, and what is live, by filtering on one attribute.
- `unknown` is a real answer rather than being folded into `idle`, so a gap in the box's own record does not read as a claim about the device.

Bad:

- **The default catalog is large and mostly idle.** Two hundred and fifty devices where a hundred and thirty matter.
  Every consumer has to know to filter on `status`, and one that forgets shows a misleading list.
- Two configuration fields need explaining, and the relationship between them is a rule rather than something the form can show.
- `active_within` changes the meaning of stored data without changing the data.
  Shortening it makes devices idle retroactively, which is fine but is the sort of thing that looks like a bug in a graph.
- The plugin holds the decision, not Dusk.
  Another ingester with the same problem will make its own choice, and the two will differ until something forces them to agree.
