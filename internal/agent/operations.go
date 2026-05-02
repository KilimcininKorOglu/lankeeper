package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

func RegisterBuiltinOps(s *Server) {
	s.Register("ping", opPing)
	s.Register("pppoe.connect", opStub("pppoe.connect"))
	s.Register("pppoe.disconnect", opStub("pppoe.disconnect"))
	s.Register("pppoe.status", opStub("pppoe.status"))
	s.Register("pppoe.sniff.start", opStub("pppoe.sniff.start"))
	s.Register("pppoe.sniff.stop", opStub("pppoe.sniff.stop"))
	s.Register("network.vlan.create", opStub("network.vlan.create"))
	s.Register("network.vlan.delete", opStub("network.vlan.delete"))
	s.Register("usb.activate", opStub("usb.activate"))
	s.Register("usb.deactivate", opStub("usb.deactivate"))
	s.Register("usb.status", opStub("usb.status"))
	s.Register("healthcheck.restart_iface", opStub("healthcheck.restart_iface"))
	s.Register("healthcheck.restart_pppoe", opStub("healthcheck.restart_pppoe"))
	s.Register("system.reboot", opStub("system.reboot"))
}

func opPing(_ context.Context, _ json.RawMessage) (any, error) {
	return map[string]string{"status": "pong"}, nil
}

func opStub(name string) Handler {
	return func(_ context.Context, _ json.RawMessage) (any, error) {
		return nil, fmt.Errorf("%s: not yet implemented", name)
	}
}
