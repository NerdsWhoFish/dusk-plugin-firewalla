// Package serve implements PluginService over the socket the host provides.
package serve

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	duskv1alpha1 "github.com/NerdsWhoFish/dusk-plugin-sdk/gen/dusk/v1alpha1"

	"github.com/NerdsWhoFish/dusk-plugin-firewalla/pkg/firewalla"
)

// PluginID names this plugin to the host and scopes its observations.
const PluginID = "firewalla"

// SchemaVersion is the contract this plugin was compiled against.
const SchemaVersion = "v1alpha1"

// Connect opens a box from validated settings. Injectable so tests need no
// router and no SSH server.
type Connect func(settings Settings) *firewalla.Client

// Server implements duskv1alpha1.PluginServiceServer.
type Server struct {
	duskv1alpha1.UnimplementedPluginServiceServer

	// Version is the plugin's own release, reported by Describe.
	Version string

	// Connect defaults to a client tunnelling to the box over SSH.
	Connect Connect
}

func (s *Server) connect() Connect {
	if s.Connect != nil {
		return s.Connect
	}
	return func(settings Settings) *firewalla.Client {
		return &firewalla.Client{
			Open:         settings.Access.Tunnel,
			Namespace:    settings.Namespace,
			Address:      settings.Access.Host,
			ActiveWithin: settings.ActiveWithin,
			ForgetAfter:  settings.ForgetAfter,
		}
	}
}

// Describe declares what this plugin emits and what it needs. It declares no
// actions on purpose: see adr/0004.
func (s *Server) Describe(context.Context, *duskv1alpha1.DescribeRequest) (*duskv1alpha1.DescribeResponse, error) {
	return &duskv1alpha1.DescribeResponse{
		PluginId:      PluginID,
		Version:       s.Version,
		SchemaVersion: SchemaVersion,
		EmitsKinds:    []string{"router", "network", "device"},
		ConfigFields:  configFields(),
		Ui:            views(),

		// One box is one small Redis on a router the whole house is behind, so
		// two configurations naming it queue rather than both leaning on it.
		Budget: &duskv1alpha1.SourceBudget{
			KeyFields:         []string{"host"},
			MaxConcurrent:     1,
			MinSpacingSeconds: 300,
		},
	}, nil
}

func configFields() []*duskv1alpha1.ConfigField {
	return []*duskv1alpha1.ConfigField{
		{
			Name:     "host",
			Label:    "Box address",
			Help:     "Where the box answers SSH, for example 10.0.0.1 or router.example.com.",
			Type:     duskv1alpha1.ConfigFieldType_CONFIG_FIELD_TYPE_STRING,
			Required: true,
		},
		{
			Name:         "port",
			Label:        "SSH port",
			Help:         "Leave at 22 unless the box was moved.",
			Type:         duskv1alpha1.ConfigFieldType_CONFIG_FIELD_TYPE_INT,
			DefaultValue: "22",
		},
		{
			Name:         "user",
			Label:        "SSH user",
			Help:         "The box's shell account.",
			Type:         duskv1alpha1.ConfigFieldType_CONFIG_FIELD_TYPE_STRING,
			DefaultValue: "pi",
		},
		{
			Name:  "host_key",
			Label: "Host key",
			Help: "The box's SSH host key, as ssh-keyscan prints it. " +
				"Required: without one, whatever answers on that address is trusted.",
			Type:     duskv1alpha1.ConfigFieldType_CONFIG_FIELD_TYPE_STRING,
			Required: true,
		},
		{
			Name:      "password",
			Label:     "SSH password",
			Help:      "The password the box shows under Settings, Advanced, SSH. Set this or a private key.",
			Type:      duskv1alpha1.ConfigFieldType_CONFIG_FIELD_TYPE_STRING,
			Sensitive: true,
		},
		{
			Name:      "private_key",
			Label:     "SSH private key",
			Help:      "A PEM private key the box will accept, used in preference to a password.",
			Type:      duskv1alpha1.ConfigFieldType_CONFIG_FIELD_TYPE_STRING,
			Sensitive: true,
		},
		{
			Name:      "key_passphrase",
			Label:     "Private key passphrase",
			Help:      "Only when the private key is encrypted.",
			Type:      duskv1alpha1.ConfigFieldType_CONFIG_FIELD_TYPE_STRING,
			Sensitive: true,
		},
		{
			Name:  "namespace",
			Label: "Namespace",
			Help:  "What refs are namespaced by. Defaults to firewalla.",
			Type:  duskv1alpha1.ConfigFieldType_CONFIG_FIELD_TYPE_STRING,
		},
		{
			Name:  "active_within",
			Label: "Active within",
			Help: "How recently a device must have been seen to be called active. " +
				"Everything older is still emitted, marked idle.",
			Type:         duskv1alpha1.ConfigFieldType_CONFIG_FIELD_TYPE_DURATION,
			DefaultValue: "336h",
		},
		{
			Name:  "forget_after",
			Label: "Forget after",
			Help: "Stop emitting devices unseen for longer than this, which lets Dusk delete them. " +
				"Empty means never, and empty is the default.",
			Type: duskv1alpha1.ConfigFieldType_CONFIG_FIELD_TYPE_DURATION,
		},
	}
}

