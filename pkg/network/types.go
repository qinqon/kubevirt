package network

import "kubevirt.io/kubevirt/pkg/network/dhcp"

type Configuration map[string]InterfaceConfiguration

type InterfaceConfiguration struct {
	DHCPConfiguration *dhcp.Configuration
}
