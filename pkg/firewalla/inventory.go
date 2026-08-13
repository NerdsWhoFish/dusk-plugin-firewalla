package firewalla

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// Device is one MAC address the box has seen, ever.
type Device struct {
	// MAC is the identity. The name is whatever somebody typed and the address
	// is whatever DHCP last handed out, so neither of them is.
	MAC string

	// Name is the box's label for it, the operator's if they set one.
	Name string

	// IPv4 is the address it last held.
	IPv4 string

	// Vendor is the OUI lookup the box did on the MAC.
	Vendor string

	// NetworkUUID names the network it is attached to.
	NetworkUUID string

	// LastSeen and FirstSeen are zero when the box recorded neither.
	LastSeen  time.Time
	FirstSeen time.Time

	// Type is the box's own guess at what the device is.
	Type string
}

// Title is what to call it: the name if there is one, and something better
// than raw hex if there is not.
func (d Device) Title() string {
	switch {
	case d.Name != "":
		return d.Name
	case d.Vendor != "":
		return d.Vendor + " " + tailOf(d.MAC)
	default:
		return d.MAC
	}
}

// RandomisedMAC reports the locally administered bit, which is what a phone
// rotating its address per network sets. Such a device is a new one to any
// inventory keyed on MAC, this one included.
func (d Device) RandomisedMAC() bool {
	octet, err := strconv.ParseUint(firstOctet(d.MAC), 16, 8)
	if err != nil {
		return false
	}
	return octet&0x02 != 0
}

// Network is one of the networks the box serves.
type Network struct {
	// UUID is the identity. The name is editable in the app.
	UUID string

	Name      string
	Interface string
	Subnet    string
	Gateway   string

	// Type is the box's own word for it, usually lan or wan.
	Type string

	DNS []string
}

// Title is the network's name, falling back to something a human can place.
func (n Network) Title() string {
	switch {
	case n.Name != "":
		return n.Name
	case n.Interface != "":
		return n.Interface
	default:
		return n.UUID
	}
}

func readDevice(mac string, fields hash) Device {
	return Device{
		MAC:         strings.ToUpper(mac),
		Name:        fields.pick("name", "bname"),
		IPv4:        fields.pick("ipv4Addr", "ipv4"),
		Vendor:      fields.pick("macVendor", "vendor"),
		NetworkUUID: fields.pick("intf_uuid"),
		LastSeen:    unixTime(fields.pick("lastActiveTimestamp", "lastActive")),
		FirstSeen:   unixTime(fields.pick("firstFoundTimestamp", "firstFound")),
		Type:        detectedType(fields["detect"]),
	}
}

func readNetwork(uuid string, fields hash) Network {
	return Network{
		UUID:      uuid,
		Name:      fields.pick("name"),
		Interface: fields.pick("intf", "interface"),
		Subnet:    fields.pick("ipv4Subnet", "subnet", "ipv4"),
		Gateway:   fields.pick("gateway"),
		Type:      fields.pick("type"),
		DNS:       stringList(fields.pick("dns")),
	}
}

// pick takes the first field with a value. The box's schema is not published
// and has renamed fields between releases, so a missing one is normal.
func (h hash) pick(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(h[name]); value != "" {
			return value
		}
	}
	return ""
}

// unixTime reads the box's float seconds. Values large enough to be
// milliseconds are treated as such rather than landing in the year 5138.
func unixTime(raw string) time.Time {
	seconds, err := strconv.ParseFloat(raw, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}
	}
	if seconds > 1e11 {
		seconds /= 1000
	}
	return time.Unix(0, int64(seconds*float64(time.Second))).UTC()
}

// detectedType unwraps the box's detection blob, which is JSON in a hash field
// rather than fields of its own.
func detectedType(raw string) string {
	if raw == "" {
		return ""
	}

	var detected struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(raw), &detected); err != nil {
		return ""
	}
	return strings.TrimSpace(detected.Type)
}

// stringList reads a field that is a JSON array in some releases and a comma
// separated string in others.
func stringList(raw string) []string {
	if raw == "" {
		return nil
	}

	var parsed []string
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
		return trimAll(parsed)
	}
	return trimAll(strings.Split(raw, ","))
}

func trimAll(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func firstOctet(mac string) string {
	octet, _, _ := strings.Cut(mac, ":")
	return octet
}

// tailOf is the last two octets, which is enough to tell two of a vendor's
// nameless devices apart.
func tailOf(mac string) string {
	parts := strings.Split(mac, ":")
	if len(parts) < 2 {
		return mac
	}
	return strings.Join(parts[len(parts)-2:], ":")
}
