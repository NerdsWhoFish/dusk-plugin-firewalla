# dusk-plugin-firewalla

Puts a [Firewalla](https://firewalla.com) box's own view of the network into [Dusk](https://github.com/NerdsWhoFish/dusk): the networks it serves, every device it has ever seen, and the box itself.

It is the network layer under everything else in a catalog.
A Kubernetes plugin knows there is a cluster and a host plugin knows there is a machine, and neither of them knows the machine is on a LAN with two hundred other things, half of which are somebody's phone.
The box does, because it learns devices from DHCP and from traffic rather than from a scan, so it sees things that are switched off right now and things that never answer a ping.

It observes only.
It declares no actions at all, on purpose ([ADR-0004](adr/0004-this-plugin-declares-no-actions.md)).

## What it emits

- **`router`**, one per configuration: the box, carrying how many networks and devices it knows.
- **`network`** per network the box serves, identified by its UUID, carrying interface, subnet, gateway, type and how many devices are on it.
- **`device`** per MAC address the box has seen, ever, carrying its address, vendor, network, activity and status.
- **`attached_to`** from each device to its network, and **`runs_on`** from each network to the box.

There is deliberately no device to box edge, and no access points, switches or wired versus wireless.
Two hops already say the first one, and the box's database contains none of the rest ([ADR-0006](adr/0006-devices-attach-to-networks-networks-run-on-the-box.md)).

## A device is its MAC

The ref is `device:<namespace>/<mac>`, hyphenated, for example `device:home/a8-bb-cc-00-00-01`.

The address it holds is a lease and the name is whatever somebody typed, so neither can be the identity: keying on either means every DHCP renewal deletes a device and creates a new one ([ADR-0002](adr/0002-a-device-is-its-mac-address.md)).
Both still travel, as the `ipv4` attribute and the entity's title, where they are free to change.

The bill for this is randomised MACs.
A phone invents an address per network, so one handset that forgets and rejoins is genuinely a new device here and there is no way to tell from the data that it is not.
Devices whose address has the locally administered bit set carry `randomised_mac`, which at least explains the strangers.

## Half the fleet is not there any more

A box remembers everything.
On a real household network that is around two hundred and fifty devices of which about half have not been seen for months.

Emitting all of them plain would make the catalog answer a question nobody asked, and dropping the old ones is worse: Dusk deletes what a **successful** ingest stops mentioning, so a last seen cutoff is a deletion mechanism, not a display setting.
So there are two fields and only one of them is dangerous ([ADR-0003](adr/0003-staleness-is-a-status-not-a-deletion.md)):

- `active_within` sets a `status` attribute, `active`, `idle` or `unknown`. It removes nothing.
- `forget_after` is the only thing here that stops emitting a device. It is empty by default, it is ignored entirely when the read was partial, and it is rejected if it is shorter than `active_within`.

`unknown` is its own answer: a box that never recorded a time has not told you the device is gone.

## How it reads the box

Over SSH, tunnelled to the Redis the box keeps on its own loopback, speaking RESP with a codec that can build exactly two commands, `SCAN` and `HGETALL` ([ADR-0001](adr/0001-reading-the-box-over-a-tunnel-to-its-redis.md)).

That shape is the point.
This device routes an entire building, so the read path is one that physically cannot express a write, rather than one that is careful not to.
`SCAN` rather than `KEYS` for the same reason: `KEYS` blocks Redis for the length of the keyspace.

Two things follow from it that are worth knowing before you deploy it:

- **The box's sshd must permit `AllowTcpForwarding`.** It is the default. If it is off, this plugin does not degrade, it fails, and the error says so.
- **Anything not in Redis is not here.** Interface link speeds live in `/sys/class/net` and reading them needs a shell, which is exactly what this design gives up.

If you have a Firewalla MSP subscription, its API is the better transport and this plugin should grow it as a preferred source rather than being replaced.
That is written down in ADR-0001 rather than left to be rediscovered.

## Configuration

| Field | Required | Meaning |
| --- | --- | --- |
| `host` | yes | Where the box answers SSH |
| `port` | no | Defaults to 22 |
| `user` | no | The box's shell account. Defaults to `pi` |
| `host_key` | yes | The box's SSH host key, as `ssh-keyscan` prints it. Pinned, because the credential goes to whatever answers ([ADR-0005](adr/0005-the-host-key-must-be-pinned.md)) |
| `password` | one of | The SSH password. Sensitive, so it is entered in Dusk's own interface, sealed, and never read back |
| `private_key` | one of | A PEM private key, used in preference to a password. Sensitive |
| `key_passphrase` | no | Only when the private key is encrypted. Sensitive |
| `namespace` | no | What refs are namespaced by. Defaults to `firewalla` |
| `active_within` | no | How recently a device must have been seen to be active. Defaults to `336h` |
| `forget_after` | no | Stop emitting devices unseen for longer than this, which lets Dusk delete them. Empty by default |

Getting `host_key` wrong is meant to be recoverable: the refusal quotes the key the box presented, in the form you paste back into the field.
Whichever type you pin is also the type the handshake asks for, because a box holds three of them and would otherwise hand back one you did not pin.

Configurations naming one box share a budget and are spaced apart, because the Redis being read belongs to something that is also forwarding everybody's packets.

## A batch is whole, or partial, or an error

One `Ingest` call sends exactly one batch and closes.

A read that cannot happen at all is an error, never an empty batch, because an empty batch is a claim that the network is empty.
A read where the keyspace enumerated but individual devices did not sets `partial`, which is how Dusk is told not to treat the gap as deletion ([ADR-0011](https://github.com/NerdsWhoFish/dusk/blob/main/adr/0011-ingester-scheduling.md)).
`forget_after` is ignored on any partial batch.

## The views

Three, all **declared** rather than drawn: a device's details, a network's, and the box's.
A declared view is a description Dusk renders with its own React ([ADR-0020](https://github.com/NerdsWhoFish/dusk/blob/main/adr/0020-plugin-ui.md)'s first tier), so this plugin ships no JavaScript and Dusk makes no decision about trusting any.

Everything worth showing about a device is a field.
A custom element here would be a table with extra steps.

## Nothing here is anybody's network

Every address, MAC, UUID and name in the tests, the docs and the help text is fabricated or `example.com`.
The plugin reads a real box at runtime and this repository knows nothing about any of them.

## Building

```sh
make build   # ./bin/dusk-plugin-firewalla
make check   # vet + no cgo + race tests, what CI runs
make live FIREWALLA_HOST=... FIREWALLA_HOST_KEY=... FIREWALLA_PASSWORD=...
```

`make live` reads a real box and prints what it found by kind and by status.
It exists because nothing offline can prove that a given firmware's Redis holds these fields or that its sshd permits forwarding, and it is skipped without a target so `make check` never touches anybody's router.
It only reads.

## License

Apache 2.0, matching Dusk.
