package importer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/shady2k/nocx/internal/profile"
	"gopkg.in/yaml.v3"
)

// TabbyConfig models the source envelope, not nocx's stored defaults.
type TabbyConfig struct {
	Version         int                      `yaml:"version"`
	Encrypted       bool                     `yaml:"encrypted"`
	Profiles        []TabbyProfile           `yaml:"profiles"`
	Groups          []TabbyGroup             `yaml:"groups"`
	ProfileDefaults map[string]tabbyDefaults `yaml:"profileDefaults"`
	Vault           *TabbyVault              `yaml:"vault"`
}

type tabbyDefaults struct {
	Options tabbyOptions `yaml:"options"`
}

// Nodes retain explicit false, zero, empty arrays and null during source merging.
type tabbyOptions map[string]yaml.Node

type TabbyProfile struct {
	ID      string       `yaml:"id"`
	Type    string       `yaml:"type"`
	Name    string       `yaml:"name"`
	Group   string       `yaml:"group"`
	Icon    string       `yaml:"icon"`
	Color   string       `yaml:"color"`
	Options tabbyOptions `yaml:"options"`
}

type TabbyGroup struct {
	ID            string                   `yaml:"id"`
	ParentGroupID string                   `yaml:"parentGroupId"`
	Name          string                   `yaml:"name"`
	Icon          string                   `yaml:"icon"`
	Color         string                   `yaml:"color"`
	Defaults      map[string]tabbyDefaults `yaml:"defaults"`
}

// Tabby's vault is always encrypted. Only the root has an encrypted flag,
// indicating whether the configuration itself lives inside vault.config.
type TabbyVault struct {
	Version  int    `yaml:"version"`
	Contents string `yaml:"contents"`
	KeySalt  string `yaml:"keySalt"`
	IV       string `yaml:"iv"`
}

type TabbyImport struct {
	Profiles []profile.SSHProfile
	Groups   []profile.ProfileGroup
	Secrets  []TabbySecret
	Warnings []ImportWarning
}

// ImportWarning contains field names and fixed explanations, never option values.
type ImportWarning struct {
	Profile string `json:"profile"`
	Field   string `json:"field"`
	Reason  string `json:"reason"`
}

// ParseTabbyConfig accepts exactly one mapping document. Decode errors are
// deliberately value-free: malformed option values may contain credentials.
func ParseTabbyConfig(data []byte) (*TabbyConfig, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("invalid Tabby configuration")
	}
	var cfg TabbyConfig
	if err := doc.Decode(&cfg); err != nil {
		return nil, errors.New("invalid Tabby configuration fields")
	}
	var trailing yaml.Node
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, errors.New("Tabby configuration must contain one document")
	}
	return &cfg, nil
}

