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
 * Copyright 2021 Red Hat, Inc.
 *
 */

//go:generate mockgen -source $GOFILE -package=$GOPACKAGE -destination=generated_mock_$GOFILE

package dhcp

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
	v1 "kubevirt.io/client-go/api/v1"
)

type Handler interface {
	StartDHCP(nic *Configuration, bridgeInterfaceName string, dhcpOptions *v1.DHCPOptions, filterByMAC bool) error
}

type Configuration struct {
	Name                string
	IP                  netlink.Addr
	IPv6                netlink.Addr
	MAC                 net.HardwareAddr
	AdvertisingIPAddr   net.IP
	AdvertisingIPv6Addr net.IP
	Routes              *[]netlink.Route
	Mtu                 uint16
	IPAMDisabled        bool
	Gateway             net.IP
}

func (d Configuration) String() string {
	return fmt.Sprintf(
		"DHCP configuration: { Name: %s, IPv4: %s, IPv6: %s, MAC: %s, AdvertisingIPAddr: %s, MTU: %d, Gateway: %s, IPAMDisabled: %t}",
		d.Name,
		d.IP,
		d.IPv6,
		d.MAC,
		d.AdvertisingIPAddr,
		d.Mtu,
		d.Gateway,
		d.IPAMDisabled,
	)
}
