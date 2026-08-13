package serve_test

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"

	duskv1alpha1 "github.com/NerdsWhoFish/dusk-plugin-sdk/gen/dusk/v1alpha1"

	"github.com/NerdsWhoFish/dusk-plugin-sdk/conformance"

	"github.com/NerdsWhoFish/dusk-plugin-firewalla/internal/serve"
	"github.com/NerdsWhoFish/dusk-plugin-firewalla/pkg/firewalla"
)

// emptyBox is a Redis holding nothing, which is enough to prove the wiring
// from gRPC down to a batch without restating the inventory tests.
func emptyBox(t *testing.T) firewalla.Open {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go answerEmpty(conn)
		}
	}()

	address := listener.Addr().String()
	return func(ctx context.Context) (net.Conn, error) {
		return new(net.Dialer).DialContext(ctx, "tcp", address)
	}
}

func answerEmpty(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if !strings.HasPrefix(line, "*") {
			continue
		}
		if _, err := conn.Write([]byte("*2\r\n$1\r\n0\r\n*0\r\n")); err != nil {
			return
		}
	}
}

func dial(t *testing.T, open firewalla.Open) duskv1alpha1.PluginServiceClient {
	t.Helper()

	// Not t.TempDir: on macOS its path alone is most of the ~104 byte limit a
	// unix socket address has.
	dir, err := os.MkdirTemp("/tmp", "dpf")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	socket := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := grpc.NewServer()
	duskv1alpha1.RegisterPluginServiceServer(server, &serve.Server{
		Version: "test",
		Connect: func(settings serve.Settings) *firewalla.Client {
			return &firewalla.Client{
				Open:         open,
				Namespace:    settings.Namespace,
				Address:      settings.Access.Host,
				ActiveWithin: settings.ActiveWithin,
				ForgetAfter:  settings.ForgetAfter,
			}
		},
	})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return duskv1alpha1.NewPluginServiceClient(conn)
}

