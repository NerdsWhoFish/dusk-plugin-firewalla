package firewalla

import (
	"os"
	"testing"

	"github.com/NerdsWhoFish/dusk-plugin-sdk/conformance"
)

// TestLive reads a real box, which is the only way to know its Redis holds
// these fields and its sshd permits forwarding. Read only, and run by
// `make live`. See the README for the variables it wants.
func TestLive(t *testing.T) {
	host := os.Getenv("FIREWALLA_HOST")
	if host == "" {
		t.Skip("set FIREWALLA_HOST to read a real box")
	}

	access := Access{
		Host:          host,
		User:          os.Getenv("FIREWALLA_USER"),
		Password:      os.Getenv("FIREWALLA_PASSWORD"),
		PrivateKey:    os.Getenv("FIREWALLA_PRIVATE_KEY"),
		KeyPassphrase: os.Getenv("FIREWALLA_KEY_PASSPHRASE"),
		HostKey:       os.Getenv("FIREWALLA_HOST_KEY"),
	}

	client := &Client{Open: access.Tunnel, Namespace: "live", Address: host}

	if err := client.Ping(t.Context()); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	batch, err := client.Batch(t.Context())
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}

	kinds := map[string]int{}
	statuses := map[string]int{}
	for _, entity := range batch.GetEntities() {
		kinds[entity.GetKind()]++
		if status, ok := entity.GetAttributes().AsMap()["status"].(string); ok {
			statuses[status]++
		}

		if _, _, _, err := conformance.ParseRef(entity.GetRef()); err != nil {
			t.Errorf("%s: %v", entity.GetRef(), err)
		}
	}

	t.Logf("entities by kind: %v", kinds)
	t.Logf("devices by status: %v", statuses)
	t.Logf("relations: %d, partial: %v", len(batch.GetRelations()), batch.GetPartial())

	if kinds["network"] == 0 {
		t.Error("a real box serves at least one network")
	}
	if kinds["device"] == 0 {
		t.Error("a real box has seen at least one device")
	}
}
