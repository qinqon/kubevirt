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

package netpod_test

import (
	"flag"
	"net"
	"os"
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	v1 "kubevirt.io/api/core/v1"

	dutils "kubevirt.io/kubevirt/pkg/ephemeral-disk-utils"
	"kubevirt.io/kubevirt/pkg/network/setup/netpod"
)

var runIPAMlessIntegration bool

func init() {
	flag.BoolVar(&runIPAMlessIntegration, "run-integration-tests", false, "run privileged network integration tests")
}

var _ = Describe("IPAM-less managedTap kernel integration", Label("integration"), func() {
	BeforeEach(dutils.MockDefaultOwnershipManager)
	BeforeEach(func() {
		if !runIPAMlessIntegration {
			Skip("integration tests are not set to run") //nolint:forbidigo
		}
	})

	It("attaches the unrenamed NIC and replaces its local MAC entry with a TAP forwarding entry", func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		originalNS, err := netns.Get()
		Expect(err).NotTo(HaveOccurred())
		defer originalNS.Close()
		testNS, err := netns.New()
		Expect(err).NotTo(HaveOccurred())
		defer testNS.Close()
		defer func() { Expect(netns.Set(originalNS)).To(Succeed()) }()

		mac, err := net.ParseMAC("02:00:00:00:00:10")
		Expect(err).NotTo(HaveOccurred())
		Expect(netlink.LinkAdd(&netlink.Veth{
			LinkAttrs: netlink.LinkAttrs{Name: "net1", MTU: 1400, HardwareAddr: mac}, PeerName: "peer0",
		})).To(Succeed())
		podLink, err := netlink.LinkByName("net1")
		Expect(err).NotTo(HaveOccurred())
		peer, err := netlink.LinkByName("peer0")
		Expect(err).NotTo(HaveOccurred())
		Expect(netlink.LinkSetUp(peer)).To(Succeed())
		Expect(netlink.LinkSetUp(podLink)).To(Succeed())
		addr, err := netlink.ParseAddr("fe80::10/64")
		Expect(err).NotTo(HaveOccurred())
		addr.Flags = unix.IFA_F_NODAD
		Expect(netlink.AddrAdd(podLink, addr)).To(Succeed())
		addressesBefore, err := netlink.AddrList(podLink, netlink.FAMILY_ALL)
		Expect(err).NotTo(HaveOccurred())

		// Use a pre-existing real TAP to exercise production nmstate and bridge
		// setup without requiring the virt-chroot binary/launcher SELinux label.
		fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
		Expect(err).NotTo(HaveOccurred())
		defer unix.Close(fd)
		ifr, err := unix.NewIfreq("tap1")
		Expect(err).NotTo(HaveOccurred())
		ifr.SetUint16(unix.IFF_TAP | unix.IFF_NO_PI | unix.IFF_VNET_HDR)
		Expect(unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr)).To(Succeed())

		cacheCreator := tempCacheCreator{}
		DeferCleanup(func() { Expect(cacheCreator.New("").Delete()).To(Succeed()) })
		networks := []v1.Network{{Name: "secondary", NetworkSource: v1.NetworkSource{Multus: &v1.MultusNetwork{NetworkName: "blue"}}}}
		state := netpod.NewState(newConfigStateCacheStub(), netnsStub{})
		pod := netpod.NewNetPod(networks, []v1.Interface{{Name: "secondary", Binding: &v1.PluginBinding{Name: "l2bridge"}}},
			"integration-vmi", os.Getpid(), 0, 1, state,
			netpod.WithCacheCreator(&cacheCreator),
			netpod.WithBindingPlugins(map[string]v1.InterfaceBindingPlugin{"l2bridge": {DomainAttachmentType: v1.ManagedTap}}))
		Expect(pod.Setup()).To(Succeed())
		actual, err := netlink.LinkByName("net1")
		Expect(err).NotTo(HaveOccurred())
		Expect(actual.Attrs().Index).To(Equal(podLink.Attrs().Index))
		Expect(actual.Attrs().HardwareAddr).To(Equal(mac))
		Expect(actual.Attrs().MTU).To(Equal(1400))
		addressesAfter, err := netlink.AddrList(actual, netlink.FAMILY_ALL)
		Expect(err).NotTo(HaveOccurred())
		var beforeIPs, afterIPs []string
		for _, a := range addressesBefore {
			beforeIPs = append(beforeIPs, a.IPNet.String())
		}
		for _, a := range addressesAfter {
			afterIPs = append(afterIPs, a.IPNet.String())
		}
		Expect(afterIPs).To(ConsistOf(beforeIPs))
		_, err = netlink.LinkByName("net1-nic")
		Expect(err).To(HaveOccurred())
		bridge, err := netlink.LinkByName("k6t-net1")
		Expect(err).NotTo(HaveOccurred())
		Expect(actual.Attrs().MasterIndex).To(Equal(bridge.Attrs().Index))
		Expect(bridge.Attrs().HardwareAddr).NotTo(Equal(mac))
		tap, err := netlink.LinkByName("tap1")
		Expect(err).NotTo(HaveOccurred())
		Expect(tap.Attrs().MasterIndex).To(Equal(bridge.Attrs().Index))
		entries, err := netlink.NeighList(tap.Attrs().Index, unix.AF_BRIDGE)
		Expect(err).NotTo(HaveOccurred())
		found := false
		for _, entry := range entries {
			if entry.HardwareAddr.String() == mac.String() && entry.Vlan == 0 {
				Expect(entry.State).To(Equal(netlink.NUD_NOARP))
				found = true
			}
		}
		Expect(found).To(BeTrue(), "expected a static, non-local guest forwarding entry on the TAP")
		_, started, finished, err := state.PendingStartedFinished(networks)
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(BeEmpty())
		Expect(finished).To(Equal(networks))
		Expect(pod.Setup()).To(Succeed())
	})
})
