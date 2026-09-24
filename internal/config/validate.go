package config

import (
	"fmt"
)

func (c *Config) Validate() []error {
	var errs []error
	errs = append(errs, c.validateSystem()...)
	errs = append(errs, c.validateInterfaces()...)
	errs = append(errs, c.validateModes()...)
	errs = append(errs, c.validateVLANs()...)
	return errs
}

func (c *Config) validateSystem() []error {
	var errs []error
	if c.System.Hostname == "" {
		errs = append(errs, fmt.Errorf("system.hostname is required"))
	}
	if c.System.WebPort < 1 || c.System.WebPort > 65535 {
		errs = append(errs, fmt.Errorf("system.webPort must be 1-65535"))
	}
	if c.System.Language != "" && c.System.Language != "tr" && c.System.Language != "en" {
		errs = append(errs, fmt.Errorf("system.language must be 'tr' or 'en'"))
	}
	validTLSModes := map[string]bool{"self-signed": true, "mkcert": true, "acme": true, "": true}
	if !validTLSModes[c.System.TLS.Mode] {
		errs = append(errs, fmt.Errorf("system.tls.mode must be self-signed, mkcert, or acme"))
	}
	return errs
}

func (c *Config) validateInterfaces() []error {
	var errs []error
	validRoles := map[string]bool{"wan": true, "lan": true, "unused": true}
	for i, iface := range c.Interfaces {
		if iface.ID == "" {
			errs = append(errs, fmt.Errorf("interfaces[%d].id is required", i))
		}
		if iface.Device == "" {
			errs = append(errs, fmt.Errorf("interfaces[%d].device is required", i))
		}
		if !validRoles[iface.Role] {
			errs = append(errs, fmt.Errorf("interfaces[%d].role must be wan, lan, or unused", i))
		}
	}
	return errs
}

// validateModes checks the PPPoE MTU and the enumerated IPv6 and QoS
// settings.
func (c *Config) validateModes() []error {
	var errs []error
	if c.PPPoE.MTU > 0 && (c.PPPoE.MTU < 68 || c.PPPoE.MTU > 1500) {
		errs = append(errs, fmt.Errorf("pppoe.mtu must be 68-1500"))
	}
	validIPv6 := map[string]bool{"auto": true, "on": true, "off": true, "": true}
	if !validIPv6[c.IPv6.Enabled] {
		errs = append(errs, fmt.Errorf("ipv6.enabled must be auto, on, or off"))
	}
	validQoS := map[string]bool{"cake": true, "fq_codel": true, "none": true, "": true}
	if !validQoS[c.QoS.Profile] {
		errs = append(errs, fmt.Errorf("qos.profile must be cake, fq_codel, or none"))
	}
	validCC := map[string]bool{"bbr": true, "cubic": true, "": true}
	if !validCC[c.QoS.CongestionControl] {
		errs = append(errs, fmt.Errorf("qos.congestionControl must be bbr or cubic"))
	}
	return errs
}

func (c *Config) validateVLANs() []error {
	var errs []error
	for i, vlan := range c.VLANs {
		if vlan.VID < 1 || vlan.VID > 4094 {
			errs = append(errs, fmt.Errorf("vlans[%d].vid must be 1-4094", i))
		}
		if vlan.Parent == "" {
			errs = append(errs, fmt.Errorf("vlans[%d].parent is required", i))
		}
	}
	return errs
}