// views are declared rather than drawn. Everything worth showing about a
// device is a field, and a plugin that ships no JavaScript needs no decision
// about whether to trust it.
func views() []*duskv1alpha1.UIContribution {
	return []*duskv1alpha1.UIContribution{
		{
			Title:          "This device",
			AppliesToKinds: []string{"device"},
			Spec: &duskv1alpha1.ViewSpec{
				Layout: duskv1alpha1.ViewLayout_VIEW_LAYOUT_BADGES,
				Empty:  "The box knows nothing else about this device.",
				Fields: []*duskv1alpha1.ViewField{
					{Source: "status", Label: "Status", Format: duskv1alpha1.ViewFormat_VIEW_FORMAT_BADGE},
					{Source: "ipv4", Label: "Address", Format: duskv1alpha1.ViewFormat_VIEW_FORMAT_CODE},
					{Source: "mac", Label: "MAC", Format: duskv1alpha1.ViewFormat_VIEW_FORMAT_CODE},
					{Source: "vendor", Label: "Vendor"},
					{Source: "device_type", Label: "Type"},
					{Source: "network_ref", Label: "Network", Format: duskv1alpha1.ViewFormat_VIEW_FORMAT_LINK},
					{Source: "last_seen", Label: "Last seen", Format: duskv1alpha1.ViewFormat_VIEW_FORMAT_TIMESTAMP},
					{Source: "first_seen", Label: "First seen", Format: duskv1alpha1.ViewFormat_VIEW_FORMAT_TIMESTAMP},
					{Source: "randomised_mac", Label: "Randomised MAC", Format: duskv1alpha1.ViewFormat_VIEW_FORMAT_BADGE},
				},
			},
		},
		{
			Title:          "This network",
			AppliesToKinds: []string{"network"},
			Spec: &duskv1alpha1.ViewSpec{
				Layout: duskv1alpha1.ViewLayout_VIEW_LAYOUT_BADGES,
				Empty:  "The box knows nothing else about this network.",
				Fields: []*duskv1alpha1.ViewField{
					{Source: "type", Label: "Type", Format: duskv1alpha1.ViewFormat_VIEW_FORMAT_BADGE},
					{Source: "interface", Label: "Interface", Format: duskv1alpha1.ViewFormat_VIEW_FORMAT_CODE},
					{Source: "subnet", Label: "Subnet", Format: duskv1alpha1.ViewFormat_VIEW_FORMAT_CODE},
					{Source: "gateway", Label: "Gateway", Format: duskv1alpha1.ViewFormat_VIEW_FORMAT_CODE},
					{Source: "dns", Label: "DNS"},
					{Source: "devices_known", Label: "Devices"},
					{Source: "devices_active", Label: "Active"},
				},
			},
		},
		{
			Title:          "This box",
			AppliesToKinds: []string{"router"},
			Spec: &duskv1alpha1.ViewSpec{
				Layout: duskv1alpha1.ViewLayout_VIEW_LAYOUT_BADGES,
				Empty:  "Nothing has been read off this box yet.",
				Fields: []*duskv1alpha1.ViewField{
					{Source: "address", Label: "Address", Format: duskv1alpha1.ViewFormat_VIEW_FORMAT_CODE},
					{Source: "networks", Label: "Networks"},
					{Source: "devices_known", Label: "Devices known"},
					{Source: "devices_active", Label: "Devices active"},
				},
			},
		},
	}
}

// ValidateConfig proves the credential, the pinned host key and the tunnel all
// work, so a wrong one fails where it was entered.
func (s *Server) ValidateConfig(ctx context.Context, request *duskv1alpha1.ValidateConfigRequest) (*duskv1alpha1.ValidateConfigResponse, error) {
	settings, fieldErrors := readConfig(request.GetConfig())
	if len(fieldErrors) > 0 {
		return &duskv1alpha1.ValidateConfigResponse{
			Ok: false, Message: "that configuration is incomplete", FieldErrors: fieldErrors,
		}, nil
	}

	if err := s.connect()(settings).Ping(ctx); err != nil {
		return &duskv1alpha1.ValidateConfigResponse{Ok: false, Message: err.Error()}, nil
	}
	return &duskv1alpha1.ValidateConfigResponse{Ok: true, Message: "read the box at " + settings.Access.Host}, nil
}

