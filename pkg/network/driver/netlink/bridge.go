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

package netlink

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// BridgeConnect attaches an existing CNI NIC without changing its identity or
// addresses, then directs the guest's MAC to an already-prepared TAP. This runs
// in the pod network namespace as part of the one-time network preparation.
func (n NetLink) BridgeConnect(podIfaceName, bridgeName, tapName string, guestMAC net.HardwareAddr) error {
	podLink, err := n.LinkByName(podIfaceName)
	if err != nil {
		return fmt.Errorf("find pod interface %s: %w", podIfaceName, err)
	}
	bridgeLink, err := n.LinkByName(bridgeName)
	if err != nil {
		return fmt.Errorf("find bridge %s: %w", bridgeName, err)
	}
	bridge, ok := bridgeLink.(*netlink.Bridge)
	if !ok {
		return fmt.Errorf("%s is not a Linux bridge", bridgeName)
	}
	tap, err := n.LinkByName(tapName)
	if err != nil {
		return fmt.Errorf("find TAP %s: %w", tapName, err)
	}
	if tap.Attrs().MasterIndex != bridge.Attrs().Index {
		return fmt.Errorf("TAP %s is not attached to bridge %s", tapName, bridgeName)
	}
	if podLink.Attrs().MasterIndex != bridge.Attrs().Index {
		if masterErr := n.LinkSetMaster(podLink, bridge); masterErr != nil {
			return masterErr
		}
	}
	if learnErr := n.LinkSetLearningOff(podLink); learnErr != nil {
		return learnErr
	}
	if podLink.Attrs().Flags&net.FlagUp == 0 {
		if upErr := n.LinkSetUp(podLink); upErr != nil {
			return upErr
		}
	}
	// NUD_NOARP is a static forwarding entry. NUD_PERMANENT would instead
	// recreate a local entry and consume guest-bound unicast in the host stack.
	// Omitting a VLAN attribute covers the untagged/default-VLAN entries, like
	// `bridge fdb replace <mac> dev <tap> master static`.
	err = netlink.NeighSet(&netlink.Neigh{
		LinkIndex:    tap.Attrs().Index,
		Family:       unix.AF_BRIDGE,
		State:        netlink.NUD_NOARP,
		Flags:        netlink.NTF_MASTER,
		HardwareAddr: guestMAC,
	})
	return withErrDescr(err, "set guest forwarding entry")
}