// PrepareTabbyImport resolves source defaults before conversion and performs no
// I/O. The caller validates the complete destination merge before saving secrets.
func PrepareTabbyImport(data []byte, passphrase string) (*TabbyImport, error) {
	cfg, err := ParseTabbyConfig(data)
	if err != nil {
		return nil, err
	}
	result := &TabbyImport{}
	if cfg.Vault != nil {
		contents, err := DecryptTabbyVault(cfg.Vault, passphrase)
		if err != nil {
			return nil, err
		}
		result.Secrets = contents.DecodedSecrets()
		if cfg.Encrypted {
			inner, err := ParseTabbyConfig(contents.Config)
			if err != nil || inner.Encrypted || inner.Vault != nil {
				return nil, ErrDecryptFailed
			}
			cfg = inner
		}
	} else if cfg.Encrypted {
		return nil, ErrDecryptFailed
	}

	groups := make(map[string]TabbyGroup, len(cfg.Groups))
	for _, g := range cfg.Groups {
		if strings.TrimSpace(g.ID) == "" {
			return nil, errors.New("Tabby group ID is required")
		}
		if _, exists := groups[g.ID]; exists {
			return nil, errors.New("duplicate Tabby group ID")
		}
		groups[g.ID] = g
		result.Groups = append(result.Groups, profile.ProfileGroup{
			ID: g.ID, ParentGroupID: g.ParentGroupID, Name: g.Name, Icon: g.Icon, Color: g.Color, Editable: true,
		})
	}
	seen := make(map[string]bool, len(cfg.Profiles))
	for _, source := range cfg.Profiles {
		if source.Type != "ssh" {
			continue
		}
		if strings.TrimSpace(source.ID) == "" {
			return nil, errors.New("Tabby profile ID is required")
		}
		if seen[source.ID] {
			return nil, errors.New("duplicate Tabby profile ID")
		}
		seen[source.ID] = true
		effective := make(tabbyOptions)
		// Tabby applies only the owning group's defaults, never its ancestors.
		for _, layer := range []tabbyOptions{cfg.ProfileDefaults["ssh"].Options, groups[source.Group].Defaults["ssh"].Options, source.Options} {
			for key, value := range layer {
				effective[key] = value
			}
		}
		p, password, warnings, err := convertTabbyProfile(source, effective)
		if err != nil {
			return nil, err
		}
		result.Profiles = append(result.Profiles, p)
		result.Warnings = append(result.Warnings, warnings...)
		if password != "" {
			key, err := json.Marshal(struct {
				Host string `json:"host"`
				Port int    `json:"port"`
				User string `json:"user"`
			}{p.Options.Host, *p.Options.Port, *p.Options.User})
			if err != nil {
				return nil, errors.New("invalid inline password target")
			}
			value, err := json.Marshal(password)
			if err != nil {
				return nil, errors.New("invalid inline password")
			}
			if len(key) > tabbyMaxSecretKeyLen || len(value) > tabbyMaxSecretValueLen {
				return nil, errors.New("inline password or target exceeds import limit")
			}
			result.Secrets = append(result.Secrets, TabbySecret{Type: "ssh:password", Key: key, Value: value, ProfileID: p.ID})
		}
	}
	return result, nil
}

