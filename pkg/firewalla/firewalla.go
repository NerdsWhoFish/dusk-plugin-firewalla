// Package firewalla reads a Firewalla box's own inventory: every device it has
// ever seen, the networks it serves, and the box itself.
//
// It holds no gRPC and no plugin machinery, so what the network looks like in
// the catalog is testable against a stub that speaks Redis.
package firewalla

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	duskv1alpha1 "github.com/NerdsWhoFish/dusk-plugin-sdk/gen/dusk/v1alpha1"
	"github.com/NerdsWhoFish/dusk-plugin-sdk/conformance"
)

// SchemaVersion is the contract a batch from here is built against.
const SchemaVersion = "v1alpha1"

// The two key patterns this plugin reads. Everything else in that Redis is the
// box's own working state and none of the catalog's business.
const (
	devicePattern  = "host:mac:*"
	networkPattern = "network:uuid:*"
)

// DefaultActiveWithin is how recently a device must have been seen to count as
// active. Fourteen days covers a fortnight away without the whole house going
// idle behind you.
const DefaultActiveWithin = 14 * 24 * time.Hour

// DefaultTimeout stops a box that accepts a connection and then says nothing
// from holding an ingest open forever.
const DefaultTimeout = 2 * time.Minute

// Client turns one box's Redis into a catalog batch.
type Client struct {
	// Open dials the box's Redis.
	Open Open

	// Namespace is what refs are namespaced by, so two boxes do not collide.
	Namespace string

	// Address is where the box answers, recorded on the router entity.
	Address string

	// ActiveWithin decides active from idle. Zero means DefaultActiveWithin.
	ActiveWithin time.Duration

	// ForgetAfter drops devices unseen for longer than this. Zero means never,
	// which is the only default that cannot quietly delete a real device.
	ForgetAfter time.Duration

	// Timeout caps one read of the box. Zero means DefaultTimeout.
	Timeout time.Duration

	// Now exists so a test can assert on staleness without waiting a fortnight.
	Now func() time.Time
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) namespace() string {
	if c.Namespace != "" {
		return slug(c.Namespace)
	}
	return "firewalla"
}