// Ingest sends one complete batch and closes, so a call is one whole view the
// host can replace the last one with.
func (s *Server) Ingest(request *duskv1alpha1.IngestRequest, stream duskv1alpha1.PluginService_IngestServer) error {
	settings, fieldErrors := readConfig(request.GetConfig())
	if len(fieldErrors) > 0 {
		return status.Errorf(codes.InvalidArgument, "incomplete configuration: %v", fieldErrors)
	}

	batch, err := s.connect()(settings).Batch(stream.Context())
	if err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}
	return stream.Send(&duskv1alpha1.IngestResponse{Batch: batch})
}

// refusal is what both action RPCs say. Naming the reason beats a bare
// Unimplemented, which reads like an oversight rather than a decision.
const refusal = "this plugin declares no actions: the box routes the whole network and is read only (adr/0004)"

// DryRun answers rather than erroring, because "not supported" is a valid
// answer and Dusk surfaces it.
func (s *Server) DryRun(context.Context, *duskv1alpha1.DryRunRequest) (*duskv1alpha1.DryRunResponse, error) {
	return &duskv1alpha1.DryRunResponse{Supported: false, Summary: refusal}, nil
}

// Invoke refuses everything. There is nothing to invoke.
func (s *Server) Invoke(context.Context, *duskv1alpha1.InvokeRequest) (*duskv1alpha1.InvokeResponse, error) {
	return nil, status.Error(codes.Unimplemented, refusal)
}

// Settings is this plugin's configuration, read from what Describe declared.
type Settings struct {
	Access       firewalla.Access
	Namespace    string
	ActiveWithin time.Duration
	ForgetAfter  time.Duration
}

func readConfig(config *structpb.Struct) (Settings, map[string]string) {
	fields := config.GetFields()
	problems := map[string]string{}

	read := func(name string) string { return strings.TrimSpace(fields[name].GetStringValue()) }

	settings := Settings{
		Access: firewalla.Access{
			Host:          read("host"),
			Port:          readPort(fields["port"], problems),
			User:          read("user"),
			Password:      read("password"),
			PrivateKey:    strings.TrimSpace(fields["private_key"].GetStringValue()),
			KeyPassphrase: fields["key_passphrase"].GetStringValue(),
			HostKey:       read("host_key"),
		},
		Namespace:    read("namespace"),
		ActiveWithin: readDuration("active_within", read("active_within"), problems),
		ForgetAfter:  readDuration("forget_after", read("forget_after"), problems),
	}

	if settings.Access.Host == "" {
		problems["host"] = "where does the box answer SSH?"
	}
	if settings.Access.Password == "" && settings.Access.PrivateKey == "" {
		problems["password"] = "the box needs a password or a private key"
	}
	if _, err := firewalla.ParseHostKey(settings.Access.HostKey); err != nil {
		problems["host_key"] = err.Error()
	}

	// Forgetting something this plugin would call active is not a policy, it is
	// a deletion loop: idle at ingest, gone at the next one, back after that.
	active := settings.ActiveWithin
	if active == 0 {
		active = firewalla.DefaultActiveWithin
	}
	if settings.ForgetAfter > 0 && settings.ForgetAfter < active {
		problems["forget_after"] = fmt.Sprintf("must be at least the active window, %s", active)
	}

	if len(problems) == 0 {
		return settings, nil
	}
	return settings, problems
}

// readPort takes the number Dusk should send and the string a hand written
// config is likely to hold.
func readPort(value *structpb.Value, problems map[string]string) int {
	if number := value.GetNumberValue(); number > 0 {
		return int(number)
	}

	raw := strings.TrimSpace(value.GetStringValue())
	if raw == "" {
		return 0
	}

	port, err := strconv.Atoi(raw)
	if err != nil || port <= 0 {
		problems["port"] = fmt.Sprintf("%q is not a port number", raw)
		return 0
	}
	return port
}

func readDuration(name, raw string, problems map[string]string) time.Duration {
	if raw == "" {
		return 0
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < 0 {
		problems[name] = fmt.Sprintf("%q is not a duration, try something like 336h", raw)
		return 0
	}
	return parsed
}