func convertTabbyProfile(source TabbyProfile, fields tabbyOptions) (profile.SSHProfile, string, []ImportWarning, error) {
	// Pin Tabby's supported provider defaults on each profile so nocx global
	// settings cannot accidentally change the imported connection semantics.
	o := profile.StoredSSHProfileOptions{
		Port: profile.Ptr(22), User: profile.Ptr("root"), Auth: profile.Ptr(profile.AuthAuto),
		KeepaliveInterval: profile.Ptr(5000), ReadyTimeout: profile.Ptr(0),
		AgentForward: profile.Ptr(false), JumpHost: profile.Ptr(""), KeyPath: profile.Ptr(""),
		Forwards: profile.Ptr([]profile.ForwardSpec{}),
	}
	var password string
	var warnings []ImportWarning
	label := source.Name
	if label == "" {
		label = source.ID
	}
	warn := func(field, reason string) {
		warnings = append(warnings, ImportWarning{Profile: label, Field: "options." + field, Reason: reason})
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		node := fields[key]
		var target any
		switch key {
		case "host":
			target = &o.Host
		case "port":
			target = o.Port
		case "user":
			target = o.User
		case "auth":
			target = o.Auth
		case "password":
			target = &password
		case "keepaliveInterval":
			target = o.KeepaliveInterval
		case "readyTimeout":
			target = o.ReadyTimeout
		case "jumpHost":
			target = o.JumpHost
		case "agentForward":
			target = o.AgentForward
		case "privateKeys":
			var paths []string
			if err := node.Decode(&paths); err != nil {
				return profile.SSHProfile{}, "", nil, errors.New("invalid Tabby privateKeys")
			}
			switch {
			case len(paths) > 1:
				warn(key, "Multiple private keys cannot be represented by one connection key")
			case len(paths) == 1 && strings.HasPrefix(paths[0], "vault://"):
				warn(key, "Vault-backed private keys are not supported")
			case len(paths) == 1 && strings.Contains(paths[0], "://"):
				warn(key, "Non-file private key sources are not supported")
			case len(paths) == 1:
				if strings.TrimSpace(paths[0]) == "" {
					return profile.SSHProfile{}, "", nil, errors.New("invalid Tabby privateKeys")
				}
				o.KeyPath = profile.Ptr(paths[0])
			}
			continue
		case "forwardedPorts":
			// Keep future tunnel settings visible rather than silently discarding
			// them when the native forward schema grows.
			for _, forward := range node.Content {
				if forward.Kind != yaml.MappingNode {
					continue
				}
				for i := 0; i+1 < len(forward.Content); i += 2 {
					field := forward.Content[i].Value
					switch field {
					case "type", "host", "port", "targetAddress", "targetPort", "description":
					default:
						warn("forwardedPorts."+field, "This Tabby forwarding option is not supported")
					}
				}
			}
			forwards, err := convertTabbyForwards(node)
			if err != nil {
				return profile.SSHProfile{}, "", nil, err
			}
			o.Forwards = &forwards
			continue
		default:
			warn(key, "This Tabby option is not supported")
			continue
		}
		if node.Tag != "!!null" {
			expected := "!!str"
			switch target.(type) {
			case *int:
				expected = "!!int"
			case *bool:
				expected = "!!bool"
			}
			if node.Tag != expected {
				return profile.SSHProfile{}, "", nil, fmt.Errorf("invalid Tabby option %s", key)
			}
		}
		if err := node.Decode(target); err != nil {
			return profile.SSHProfile{}, "", nil, fmt.Errorf("invalid Tabby option %s", key)
		}
	}
	if strings.TrimSpace(o.Host) == "" {
		return profile.SSHProfile{}, "", nil, errors.New("Tabby SSH host is required")
	}
	if *o.Port < 1 || *o.Port > 65535 || *o.KeepaliveInterval < 0 || *o.ReadyTimeout < 0 {
		return profile.SSHProfile{}, "", nil, errors.New("invalid Tabby SSH port or timeout")
	}
	switch *o.Auth {
	case profile.AuthAuto, profile.AuthPassword, profile.AuthPublicKey, profile.AuthAgent, profile.AuthKeyboardInteractive:
	default:
		return profile.SSHProfile{}, "", nil, errors.New("invalid Tabby authentication mode")
	}
	return profile.SSHProfile{
		Base:    profile.Base{ID: source.ID, Type: "ssh", Name: source.Name, Group: source.Group, Icon: source.Icon, Color: source.Color},
		Options: o,
	}, password, warnings, nil
}

func convertTabbyForwards(node yaml.Node) ([]profile.ForwardSpec, error) {
	var source []struct {
		Type          string `yaml:"type"`
		Host          string `yaml:"host"`
		Port          int    `yaml:"port"`
		TargetAddress string `yaml:"targetAddress"`
		TargetPort    int    `yaml:"targetPort"`
	}
	if err := node.Decode(&source); err != nil {
		return nil, errors.New("invalid Tabby forwardedPorts")
	}
	out := make([]profile.ForwardSpec, 0, len(source))
	for _, f := range source {
		direction := ""
		switch f.Type {
		case "Local":
			direction = "local"
		case "Remote":
			direction = "remote"
		case "Dynamic":
			direction = "dynamic"
		default:
			return nil, errors.New("invalid Tabby forwardedPorts type")
		}
		mapped := profile.ForwardSpec{Direction: direction, BindHost: f.Host, BindPort: f.Port}
		if direction != "dynamic" {
			if strings.TrimSpace(f.TargetAddress) == "" {
				return nil, errors.New("invalid Tabby forwardedPorts target")
			}
			mapped.Destination = net.JoinHostPort(f.TargetAddress, strconv.Itoa(f.TargetPort))
		}
		out = append(out, mapped)
	}
	if err := profile.ValidForwards(out); err != nil {
		return nil, errors.New("invalid Tabby forwardedPorts")
	}
	return out, nil
}