func (c *Client) activeWithin() time.Duration {
	if c.ActiveWithin > 0 {
		return c.ActiveWithin
	}
	return DefaultActiveWithin
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

func (c *Client) ref(kind, name string) string {
	return conformance.CanonicalRef(kind, c.namespace(), slug(name))
}

// Ping proves the whole chain works, from the SSH handshake to Redis
// answering, without walking the keyspace the way an ingest does.
func (c *Client) Ping(ctx context.Context) error {
	return c.connected(ctx, func(r *redis) error {
		reply, err := r.call("SCAN", "0", "COUNT", "1")
		if err != nil {
			return err
		}
		if len(reply.array) != 2 {
			return errors.New("firewalla: that is not a Redis, it answered SCAN with something else")
		}
		return nil
	})
}

// Batch reads the whole inventory. A read that fails outright returns an error
// rather than an empty batch, because absence has to mean absence.
func (c *Client) Batch(ctx context.Context) (*duskv1alpha1.IngestBatch, error) {
	var batch *duskv1alpha1.IngestBatch

	err := c.connected(ctx, func(r *redis) error {
		networks, networksPartial, err := c.readNetworks(r)
		if err != nil {
			return err
		}

		devices, devicesPartial, err := c.readDevices(r)
		if err != nil {
			return err
		}

		batch = c.assemble(networks, devices, networksPartial || devicesPartial)
		return nil
	})

	return batch, err
}

// connected opens the box, runs one read against it and always closes up. The
// watcher goroutine is what makes a cancelled context land on a blocked read,
// which an SSH channel cannot be given a deadline for.
func (c *Client) connected(ctx context.Context, read func(*redis) error) error {
	if c.Open == nil {
		return errors.New("firewalla: no way to reach the box was configured")
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	conn, err := c.Open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	done := make(chan struct{})
	defer close(done)
	go watch(ctx, conn, done)

	return read(newRedis(conn))
}

func watch(ctx context.Context, conn net.Conn, done <-chan struct{}) {
	select {
	case <-ctx.Done():
		_ = conn.Close()
	case <-done:
	}
}

func (c *Client) readDevices(r *redis) ([]Device, bool, error) {
	keys, err := r.scan(devicePattern)
	if err != nil {
		return nil, false, err
	}

	hashes, failures := r.hashes(keys)

	devices := make([]Device, 0, len(hashes))
	for key, fields := range hashes {
		mac := strings.TrimPrefix(key, "host:mac:")
		if slug(mac) == "" {
			continue
		}
		devices = append(devices, readDevice(mac, fields))
	}

	return devices, len(failures) > 0, nil
}

func (c *Client) readNetworks(r *redis) ([]Network, bool, error) {
	keys, err := r.scan(networkPattern)
	if err != nil {
		return nil, false, err
	}

	hashes, failures := r.hashes(keys)

	networks := make([]Network, 0, len(hashes))
	for key, fields := range hashes {
		uuid := strings.TrimPrefix(key, "network:uuid:")
		if slug(uuid) == "" {
			continue
		}
		networks = append(networks, readNetwork(uuid, fields))
	}

	return networks, len(failures) > 0, nil
}

// assemble builds the batch. Counting here rather than leaving it to Dusk is
// the normalisation rule: whatever shape a plugin emits is the shape held.
func (c *Client) assemble(networks []Network, devices []Device, partial bool) *duskv1alpha1.IngestBatch {
	batch := &duskv1alpha1.IngestBatch{SchemaVersion: SchemaVersion, Partial: partial}

	known := make(map[string]Network, len(networks))
	for _, network := range networks {
		known[network.UUID] = network
	}

	counts := map[string]counter{}
	overall := counter{}
	kept := make([]Device, 0, len(devices))
	for _, device := range devices {
		if c.forgotten(device, partial) {
			continue
		}
		kept = append(kept, device)

		status := c.status(device)
		overall = overall.add(status)
		if _, ok := known[device.NetworkUUID]; ok {
			counts[device.NetworkUUID] = counts[device.NetworkUUID].add(status)
		}
	}

	routerRef := c.ref("router", "firewalla")
	batch.Entities = append(batch.Entities, c.routerEntity(routerRef, len(networks), overall))

	for _, network := range networks {
		networkRef := c.ref("network", network.UUID)
		batch.Entities = append(batch.Entities, c.networkEntity(networkRef, network, counts[network.UUID]))
		batch.Relations = append(batch.Relations, &duskv1alpha1.Relation{
			From: networkRef, To: routerRef, Type: "runs_on",
		})
	}

	for _, device := range kept {
		deviceRef := c.ref("device", slug(device.MAC))
		network, attached := known[device.NetworkUUID]

		networkRef := ""
		if attached {
			networkRef = c.ref("network", network.UUID)
			batch.Relations = append(batch.Relations, &duskv1alpha1.Relation{
				From: deviceRef, To: networkRef, Type: "attached_to",
			})
		}

		batch.Entities = append(batch.Entities, c.deviceEntity(deviceRef, device, network, networkRef))
	}

	sortBatch(batch)
	return batch
}

// counter is how many devices a network holds, and how many of them are live.
type counter struct {
	known  int
	active int
}

func (c counter) add(status string) counter {
	c.known++
	if status == StatusActive {
		c.active++
	}
	return c
}

// The three things a device's last activity can say. Unknown is its own answer
// rather than idle, because a box that never recorded a time has not told us
// the device is gone.
const (
	StatusActive  = "active"
	StatusIdle    = "idle"
	StatusUnknown = "unknown"
)

func (c *Client) status(device Device) string {
	switch {
	case device.LastSeen.IsZero():
		return StatusUnknown
	case c.now().Sub(device.LastSeen) <= c.activeWithin():
		return StatusActive
	default:
		return StatusIdle
	}
}

// forgotten is the only way a device leaves the catalog, and it is off unless
// an operator asked for it. A partial read forgets nothing at all: half a
// keyspace plus a cutoff is a deletion mechanism wearing a policy's hat.
func (c *Client) forgotten(device Device, partial bool) bool {
	if partial || c.ForgetAfter <= 0 || device.LastSeen.IsZero() {
		return false
	}
	return c.now().Sub(device.LastSeen) > c.ForgetAfter
}

func (c *Client) routerEntity(ref string, networks int, devices counter) *duskv1alpha1.Entity {
	fields := map[string]any{
		"networks":       float64(networks),
		"devices_known":  float64(devices.known),
		"devices_active": float64(devices.active),
	}
	if c.Address != "" {
		fields["address"] = c.Address
	}
	attributes, _ := structpb.NewStruct(fields)

	return &duskv1alpha1.Entity{
		Ref: ref, Kind: "router", Namespace: c.namespace(), Name: "firewalla",
		Title: "Firewalla",
		Description: fmt.Sprintf("Router and firewall for %s, and the only place %s know about. "+
			"Observed by Dusk from Firewalla.", count(networks, "network"), count(devices.known, "device")),
		Attributes: attributes,
	}
}

func (c *Client) networkEntity(ref string, network Network, devices counter) *duskv1alpha1.Entity {
	fields := map[string]any{
		"uuid":           network.UUID,
		"devices_known":  float64(devices.known),
		"devices_active": float64(devices.active),
	}
	for name, value := range map[string]string{
		"interface": network.Interface,
		"subnet":    network.Subnet,
		"gateway":   network.Gateway,
		"type":      network.Type,
	} {
		if value != "" {
			fields[name] = value
		}
	}
	if len(network.DNS) > 0 {
		fields["dns"] = strings.Join(network.DNS, ", ")
	}
	attributes, _ := structpb.NewStruct(fields)

	return &duskv1alpha1.Entity{
		Ref: ref, Kind: "network", Namespace: c.namespace(), Name: slug(network.UUID),
		Title:       network.Title(),
		Description: describeNetwork(network, devices),
		Attributes:  attributes,
	}
}

func (c *Client) deviceEntity(ref string, device Device, network Network, networkRef string) *duskv1alpha1.Entity {
	status := c.status(device)

	fields := map[string]any{"mac": device.MAC, "status": status}
	for name, value := range map[string]string{
		"ipv4":        device.IPv4,
		"vendor":      device.Vendor,
		"device_type": device.Type,
		"network_ref": networkRef,
	} {
		if value != "" {
			fields[name] = value
		}
	}
	if !device.LastSeen.IsZero() {
		fields["last_seen"] = device.LastSeen.UTC().Format(time.RFC3339)
	}
	if !device.FirstSeen.IsZero() {
		fields["first_seen"] = device.FirstSeen.UTC().Format(time.RFC3339)
	}

	// A randomised MAC is a new device to this plugin every time the phone
	// rotates it, which is the honest explanation for a pile of idle strangers.
	if device.RandomisedMAC() {
		fields["randomised_mac"] = true
	}
	attributes, _ := structpb.NewStruct(fields)

	return &duskv1alpha1.Entity{
		Ref: ref, Kind: "device", Namespace: c.namespace(), Name: slug(device.MAC),
		Title:       device.Title(),
		Description: describeDevice(device, network, status),
		Attributes:  attributes,
	}
}

func describeNetwork(network Network, devices counter) string {
	sentence := "Network served by the Firewalla"
	if network.Interface != "" {
		sentence += " on " + network.Interface
	}
	if network.Subnet != "" {
		sentence += ", " + network.Subnet
	}
	return fmt.Sprintf("%s. %s on it, %d of them active. Observed by Dusk from Firewalla.",
		sentence, count(devices.known, "device"), devices.active)
}

func describeDevice(device Device, network Network, status string) string {
	sentence := "Device"
	if device.Vendor != "" {
		sentence = device.Vendor + " device"
	}
	if network.UUID != "" {
		sentence += " on " + network.Title()
	}
	if device.IPv4 != "" {
		sentence += " at " + device.IPv4
	}

	switch {
	case device.LastSeen.IsZero():
		sentence += ". The box has never recorded it active"
	case status == StatusActive:
		sentence += ", last active " + device.LastSeen.UTC().Format("2 January 2006")
	default:
		sentence += ", idle since " + device.LastSeen.UTC().Format("2 January 2006")
	}

	return sentence + ". Observed by Dusk from Firewalla."
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// sortBatch keeps a batch stable, so a diff between runs shows what changed
// rather than what Redis happened to hand back first.
func sortBatch(batch *duskv1alpha1.IngestBatch) {
	sort.Slice(batch.Entities, func(i, j int) bool {
		return batch.Entities[i].GetRef() < batch.Entities[j].GetRef()
	})
	sort.Slice(batch.Relations, func(i, j int) bool {
		left, right := batch.Relations[i], batch.Relations[j]
		if left.GetFrom() != right.GetFrom() {
			return left.GetFrom() < right.GetFrom()
		}
		return left.GetTo() < right.GetTo()
	})
}

func slug(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, name)
	return strings.Trim(cleaned, "-")
}
