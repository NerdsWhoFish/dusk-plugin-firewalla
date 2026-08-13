package firewalla

import (
	"testing"
	"time"
)

// The box writes float seconds. Milliseconds are accepted too, because a
// timestamp read a thousand times too large lands in the year 5138 and reads
// as a device from the future rather than a parse bug.
func TestUnixTime(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want time.Time
	}{
		{name: "float seconds", raw: "1767225600.5", want: time.Unix(1767225600, 500000000).UTC()},
		{name: "whole seconds", raw: "1767225600", want: time.Unix(1767225600, 0).UTC()},
		{name: "milliseconds", raw: "1767225600000", want: time.Unix(1767225600, 0).UTC()},
		{name: "empty", raw: "", want: time.Time{}},
		{name: "zero", raw: "0", want: time.Time{}},
		{name: "nonsense", raw: "never", want: time.Time{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := unixTime(test.raw); !got.Equal(test.want) {
				t.Errorf("unixTime(%q) = %v, want %v", test.raw, got, test.want)
			}
		})
	}
}

func TestDetectedType(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "a detection blob", raw: `{"type":"phone","brand":"Example"}`, want: "phone"},
		{name: "a blob with no type", raw: `{"brand":"Example"}`},
		{name: "not JSON at all", raw: "phone"},
		{name: "empty"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := detectedType(test.raw); got != test.want {
				t.Errorf("detectedType(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

// The box has written this field both ways across releases.
func TestStringList(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "a JSON array", raw: `["10.0.0.1","10.0.0.2"]`, want: []string{"10.0.0.1", "10.0.0.2"}},
		{name: "a comma list", raw: "10.0.0.1, 10.0.0.2", want: []string{"10.0.0.1", "10.0.0.2"}},
		{name: "one value", raw: "10.0.0.1", want: []string{"10.0.0.1"}},
		{name: "empty"},
		{name: "an empty array", raw: "[]"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := stringList(test.raw)
			if len(got) != len(test.want) {
				t.Fatalf("stringList(%q) = %v, want %v", test.raw, got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Errorf("stringList(%q)[%d] = %q, want %q", test.raw, i, got[i], test.want[i])
				}
			}
		})
	}
}

// The locally administered bit is what a phone sets when it makes an address
// up for one network.
func TestRandomisedMAC(t *testing.T) {
	tests := []struct {
		mac  string
		want bool
	}{
		{mac: "A8:BB:CC:00:00:01"},
		{mac: "B2:BB:CC:00:00:04", want: true},
		{mac: "AA:BB:CC:00:00:01", want: true},
		{mac: "not-a-mac"},
		{mac: ""},
	}

	for _, test := range tests {
		t.Run(test.mac, func(t *testing.T) {
			if got := (Device{MAC: test.mac}).RandomisedMAC(); got != test.want {
				t.Errorf("RandomisedMAC(%q) = %v, want %v", test.mac, got, test.want)
			}
		})
	}
}

// A device with no name still needs something better than raw hex to be found
// by, and a network with no name still needs a title.
func TestTitlesFallBackRatherThanGoingBlank(t *testing.T) {
	devices := []struct {
		name   string
		device Device
		want   string
	}{
		{name: "named", device: Device{Name: "hallway", MAC: "A8:BB:CC:00:00:01"}, want: "hallway"},
		{
			name:   "vendor only",
			device: Device{Vendor: "Example Devices", MAC: "A8:BB:CC:00:00:01"},
			want:   "Example Devices 00:01",
		},
		{name: "nothing at all", device: Device{MAC: "A8:BB:CC:00:00:01"}, want: "A8:BB:CC:00:00:01"},
	}
	for _, test := range devices {
		t.Run(test.name, func(t *testing.T) {
			if got := test.device.Title(); got != test.want {
				t.Errorf("Title() = %q, want %q", got, test.want)
			}
		})
	}

	networks := []struct {
		name    string
		network Network
		want    string
	}{
		{name: "named", network: Network{Name: "Home", UUID: homeUUID}, want: "Home"},
		{name: "interface only", network: Network{Interface: "eth0", UUID: homeUUID}, want: "eth0"},
		{name: "nothing at all", network: Network{UUID: homeUUID}, want: homeUUID},
	}
	for _, test := range networks {
		t.Run(test.name, func(t *testing.T) {
			if got := test.network.Title(); got != test.want {
				t.Errorf("Title() = %q, want %q", got, test.want)
			}
		})
	}
}

// The operator's own label wins over the one the box worked out.
func TestReadDevicePrefersTheNameSomebodyTyped(t *testing.T) {
	device := readDevice("A8:BB:CC:00:00:01", hash{
		"name": "hallway", "bname": "esp-1234", "macVendor": "Example Devices",
	})

	if device.Name != "hallway" {
		t.Errorf("name = %q, want the typed one", device.Name)
	}
	if device.MAC != "A8:BB:CC:00:00:01" {
		t.Errorf("mac = %q, want it upper case and unchanged", device.MAC)
	}
}

func TestReadNetworkToleratesAFieldItHasNeverSeen(t *testing.T) {
	network := readNetwork(homeUUID, hash{"name": "Home", "somethingNew": "whatever"})

	if network.Title() != "Home" {
		t.Errorf("title = %q, want Home", network.Title())
	}
	if network.Subnet != "" {
		t.Errorf("subnet = %q, want nothing rather than a guess", network.Subnet)
	}
}
