# 1. Reading the box over an SSH tunnel to its Redis

Date: 2026-08-13

## Status

Accepted

## Context and Problem Statement

A Firewalla box is the only thing on a home network that knows the whole device fleet.
It learns devices from DHCP and from traffic, so it sees everything that has ever been on the wire, not just what answers a scan today.
Nothing else has that list.

Getting it out is the problem.
The box keeps its inventory in a Redis bound to loopback, so the database is not reachable from the network at all.
Its own local API on port 8833 answers only requests signed the way the mobile app signs them, which means reproducing an undocumented, unstable protocol.
The one documented interface, the MSP API, needs an MSP subscription and a personal access token.

So the shape of the problem is: a plugin that runs somewhere else has to read a database that only exists on the far side of an SSH login.

## Considered Options

1. **Shell out to `ssh` and `redis-cli`.** Build a command string, run `exec.Command("ssh", host, script)`, parse the text.
2. **In-process SSH, running `redis-cli` in an exec session.** Same remote command, no dependency on an `ssh` binary.
3. **In-process SSH, tunnelling to Redis and speaking RESP directly.** A direct-tcpip channel to `127.0.0.1:6379` and a small protocol codec of our own.
4. **The Firewalla MSP API.** `/v2/devices` and friends, over HTTPS, with a token.
5. **The local API on port 8833.** Reverse engineer the app's request signing.

## Decision Outcome

**Option 3.** The plugin opens an SSH connection with `golang.org/x/crypto/ssh`, asks for a channel to the box's own `127.0.0.1:6379`, and speaks RESP over it with a codec that can build exactly two commands: `SCAN` and `HGETALL`.

The deciding argument is not elegance, it is blast radius.
This box routes an entire house.
With options 1 and 2, every read is a string that a remote shell interprets, and the distance between "read a hash" and "run something else entirely" is one bad interpolation.
With option 3 the read path physically cannot express a write, because no code exists that constructs one.
The standing rule for this device is read only, always, and this is the option that makes the rule structural rather than a promise.

Two smaller arguments point the same way.
Option 1 needs an `ssh` binary in whatever image Dusk runs in, and inherits that user's `~/.ssh/config` and `known_hosts`, which is a lot of behaviour a plugin cannot reason about.
Options 1 and 2 both return text that has to be parsed back into fields, where option 3 gets typed replies for free.

`SCAN` rather than `KEYS` is part of the same decision.
`KEYS` blocks Redis for the length of the keyspace, and this Redis belongs to the thing forwarding everybody's packets.

Option 4 is the better transport and is rejected only for now.
It is documented, it is stable, it is HTTPS, and it needs no shell on the router at all.
It also needs an MSP subscription, which not every owner has and this one does not.
If MSP access appears, the right move is to add it as a second, preferred source in this plugin rather than a second plugin, and this ADR gets superseded.

Option 5 is rejected outright.
An undocumented request signature that the vendor can change in any firmware release is not a foundation.

## Consequences

Good:

- The read path cannot write.
  Not "does not", cannot.
- No dependency on any binary being present, on either end.
  The box needs no `redis-cli` and the host needs no `ssh`.
- Typed replies, so a field that is missing is missing rather than being an empty column in a text table.
- The codec is about 200 lines and has no dependencies, so it is testable against a stub in the same package.

Bad:

- **It depends on `AllowTcpForwarding` in the box's sshd.** That is the default, but it is a setting an owner can turn off, and if it is off the plugin does not degrade, it fails.
  The error names the setting, which is the most that can be done from here.
- **It gives up everything that is not in Redis.** The box's interface link speeds live in `/sys/class/net`, and reading them needs an exec session.
  That is a real loss: whether the 10G ports are populated is exactly the sort of thing a catalog should know.
  It is not worth reintroducing a shell to get.
- We maintain a hand written RESP codec.
  A Redis library would be better tested, and would also be able to write.
- Authentication is still a shell account on the router.
  A tunnel is not a smaller credential than an exec session, it is the same credential used for less.