func hostKey(t *testing.T) string {
	t.Helper()

	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

func config(t *testing.T, change map[string]any) *structpb.Struct {
	t.Helper()

	fields := map[string]any{
		"host":      "router.example.com",
		"host_key":  hostKey(t),
		"password":  "not-the-real-one",
		"namespace": "test",
	}
	for name, value := range change {
		if value == nil {
			delete(fields, name)
			continue
		}
		fields[name] = value
	}

	built, err := structpb.NewStruct(fields)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return built
}

func describe(t *testing.T) *duskv1alpha1.DescribeResponse {
	t.Helper()

	described, err := dial(t, emptyBox(t)).Describe(t.Context(), &duskv1alpha1.DescribeRequest{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	return described
}

// Everything downstream is built from Describe, so a plugin that describes
// itself wrongly fails later, in Dusk, with an error naming Dusk.
func TestDescribeIsConformant(t *testing.T) {
	if result := conformance.ValidateDescribe(describe(t)); !result.OK() {
		t.Fatalf("this plugin describes itself wrongly:\n%s", result.Error())
	}
}

// ADR-0004: the box routes the whole network, so this plugin only ever reads.
// An action arriving here later is a decision somebody has to argue for.
func TestADR0004_ThisPluginDeclaresNoActions(t *testing.T) {
	if actions := describe(t).GetActions(); len(actions) != 0 {
		t.Fatalf("declared %d actions against a router this plugin is meant to only read", len(actions))
	}
}

// Every view is declared rather than drawn, so the plugin ships no JavaScript
// and Dusk makes no trust decision about it.
func TestEveryViewIsDeclaredRatherThanDrawn(t *testing.T) {
	views := describe(t).GetUi()
	if len(views) == 0 {
		t.Fatal("declared no views at all")
	}

	for _, view := range views {
		if view.GetElement() != "" || view.GetAsset() != "" {
			t.Errorf("%q ships JavaScript, which this plugin has no reason to", view.GetTitle())
		}
		if view.GetSpec() == nil {
			t.Errorf("%q is neither declared nor drawn, so it has no rendering", view.GetTitle())
		}
	}
}

func TestGetAssetServesNothingBecauseNothingIsDeclared(t *testing.T) {
	stream, err := dial(t, emptyBox(t)).GetAsset(t.Context(), &duskv1alpha1.GetAssetRequest{Name: "anything.js"})
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	if _, err := stream.Recv(); err == nil {
		t.Error("served an asset this plugin never declared")
	}
}

// The budget stops two configurations of one box leaning on its Redis at once.
func TestDescribeQueuesConfigurationsSharingOneBox(t *testing.T) {
	budget := describe(t).GetBudget()

	if got := budget.GetKeyFields(); len(got) != 1 || got[0] != "host" {
		t.Errorf("key fields = %v, want the box's address", got)
	}
	if budget.GetMaxConcurrent() != 1 {
		t.Errorf("max concurrent = %d, want one read of a router at a time", budget.GetMaxConcurrent())
	}
	if budget.GetMinSpacingSeconds() <= 0 {
		t.Error("no spacing between reads of a router that is also doing its actual job")
	}
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name   string
		change map[string]any
		field  string
	}{
		{name: "complete"},
		{name: "no address", change: map[string]any{"host": nil}, field: "host"},
		{name: "no credential", change: map[string]any{"password": nil}, field: "password"},
		{name: "no pinned host key", change: map[string]any{"host_key": nil}, field: "host_key"},
		{name: "a host key that is not one", change: map[string]any{"host_key": "trust me"}, field: "host_key"},
		{name: "a port that is not a number", change: map[string]any{"port": "front"}, field: "port"},
		{name: "a window that is not a duration", change: map[string]any{"active_within": "ages"}, field: "active_within"},
		{
			// Forgetting something the same run calls idle is a deletion loop,
			// not a policy.
			name:   "forgetting sooner than idling",
			change: map[string]any{"active_within": "336h", "forget_after": "1h"},
			field:  "forget_after",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := dial(t, emptyBox(t)).ValidateConfig(t.Context(),
				&duskv1alpha1.ValidateConfigRequest{Config: config(t, test.change)})
			if err != nil {
				t.Fatalf("ValidateConfig: %v", err)
			}

			if test.field == "" {
				if !got.GetOk() {
					t.Fatalf("a complete configuration was refused: %s", got.GetMessage())
				}
				return
			}

			if got.GetOk() {
				t.Fatal("accepted a configuration that cannot work")
			}
			if _, ok := got.GetFieldErrors()[test.field]; !ok {
				t.Errorf("field errors = %v, want the failure attributed to %s", got.GetFieldErrors(), test.field)
			}
		})
	}
}

// An empty box is a real answer, and the box itself is still an entity: that
// is what the networks and devices hang off when there are any.
func TestIngestEmitsTheBoxEvenWhenItKnowsNothing(t *testing.T) {
	stream, err := dial(t, emptyBox(t)).Ingest(t.Context(), &duskv1alpha1.IngestRequest{Config: config(t, nil)})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	response, err := stream.Recv()
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	batch := response.GetBatch()

	if len(batch.GetEntities()) != 1 {
		t.Fatalf("entities = %d, want just the box", len(batch.GetEntities()))
	}
	if got := batch.GetEntities()[0].GetRef(); got != "router:test/firewalla" {
		t.Errorf("ref = %q, want the box", got)
	}
	if batch.GetPartial() {
		t.Error("a complete read of an empty box claimed to be partial")
	}
}

func TestIngestRefusesAnIncompleteConfiguration(t *testing.T) {
	stream, err := dial(t, emptyBox(t)).Ingest(t.Context(),
		&duskv1alpha1.IngestRequest{Config: config(t, map[string]any{"host": nil})})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if _, err := stream.Recv(); err == nil {
		t.Fatal("ingested with nowhere to read from")
	}
}

// ADR-0004 again, from the other side: a caller that tries anyway is told why
// rather than getting a bare Unimplemented that reads like an oversight.
func TestADR0004_ActingOnTheBoxIsRefusedWithAReason(t *testing.T) {
	client := dial(t, emptyBox(t))

	preview, err := client.DryRun(t.Context(), &duskv1alpha1.DryRunRequest{
		Ref: "device:test/a8-bb-cc-00-00-01", Action: "block",
	})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if preview.GetSupported() {
		t.Fatal("previewed an action this plugin does not have")
	}
	if !strings.Contains(preview.GetSummary(), "read only") {
		t.Errorf("summary = %q, want it to say why", preview.GetSummary())
	}

	_, err = client.Invoke(t.Context(), &duskv1alpha1.InvokeRequest{
		Ref: "device:test/a8-bb-cc-00-00-01", Action: "block",
	})
	if err == nil {
		t.Fatal("invoked an action this plugin does not have")
	}
	if !strings.Contains(err.Error(), "read only") {
		t.Errorf("refusal = %q, want it to say why", err)
	}
}

// Dusk sends an int field as a number and a hand written config holds a
// string, and both have to reach the same port.
func TestThePortIsReadFromANumberOrAString(t *testing.T) {
	for _, port := range []any{float64(2222), "2222"} {
		got, err := dial(t, emptyBox(t)).ValidateConfig(t.Context(),
			&duskv1alpha1.ValidateConfigRequest{Config: config(t, map[string]any{"port": port})})
		if err != nil {
			t.Fatalf("ValidateConfig: %v", err)
		}
		if !got.GetOk() {
			t.Errorf("port %v was refused: %s %v", port, got.GetMessage(), got.GetFieldErrors())
		}
	}
}

// A connection that cannot be opened has to be an error, so Dusk keeps what it
// already has rather than treating silence as an empty network.
func TestIngestFailsRatherThanEmptyingTheCatalog(t *testing.T) {
	unreachable := func(context.Context) (net.Conn, error) {
		return nil, errors.New("no route to the box")
	}

	stream, err := dial(t, unreachable).Ingest(t.Context(), &duskv1alpha1.IngestRequest{Config: config(t, nil)})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if _, err := stream.Recv(); err == nil {
		t.Fatal("an unreachable box streamed a batch")
	}
}
