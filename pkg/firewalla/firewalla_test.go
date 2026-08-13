package firewalla

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	duskv1alpha1 "github.com/NerdsWhoFish/dusk-plugin-sdk/gen/dusk/v1alpha1"
	"github.com/NerdsWhoFish/dusk-plugin-sdk/conformance"
)

// Fabricated throughout. Nothing here is anybody's network.
const (
	homeUUID   = "11111111-1111-4111-8111-111111111111"
	uplinkUUID = "22222222-2222-4222-8222-222222222222"
)

var reading = time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)

func stamp(at time.Time) string {
	return strconv.FormatFloat(float64(at.UnixNano())/float64(time.Second), 'f', 3, 64)
}

func house() *stub {
	return &stub{hashes: map[string]map[string]string{
		"network:uuid:" + homeUUID: {
			"name": "Home", "intf": "eth0", "type": "lan",
			"ipv4Subnet": "10.0.0.0/24", "gateway": "10.0.0.1", "dns": `["10.0.0.1"]`,
		},
		"network:uuid:" + uplinkUUID: {
			"name": "Uplink", "intf": "eth1", "type": "wan",
		},
		"host:mac:A8:BB:CC:00:00:01": {
			"name": "kitchen-display", "ipv4Addr": "10.0.0.20",
			"macVendor": "Example Devices", "intf_uuid": homeUUID,
			"lastActiveTimestamp": stamp(reading.Add(-time.Hour)),
			"firstFoundTimestamp": stamp(reading.AddDate(-1, 0, 0)),
			"detect":              `{"type":"tablet","brand":"Example"}`,
		},
		"host:mac:A8:BB:CC:00:00:02": {
			"bname": "old-laptop", "ipv4Addr": "10.0.0.21",
			"macVendor": "Example Computers", "intf_uuid": homeUUID,
			"lastActiveTimestamp": stamp(reading.AddDate(-1, 0, 0)),
		},
		"host:mac:A8:BB:CC:00:00:03": {
			"macVendor": "Example Devices",
		},
		"host:mac:B2:BB:CC:00:00:04": {
			"bname": "someones-phone", "intf_uuid": homeUUID,
			"lastActiveTimestamp": stamp(reading.Add(-2 * time.Hour)),
		},
	}}
}

func client(t *testing.T, box *stub, change func(*Client)) *Client {
	t.Helper()

	c := &Client{
		Open:      box.listen(t),
		Namespace: "test",
		Address:   "router.example.com",
		Now:       func() time.Time { return reading },
	}
	if change != nil {
		change(c)
	}
	return c
}

func batch(t *testing.T, c *Client) *duskv1alpha1.IngestBatch {
	t.Helper()

	built, err := c.Batch(t.Context())
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	return built
}

func byRef(built *duskv1alpha1.IngestBatch) map[string]*duskv1alpha1.Entity {
	out := map[string]*duskv1alpha1.Entity{}
	for _, entity := range built.GetEntities() {
		out[entity.GetRef()] = entity
	}
	return out
}

func attribute(entity *duskv1alpha1.Entity, name string) any {
	return entity.GetAttributes().AsMap()[name]
}

// The three kinds are the whole point: a box, the networks it serves, and
// everything that has ever been on them.
func TestBatchEmitsTheBoxItsNetworksAndItsDevices(t *testing.T) {
	built := batch(t, client(t, house(), nil))

	kinds := map[string]int{}
	for _, entity := range built.GetEntities() {
		kinds[entity.GetKind()]++
	}

	want := map[string]int{"router": 1, "network": 2, "device": 4}
	for kind, count := range want {
		if kinds[kind] != count {
			t.Errorf("%s entities = %d, want %d", kind, kinds[kind], count)
		}
	}
	if built.GetPartial() {
		t.Error("a complete read claimed to be partial")
	}
	if built.GetSchemaVersion() != conformance.SchemaVersion {
		t.Errorf("schema version = %q, want %q", built.GetSchemaVersion(), conformance.SchemaVersion)
	}
}

// Every ref has to be canonical or nothing correlates with anything.
func TestEveryEmittedRefIsCanonical(t *testing.T) {
	built := batch(t, client(t, house(), nil))

	for _, entity := range built.GetEntities() {
		kind, namespace, name, err := conformance.ParseRef(entity.GetRef())
		if err != nil {
			t.Errorf("%s: %v", entity.GetRef(), err)
			continue
		}
		if kind != entity.GetKind() || namespace != entity.GetNamespace() || name != entity.GetName() {
			t.Errorf("%s does not match its own fields", entity.GetRef())
		}
	}
}

