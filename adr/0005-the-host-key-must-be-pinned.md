# 5. The host key must be pinned

Date: 2026-08-13

## Status

Accepted

## Context and Problem Statement

The plugin authenticates to the box with a password or a private key ([ADR-0001](0001-reading-the-box-over-a-tunnel-to-its-redis.md)).
Whichever it is, it gets sent to whatever answers on that address.

`golang.org/x/crypto/ssh` makes this an explicit choice, because `HostKeyCallback` has no default.
The path of least resistance is `ssh.InsecureIgnoreHostKey()`, which is one line and means the plugin will hand a router password to anything that wins a race for that IP.
On a home network the realistic version of that is not an attacker, it is a rebuilt box or a DHCP address that moved, but the credential goes out either way.

The reason this is a decision rather than an obvious yes is that pinning has a real usability cost, and the cost lands at exactly the wrong moment.
An operator configuring the plugin for the first time does not know the box's host key.
Making them find it before anything works is how a required field becomes a reason not to bother.

## Considered Options

1. **Accept any host key.** One line, no field, no friction.
2. **Trust on first use.** Remember the first key seen, refuse changes after that.
3. **Require a pinned key, and make the plugin tell you what it is when you get it wrong.**

## Decision Outcome

**Option 3.** `host_key` is a required config field, holding the key in the one line form `ssh-keyscan` prints.
Either form parses, the bare key or the host prefixed line, because an operator pastes whatever their terminal printed.

The friction is answered rather than accepted.
When the presented key does not match the pin, the refusal quotes the presented key in `ssh-keyscan` form, so the error message contains the exact string the operator needs to paste.
A first run against an unknown key is therefore one failed validation and one copy, not a research task.
That also makes a legitimately rebuilt box recoverable: the error says what the new key is, which is the thing the operator would otherwise have to go and find.

**The pin also selects the algorithm**, which is not obvious and is the part that bites.
A box holds several host keys, an ed25519, an RSA and an ECDSA one, and left to negotiate it offers whichever the two ends prefer.
Pinning the ed25519 key and letting the handshake choose freely gets an ECDSA key back and a mismatch on a box that is entirely correct.
So the client asks for the type that was pinned, and only that type.
This was found against a real box and not by any test, which is why there is now a test named after it.

Option 2 was rejected on where the state would have to live.
Trust on first use needs somewhere to remember the key, and a plugin subprocess has nowhere.
Writing it back into its own configuration means a plugin that mutates its config, which is a much larger idea than this needs, and the first use it trusts is still unverified.

## Consequences

Good:

- The credential only ever goes to a box that proves it is the same box as last time.
- A rebuilt or replaced box fails loudly and tells you what changed, rather than being silently trusted.
- No state to keep, so two Dusk installs configured from the same values behave identically.

Bad:

- **An extra required field before anything works**, and the value is opaque to anybody who has not met `ssh-keyscan`.
  The help text names the command, which is the most a form field can do.
- **A firmware update that rotates the host key breaks ingest** until somebody updates the field, and the symptom is a failing ingester rather than anything that mentions host keys until you read the error.
- The error message quotes a key the plugin has not verified, which is fine because a host public key is public, but it does mean the fix path is "paste what the unverified thing told you", and that only works because the operator is expected to sanity check it.
- **The pinned key's type is now also a negotiation constraint**, so pinning an old or weak key type quietly narrows what the handshake will agree to.
  That is the right default here and it is still a coupling between a security setting and a compatibility one.
