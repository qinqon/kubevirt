/*
 * This file is part of the KubeVirt project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright The KubeVirt Authors.
 *
 */

package netpod

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net"

	v1 "kubevirt.io/api/core/v1"

	"kubevirt.io/kubevirt/pkg/network/driver/nmstate"
	"kubevirt.io/kubevirt/pkg/network/link"
	"kubevirt.io/kubevirt/pkg/network/vmispec"
)

// isIPAMlessManagedTap selects fresh secondary Multus attachments without
// configured IP addresses. IPv6 link-local autoconfiguration does not imply
// IPAM; these addresses remain on the original NIC. Existing renamed/dummy
// topologies continue to use their existing setup path.
func isIPAMlessManagedTap(network v1.Network, podIfaceName string, interfaces map[string]nmstate.Interface) bool {
	if !vmispec.IsSecondaryMultusNetwork(network) {
		return false
	}
	podIface, exists := interfaces[podIfaceName]
	if !exists || podIface.TypeName == nmstate.TypeDummy {
		return false
	}
	if _, renamed := interfaces[link.GenerateNewBridgedVmiInterfaceName(podIfaceName)]; renamed {
		return false
	}
	if len(podIface.IPv4.Address) != 0 {
		return false
	}
	for _, address := range podIface.IPv6.Address {
		ip := net.ParseIP(address.IP)
		if ip == nil || !ip.IsLinkLocalUnicast() {
			return false
		}
	}
	return true
}

func (n NetPod) ipamlessGuestMAC(iface v1.Interface, podIface nmstate.Interface) (net.HardwareAddr, error) {
	var statusMAC string
	if status := vmispec.LookupInterfaceStatusByName(n.vmiIfaceStatuses, iface.Name); status != nil {
		statusMAC = status.MAC
	}
	mac, err := resolveMacAddress(podIface.MacAddress, statusMAC, iface.MacAddress)
	if err != nil {
		return nil, err
	}
	if len(mac) != 6 || mac[0]&1 != 0 {
		return nil, fmt.Errorf("managedTap requires an Ethernet unicast guest MAC, got %s", mac)
	}
	return mac, nil
}

func (n NetPod) ipamlessManagedTapSpec(podIfaceName string, ifaceIndex int, podIface nmstate.Interface) ([]nmstate.Interface, error) {
	iface := n.vmiSpecIfaces[ifaceIndex]
	network := vmispec.LookupNetworkByName(n.vmiSpecNets, iface.Name)
	guestMAC, err := n.ipamlessGuestMAC(iface, podIface)
	if err != nil {
		return nil, err
	}
	podMAC, err := net.ParseMAC(podIface.MacAddress)
	if err != nil {
		return nil, err
	}
	// An explicitly assigned bridge MAC prevents Linux from adopting a port's
	// MAC when it is attached. Keep it stable and distinct from both identities.
	hash := sha256.Sum256([]byte(n.vmiUID + "/" + iface.Name))
	bridgeMAC := net.HardwareAddr(hash[:6])
	bridgeMAC[0] = (bridgeMAC[0] | 2) & 0xfe
	for bytes.Equal(bridgeMAC, podMAC) || bytes.Equal(bridgeMAC, guestMAC) {
		bridgeMAC[5]++
	}
	bridgeName := link.GenerateBridgeName(podIfaceName)
	// Only owned devices enter nmstate's interface configuration path. The CNI
	// NIC is attached separately without renaming it or rewriting its addresses.
	return []nmstate.Interface{
		{
			Name: bridgeName, TypeName: nmstate.TypeBridge, State: nmstate.IfaceStateUp,
			MacAddress: bridgeMAC.String(), MTU: podIface.MTU,
			IPv4: nmstate.IP{Enabled: new(false)}, IPv6: nmstate.IP{Enabled: new(false)},
			Metadata: &nmstate.IfaceMetadata{NetworkName: iface.Name},
		},
		{
			Name:     link.GenerateTapDeviceName(podIfaceName, *network),
			TypeName: nmstate.TypeTap, State: nmstate.IfaceStateUp,
			MTU: podIface.MTU, Controller: bridgeName,
			Tap:      &nmstate.TapDevice{Queues: n.networkQueues(ifaceIndex), UID: n.ownerID, GID: n.ownerID},
			Metadata: &nmstate.IfaceMetadata{Pid: n.podPID, NetworkName: iface.Name},
		},
	}, nil
}

func (n NetPod) setupIPAMlessManagedTap(status *nmstate.Status) error {
	interfaces := ifaceStatusByName(status.Interfaces)
	names := createNetworkNameScheme(n.vmiSpecNets, n.vmiIfaceStatuses, status.Interfaces)
	for _, iface := range n.vmiSpecIfaces {
		if iface.Binding == nil || n.bindingPluginsByName[iface.Binding.Name].DomainAttachmentType != v1.ManagedTap {
			continue
		}
		network := vmispec.LookupNetworkByName(n.vmiSpecNets, iface.Name)
		podIfaceName := names[iface.Name]
		if !isIPAMlessManagedTap(*network, podIfaceName, interfaces) {
			continue
		}
		guestMAC, err := n.ipamlessGuestMAC(iface, interfaces[podIfaceName])
		if err != nil {
			return err
		}
		bridgeName := link.GenerateBridgeName(podIfaceName)
		tapName := link.GenerateTapDeviceName(podIfaceName, *network)
		if err := n.bridgeAdapter.BridgeConnect(podIfaceName, bridgeName, tapName, guestMAC); err != nil {
			return fmt.Errorf("managedTap bridge setup for %s: %w", iface.Name, err)
		}
	}
	return nil
}
