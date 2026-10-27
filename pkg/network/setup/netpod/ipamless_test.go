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
	"errors"
	"net"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "kubevirt.io/api/core/v1"

	dutils "kubevirt.io/kubevirt/pkg/ephemeral-disk-utils"
	"kubevirt.io/kubevirt/pkg/network/cache"
	"kubevirt.io/kubevirt/pkg/network/driver/nmstate"
	neterrors "kubevirt.io/kubevirt/pkg/network/errors"
	"kubevirt.io/kubevirt/pkg/network/setup/netpod"
)

var _ = Describe("IPAM-less managedTap", func() {
	const podMAC = "02:00:00:00:00:10"
	var (
		network        v1.Network
		iface          v1.Interface
		statuses       []v1.VirtualMachineInstanceNetworkInterface
		attachmentType v1.DomainAttachmentType
		nm             *ipamlessNMStateStub
		bridge         *ipamlessBridgeStub
		state          *netpod.State
		cacheCreator   tempCacheCreator
	)
	BeforeEach(dutils.MockDefaultOwnershipManager)
	BeforeEach(func() {
		DeferCleanup(os.Setenv, "MY_POD_IP", os.Getenv("MY_POD_IP"))
		Expect(os.Setenv("MY_POD_IP", "10.10.10.10")).To(Succeed())
		network = v1.Network{Name: "secondary", NetworkSource: v1.NetworkSource{Multus: &v1.MultusNetwork{NetworkName: "blue"}}}
		iface = v1.Interface{Name: network.Name, Binding: &v1.PluginBinding{Name: "l2bridge"}}
		statuses = nil
		attachmentType = v1.ManagedTap
		nm = &ipamlessNMStateStub{status: nmstate.Status{Interfaces: []nmstate.Interface{{
			Name: "net1", Index: 11, TypeName: nmstate.TypeVETH, State: nmstate.IfaceStateUp,
			MacAddress: podMAC, MTU: 1400,
		}}}}
		bridge = &ipamlessBridgeStub{}
		state = netpod.NewState(newConfigStateCacheStub(), netnsStub{})
		cacheCreator = tempCacheCreator{}
		DeferCleanup(func() { Expect(cacheCreator.New("").Delete()).To(Succeed()) })
	})
	newPod := func() netpod.NetPod {
		return netpod.NewNetPod([]v1.Network{network}, []v1.Interface{iface}, "ipamless-vmi", 123, 107, 4, state,
			netpod.WithCacheCreator(&cacheCreator), netpod.WithNMStateAdapter(nm), netpod.WithBridgeAdapter(bridge),
			netpod.WithVMIIfaceStatuses(statuses),
			netpod.WithBindingPlugins(map[string]v1.InterfaceBindingPlugin{"l2bridge": {DomainAttachmentType: attachmentType}}))
	}

	It("creates only owned devices and connects the unchanged NIC after applying them", func() {
		original := nm.status.Interfaces[0]
		bridge.onSetup = func() {
			Expect(nm.applies).To(Equal(1))
			_, started, finished, err := state.PendingStartedFinished([]v1.Network{network})
			Expect(err).NotTo(HaveOccurred())
			Expect(started).To(Equal([]v1.Network{network}))
			Expect(finished).To(BeEmpty())
		}
		pod := newPod()
		Expect(pod.Setup()).To(Succeed())
		Expect(nm.spec.Interfaces).To(HaveLen(2))
		Expect(nm.spec.Interfaces[0].TypeName).To(Equal(nmstate.TypeBridge))
		Expect(nm.spec.Interfaces[0].Name).To(Equal("k6t-net1"))
		Expect(nm.spec.Interfaces[0].MTU).To(Equal(1400))
		mac, err := net.ParseMAC(nm.spec.Interfaces[0].MacAddress)
		Expect(err).NotTo(HaveOccurred())
		Expect(mac[0] & 3).To(Equal(byte(2)))
		Expect(mac.String()).NotTo(Equal(podMAC))
		Expect(nm.spec.Interfaces[1].Name).To(Equal("tap1"))
		Expect(nm.spec.Interfaces[1].Controller).To(Equal("k6t-net1"))
		Expect(nm.spec.Interfaces[1].Tap).To(Equal(&nmstate.TapDevice{Queues: 4, UID: 107, GID: 107}))
		Expect(nm.spec.Interfaces[1].Metadata.Pid).To(Equal(123))
		Expect(nm.status.Interfaces[0]).To(Equal(original))
		Expect(bridge.podName).To(Equal("net1"))
		Expect(bridge.bridgeName).To(Equal("k6t-net1"))
		Expect(bridge.tapName).To(Equal("tap1"))
		Expect(bridge.mac.String()).To(Equal(podMAC))
		_, started, finished, err := state.PendingStartedFinished([]v1.Network{network})
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(BeEmpty())
		Expect(finished).To(Equal([]v1.Network{network}))
	})

	It("allows IPv6 link-local addresses without passing the original NIC through address configuration", func() {
		nm.status.Interfaces[0].IPv6 = nmstate.IP{Enabled: new(true), Address: []nmstate.IPAddress{{IP: "fe80::10", PrefixLen: 64}}}
		pod := newPod()
		Expect(pod.Setup()).To(Succeed())
		Expect(bridge.calls).To(Equal(1))
		for _, desired := range nm.spec.Interfaces {
			Expect(desired.Name).NotTo(Equal("net1"))
			Expect(desired.TypeName).NotTo(Equal(nmstate.TypeDummy))
		}
		Expect(nm.status.Interfaces[0].IPv6.Address).To(Equal([]nmstate.IPAddress{{IP: "fe80::10", PrefixLen: 64}}))
	})

	DescribeTable("uses the intended guest MAC for forwarding", func(specMAC, statusMAC, expected string) {
		iface.MacAddress = specMAC
		statuses = []v1.VirtualMachineInstanceNetworkInterface{{Name: iface.Name, MAC: statusMAC}}
		pod := newPod()
		Expect(pod.Setup()).To(Succeed())
		Expect(bridge.mac.String()).To(Equal(expected))
		Expect(nm.spec.Interfaces[0].MacAddress).NotTo(BeElementOf(podMAC, expected))
	},
		Entry("inherited from CNI", "", "", podMAC),
		Entry("preserved from source VMI status", "", "02:00:00:00:00:20", "02:00:00:00:00:20"),
		Entry("explicit VMI MAC takes priority", "02:00:00:00:00:30", "02:00:00:00:00:20", "02:00:00:00:00:30"),
	)

	DescribeTable("keeps the existing path for configured IP addresses", func(ipv4, ipv6 string) {
		if ipv4 != "" {
			nm.status.Interfaces[0].IPv4 = nmstate.IP{Enabled: new(true), Address: []nmstate.IPAddress{{IP: ipv4, PrefixLen: 24}}}
		}
		if ipv6 != "" {
			nm.status.Interfaces[0].IPv6 = nmstate.IP{Enabled: new(true), Address: []nmstate.IPAddress{{IP: ipv6, PrefixLen: 64}}}
		}
		pod := newPod()
		Expect(pod.Setup()).To(Succeed())
		Expect(bridge.calls).To(BeZero())
		Expect(nm.spec.Interfaces).To(HaveLen(4))
		Expect(nm.spec.Interfaces[1].Name).To(Equal("net1-nic"))
		Expect(nm.spec.Interfaces[3].TypeName).To(Equal(nmstate.TypeDummy))
	},
		Entry("IPv4", "192.0.2.10", ""),
		Entry("IPv4 link-local", "169.254.1.10", ""),
		Entry("IPv6", "", "2001:db8::10"),
		Entry("dual-stack", "192.0.2.10", "2001:db8::10"),
	)

	DescribeTable("keeps primary networks on the existing path", func(multusDefault bool) {
		if multusDefault {
			network.Multus.Default = true
		} else {
			network.NetworkSource = v1.NetworkSource{Pod: &v1.PodNetwork{}}
		}
		nm.status.Interfaces[0].Name = "eth0"
		pod := newPod()
		Expect(pod.Setup()).To(Succeed())
		Expect(bridge.calls).To(BeZero())
		Expect(nm.spec.Interfaces).To(HaveLen(4))
	}, Entry("pod network", false), Entry("default Multus network", true))

	It("does not convert a previously prepared renamed/dummy topology", func() {
		nm.status.Interfaces[0].TypeName = nmstate.TypeDummy
		nm.status.Interfaces = append(nm.status.Interfaces, nmstate.Interface{
			Name: "net1-nic", TypeName: nmstate.TypeVETH, MacAddress: "02:00:00:00:00:40",
		})
		pod := newPod()
		Expect(pod.Setup()).To(Succeed())
		Expect(bridge.calls).To(BeZero())
		Expect(nm.spec.Interfaces).To(HaveLen(4))
	})

	It("skips completed attachments on subsequent setup calls", func() {
		pod := newPod()
		Expect(pod.Setup()).To(Succeed())
		Expect(pod.Setup()).To(Succeed())
		Expect(nm.reads).To(Equal(1))
		Expect(nm.applies).To(Equal(1))
		Expect(bridge.calls).To(Equal(1))
	})

	It("skips an attachment recovered from finished configuration state", func() {
		cachedState := newConfigStateCacheStub()
		Expect(cachedState.Write(network.Name, cache.PodIfaceNetworkPreparationFinished)).To(Succeed())
		state = netpod.NewState(cachedState, netnsStub{})
		pod := newPod()
		Expect(pod.Setup()).To(Succeed())
		Expect(nm.reads).To(BeZero())
		Expect(nm.applies).To(BeZero())
		Expect(bridge.calls).To(BeZero())
	})

	It("does not configure the bridge for a plain tap attachment", func() {
		attachmentType = v1.Tap
		pod := newPod()
		Expect(pod.Setup()).To(Succeed())
		Expect(nm.spec.Interfaces).To(BeEmpty())
		Expect(bridge.calls).To(BeZero())
	})

	It("does not connect the NIC when device preparation fails", func() {
		nm.applyErr = errors.New("device preparation failed")
		pod := newPod()
		Expect(pod.Setup()).To(MatchError(nm.applyErr))
		Expect(bridge.calls).To(BeZero())
		_, started, finished, err := state.PendingStartedFinished([]v1.Network{network})
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(HaveLen(1))
		Expect(finished).To(BeEmpty())
	})

	It("leaves a failed FDB setup started and preserves the existing non-retry lifecycle", func() {
		bridge.err = errors.New("FDB unavailable")
		pod := newPod()
		err := pod.Setup()
		Expect(err).To(MatchError(ContainSubstring("FDB unavailable")))
		var critical *neterrors.CriticalNetworkError
		Expect(errors.As(err, &critical)).To(BeTrue())
		_, started, finished, err := state.PendingStartedFinished([]v1.Network{network})
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(HaveLen(1))
		Expect(finished).To(BeEmpty())
		Expect(pod.Setup()).To(MatchError(ContainSubstring("cannot be restarted")))
		Expect(bridge.calls).To(Equal(1))
	})

	It("prepares a fresh target using a stable bridge MAC and the source guest MAC", func() {
		statuses = []v1.VirtualMachineInstanceNetworkInterface{{Name: iface.Name, MAC: "02:00:00:00:00:20"}}
		pod := newPod()
		Expect(pod.Setup()).To(Succeed())
		bridgeMAC := nm.spec.Interfaces[0].MacAddress
		state = netpod.NewState(newConfigStateCacheStub(), netnsStub{})
		pod = newPod()
		Expect(pod.Setup()).To(Succeed())
		Expect(bridge.calls).To(Equal(2))
		Expect(nm.spec.Interfaces[0].MacAddress).To(Equal(bridgeMAC))
		Expect(bridge.mac.String()).To(Equal(statuses[0].MAC))
	})
})

type ipamlessNMStateStub struct {
	status         nmstate.Status
	spec           *nmstate.Spec
	reads, applies int
	applyErr       error
}

func (s *ipamlessNMStateStub) Read() (*nmstate.Status, error) { s.reads++; return &s.status, nil }
func (s *ipamlessNMStateStub) Apply(spec *nmstate.Spec) error {
	s.applies++
	s.spec = spec
	return s.applyErr
}

type ipamlessBridgeStub struct {
	podName, bridgeName, tapName string
	mac                          net.HardwareAddr
	calls                        int
	err                          error
	onSetup                      func()
}

func (b *ipamlessBridgeStub) BridgeConnect(pod, bridge, tap string, mac net.HardwareAddr) error {
	b.calls++
	b.podName, b.bridgeName, b.tapName, b.mac = pod, bridge, tap, mac
	if b.onSetup != nil {
		b.onSetup()
	}
	return b.err
}
