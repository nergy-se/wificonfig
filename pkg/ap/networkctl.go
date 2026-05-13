package ap

import (
	"encoding/json"
	"fmt"
	"net"

	"github.com/nergy-se/wificonfig/pkg/commands"
)

type NetworkctlOutput struct {
	Interfaces []NetworkctlInterface `json:"Interfaces"`
}

type NetworkctlInterface struct {
	Name      string              `json:"Name"`
	Type      string              `json:"Type"`
	Addresses []NetworkctlAddress `json:"Addresses"`
	Routes    []NetworkctlRoute   `json:"Routes"`
	DNS       []NetworkctlDNS     `json:"DNS"`
	NTP       []NetworkctlNTP     `json:"NTP"`
}

type NetworkctlAddress struct {
	Family       int   `json:"Family"`
	Address      []int `json:"Address"`
	PrefixLength int   `json:"PrefixLength"`
}

type NetworkctlRoute struct {
	Family                  int   `json:"Family"`
	DestinationPrefixLength int   `json:"DestinationPrefixLength"`
	Gateway                 []int `json:"Gateway"`
}

type NetworkctlDNS struct {
	Family  int   `json:"Family"`
	Address []int `json:"Address"`
}

type NetworkctlNTP struct {
	Server string `json:"Server"`
}

func (a *Ap) NetworkctlList() (*NetworkctlOutput, error) {
	out, err := commands.Run("networkctl", "--json=short")
	if err != nil {
		return nil, err
	}
	o := &NetworkctlOutput{}
	if err := json.Unmarshal([]byte(out), o); err != nil {
		return nil, err
	}
	return o, nil
}

func (a NetworkctlAddress) IPv4CIDR() string {
	if a.Family != 2 || len(a.Address) != 4 {
		return ""
	}
	return fmt.Sprintf("%s/%d", net.IPv4(byte(a.Address[0]), byte(a.Address[1]), byte(a.Address[2]), byte(a.Address[3])).String(), a.PrefixLength)
}

func (d NetworkctlDNS) IPv4String() string {
	if d.Family != 2 || len(d.Address) != 4 {
		return ""
	}
	return net.IPv4(byte(d.Address[0]), byte(d.Address[1]), byte(d.Address[2]), byte(d.Address[3])).String()
}

func (r NetworkctlRoute) GatewayIPv4String() string {
	if r.Family != 2 || len(r.Gateway) != 4 {
		return ""
	}
	return net.IPv4(byte(r.Gateway[0]), byte(r.Gateway[1]), byte(r.Gateway[2]), byte(r.Gateway[3])).String()
}
