package dhcp

import (
	"fmt"

	v1 "kubevirt.io/client-go/api/v1"
	"kubevirt.io/client-go/log"
	"kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap/converter"
	dhcpv4 "kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap/network/dhcp"
	"kubevirt.io/kubevirt/pkg/virt-launcher/virtwrap/network/dhcpv6"
)

// Allow mocking for tests
var DHCPServer = dhcpv4.SingleClientDHCPServer
var DHCPv6Server = dhcpv6.SingleClientDHCPv6Server

type HandlerImpl struct{}

func (h *HandlerImpl) DisableTXOffloadChecksum(ifaceName string) error {
	if err := dhcpv4.EthtoolTXOff(ifaceName); err != nil {
		log.Log.Reason(err).Errorf("Failed to set tx offload for interface %s off", ifaceName)
		return err
	}
	return nil
}

func (h *HandlerImpl) StartDHCP(nic *Configuration, bridgeInterfaceName string, dhcpOptions *v1.DHCPOptions, filterByMAC bool) error {
	log.Log.V(4).Infof("StartDHCP network Nic: %+v", nic)
	nameservers, searchDomains, err := converter.GetResolvConfDetailsFromPod()
	if err != nil {
		return fmt.Errorf("Failed to get DNS servers from resolv.conf: %v", err)
	}

	// panic in case the DHCP server failed during the vm creation
	// but ignore dhcp errors when the vm is destroyed or shutting down
	go func() {
		if err = DHCPServer(
			nic.MAC,
			filterByMAC,
			nic.IP.IP,
			nic.IP.Mask,
			bridgeInterfaceName,
			nic.AdvertisingIPAddr,
			nic.Gateway,
			nameservers,
			nic.Routes,
			searchDomains,
			nic.Mtu,
			dhcpOptions,
		); err != nil {
			log.Log.Errorf("failed to run DHCP: %v", err)
			panic(err)
		}
	}()

	if nic.IPv6.IPNet != nil {
		go func() {
			if err = DHCPv6Server(
				nic.IPv6.IP,
				bridgeInterfaceName,
			); err != nil {
				log.Log.Reason(err).Error("failed to run DHCPv6")
				panic(err)
			}
		}()
	}

	return nil
}