// A device hangs off a network and a network off the box. There is deliberately
// no device to box edge: two hops already say it.
func TestRelationsGoDeviceToNetworkToBox(t *testing.T) {
	built := batch(t, client(t, house(), nil))

	edges := map[string]string{}
	for _, relation := range built.GetRelations() {
		edges[relation.GetFrom()+" "+relation.GetType()] = relation.GetTo()
	}

	router := "router:test/firewalla"
	home := "network:test/" + homeUUID

	tests := []struct {
		edge string
		want string
	}{
		{edge: "device:test/a8-bb-cc-00-00-01 attached_to", want: home},
		{edge: "device:test/b2-bb-cc-00-00-04 attached_to", want: home},
		{edge: home + " runs_on", want: router},
		{edge: "network:test/" + uplinkUUID + " runs_on", want: router},
	}
	for _, test := range tests {
		if got := edges[test.edge]; got != test.want {
			t.Errorf("%q went to %q, want %q", test.edge, got, test.want)
		}
	}

	// The device with no interface has nothing to attach to, and inventing one
	// would be worse than leaving it loose.
	if got, ok := edges["device:test/a8-bb-cc-00-00-03 attached_to"]; ok {
		t.Errorf("a device with no network was attached to %q", got)
	}
	for _, relation := range built.GetRelations() {
		if relation.GetFrom() == "device:test/a8-bb-cc-00-00-01" && relation.GetTo() == router {
			t.Error("emitted a device to box edge, which the network edges already say")
		}
	}
}

// The MAC is the identity. If a lease or a rename moved a ref, every DHCP
// renewal would churn the catalog.
func TestIdentityIsTheMACNotTheAddressOrTheName(t *testing.T) {
	before := batch(t, client(t, house(), nil))

	moved := house()
	moved.hashes["host:mac:A8:BB:CC:00:00:01"]["ipv4Addr"] = "10.0.0.99"
	moved.hashes["host:mac:A8:BB:CC:00:00:01"]["name"] = "hallway-display"
	after := batch(t, client(t, moved, nil))

	ref := "device:test/a8-bb-cc-00-00-01"
	first, second := byRef(before)[ref], byRef(after)[ref]
	if first == nil || second == nil {
		t.Fatalf("the device changed ref when its address and name changed")
	}
	if second.GetTitle() != "hallway-display" {
		t.Errorf("title = %q, want the new name", second.GetTitle())
	}
	if attribute(second, "ipv4") != "10.0.0.99" {
		t.Errorf("ipv4 = %v, want the new address", attribute(second, "ipv4"))
	}
}

// Staleness is something the catalog says about a device, not a reason to stop
// saying anything about it.
func TestStalenessIsAStatusAttribute(t *testing.T) {
	built := batch(t, client(t, house(), nil))
	entities := byRef(built)

	tests := []struct {
		ref  string
		want string
	}{
		{ref: "device:test/a8-bb-cc-00-00-01", want: StatusActive},
		{ref: "device:test/a8-bb-cc-00-00-02", want: StatusIdle},
		{ref: "device:test/a8-bb-cc-00-00-03", want: StatusUnknown},
	}
	for _, test := range tests {
		entity := entities[test.ref]
		if entity == nil {
			t.Errorf("%s was not emitted at all", test.ref)
			continue
		}
		if got := attribute(entity, "status"); got != test.want {
			t.Errorf("%s status = %v, want %s", test.ref, got, test.want)
		}
	}
}

// ADR-0011: a plugin must never make absence mean deletion by accident. The
// only knob that removes anything is off unless somebody turns it on.
func TestADR0011_ForgettingIsOffByDefault(t *testing.T) {
	built := batch(t, client(t, house(), nil))

	if _, ok := byRef(built)["device:test/a8-bb-cc-00-00-02"]; !ok {
		t.Fatal("a device idle for a year was dropped by a plugin nobody asked to forget anything")
	}
}

func TestForgetAfterDropsOnlyWhatTheOperatorAskedFor(t *testing.T) {
	built := batch(t, client(t, house(), func(c *Client) {
		c.ForgetAfter = 30 * 24 * time.Hour
	}))
	entities := byRef(built)

	if _, ok := entities["device:test/a8-bb-cc-00-00-02"]; ok {
		t.Error("a device idle for a year survived an explicit thirty day cutoff")
	}
	if _, ok := entities["device:test/a8-bb-cc-00-00-01"]; !ok {
		t.Error("an active device was forgotten")
	}

	// Never seen is not the same as seen long ago, and guessing turns a box
	// that forgot to record a time into a deletion.
	if _, ok := entities["device:test/a8-bb-cc-00-00-03"]; !ok {
		t.Error("a device with no recorded activity was forgotten on a timestamp it does not have")
	}
}

// ADR-0011: half a keyspace plus a cutoff is a deletion mechanism. A partial
// read forgets nothing, whatever the operator configured.
func TestADR0011_APartialReadForgetsNothing(t *testing.T) {
	box := house()
	box.broken = map[string]bool{"host:mac:B2:BB:CC:00:00:04": true}

	built := batch(t, client(t, box, func(c *Client) {
		c.ForgetAfter = 30 * 24 * time.Hour
	}))

	if !built.GetPartial() {
		t.Fatal("an unreadable device did not make the batch partial, so Dusk would treat the gap as deletion")
	}
	if _, ok := byRef(built)["device:test/a8-bb-cc-00-00-02"]; !ok {
		t.Error("a partial read still applied the forget cutoff")
	}
}

// One unreadable key is a partial answer, not the end of the read.
func TestOneUnreadableDeviceDoesNotLoseTheRest(t *testing.T) {
	box := house()
	box.broken = map[string]bool{"host:mac:A8:BB:CC:00:00:01": true}

	built := batch(t, client(t, box, nil))
	if _, ok := byRef(built)["device:test/a8-bb-cc-00-00-02"]; !ok {
		t.Error("one bad key took the other devices with it")
	}
}

// The counts are the plugin's job: Dusk never re-derives what a plugin sends.
func TestTheBoxAndItsNetworksCarryTheirCounts(t *testing.T) {
	entities := byRef(batch(t, client(t, house(), nil)))

	router := entities["router:test/firewalla"]
	if router == nil {
		t.Fatal("no box entity")
	}
	if got := attribute(router, "devices_known"); got != float64(4) {
		t.Errorf("devices_known = %v, want 4", got)
	}
	if got := attribute(router, "devices_active"); got != float64(2) {
		t.Errorf("devices_active = %v, want the two seen in the last fortnight", got)
	}
	if got := attribute(router, "address"); got != "router.example.com" {
		t.Errorf("address = %v, want the configured one", got)
	}

	home := entities["network:test/"+homeUUID]
	if got := attribute(home, "devices_known"); got != float64(3) {
		t.Errorf("home devices_known = %v, want the three attached to it", got)
	}
}

// A phone rotating its address per network is a new device to anything keyed
// on MAC, and the attribute is what stops that reading as a mystery.
func TestARandomisedMACIsFlagged(t *testing.T) {
	entities := byRef(batch(t, client(t, house(), nil)))

	if got := attribute(entities["device:test/b2-bb-cc-00-00-04"], "randomised_mac"); got != true {
		t.Errorf("randomised_mac = %v, want true for a locally administered address", got)
	}
	if got := attribute(entities["device:test/a8-bb-cc-00-00-01"], "randomised_mac"); got != nil {
		t.Errorf("randomised_mac = %v, want nothing for a burned in address", got)
	}
}

// Reads are pipelined in chunks, so the boundary is where a device would go
// missing without anything failing.
func TestEveryDeviceSurvivesThePipelineBoundary(t *testing.T) {
	box := &stub{hashes: map[string]map[string]string{}, page: 7}
	const many = pipeline*2 + 3
	for i := range many {
		box.hashes[fmt.Sprintf("host:mac:A8:BB:CC:00:%02X:%02X", i/256, i%256)] = map[string]string{
			"bname": fmt.Sprintf("device-%d", i), "lastActiveTimestamp": stamp(reading),
		}
	}

	built := batch(t, client(t, box, nil))

	devices := 0
	for _, entity := range built.GetEntities() {
		if entity.GetKind() == "device" {
			devices++
		}
	}
	if devices != many {
		t.Errorf("devices = %d, want %d", devices, many)
	}
}

// A batch that cannot be read is an error, never an empty batch. An empty
// batch is a claim that the network is empty.
func TestAnUnreachableBoxIsAnErrorNotAnEmptyBatch(t *testing.T) {
	broken := &Client{Open: func(context.Context) (net.Conn, error) {
		return nil, errors.New("no route to the box")
	}}

	built, err := broken.Batch(t.Context())
	if err == nil {
		t.Fatal("an unreachable box returned a batch")
	}
	if built != nil {
		t.Errorf("an unreachable box returned %d entities", len(built.GetEntities()))
	}
}

func TestAClientWithNoWayToReachTheBoxSaysSo(t *testing.T) {
	if _, err := (&Client{}).Batch(t.Context()); err == nil {
		t.Fatal("a client with no transport read something")
	}
}

// The batch has to be stable or every run diffs against itself.
func TestTheBatchIsOrdered(t *testing.T) {
	built := batch(t, client(t, house(), nil))

	for i := 1; i < len(built.GetEntities()); i++ {
		if built.GetEntities()[i-1].GetRef() > built.GetEntities()[i].GetRef() {
			t.Fatalf("entities are unordered at %d", i)
		}
	}
}
