# IPAM-less managedTap

> PoC / proof of concept, not merged.
>
> Companion change:
> [multi-networkpolicy-nftables bridge forwarding](https://github.com/qinqon/multi-networkpolicy-nftables/commit/0094ede5e57f1fe8f6f97307a663c1b99b87d283)

## Problem

Secondary Multus networks may be attached to VMs without IPAM: the CNI assigns
no IP address and the guest is expected to obtain its configuration from the
customer's DHCP server. Such an attachment cannot use the existing managedTap
implementation, because it renames the CNI interface and replaces it with a
dummy device that carries the pod IP.

Renaming changes the interface identity: the original name, ifindex and the
CNI-assigned MAC visible to the network are no longer tied to the interface the
guest uses, and consumers that refer to the Multus-reported interface name
(for example MultiNetworkPolicy) no longer see the traffic path they expect.

## Topology

All devices live in the virt-launcher pod network namespace:

1. The CNI interface is kept as is: same name, ifindex, MAC, MTU and addresses.
   No dummy device and no rename.
2. A Linux bridge named `k6t-<pod interface>` is created, with an explicitly
   assigned locally administered MAC derived from the VMI UID and the network
   name (stable across migration target preparation).
3. The binding TAP keeps the managedTap naming and is enslaved to the bridge.
4. The CNI interface is enslaved to the bridge with learning disabled.
5. A static forwarding entry directs the guest MAC to the TAP:

   ```
   bridge fdb replace <guest MAC> dev <tap> master static
   ```

   In netlink terms: `AF_BRIDGE` family, `NUD_NOARP` state, `NTF_MASTER` flag,
   create-or-replace.

The same TAP and domain attachment are used as for managedTap, so libvirt
wiring and the guest interface are unchanged.

### Why an explicit bridge MAC

Without an explicit address, a Linux bridge adopts the MAC of its first port.
Here that is the CNI interface, whose MAC the guest inherits. The bridge's own
address would then equal the guest MAC and the host stack would consume frames
addressed to the guest. Broadcast DHCP still works in that state, so a
broadcast-only DORA check does not expose the problem; unicast DHCP offers and
renewals as well as TCP fail.

An explicitly assigned (and distinct) bridge MAC keeps the bridge address away
from the guest identity. Once set, the kernel does not re-adopt a port MAC when
ports are attached later.

### Why the static forwarding entry

Even with a distinct bridge MAC, adding the CNI interface as a bridge port
installs a local forwarding entry for the port MAC, which is the guest MAC.
The static entry on the TAP supersedes it for forwarding lookups. Ordering is
therefore significant: the entry must be installed after all interface, MAC and
bridge configuration, and before the attachment is marked finished.

The entry is idempotently re-applied on every network setup pass that has work
to do (for example when another network is hotplugged), but there is no
background reconciliation loop.

## Detection rule

The new path is selected only when, for a secondary Multus network:

- the reported pod interface exists, is not a dummy device, and the
  `<pod interface>-nic` interface created by the existing managedTap flow does
  not exist (a fresh attachment);
- it has no IPv4 address, and no IPv6 address other than link-local. An IPv6
  link-local address is auto-configured by the kernel whenever IPv6 is enabled
  and does not imply IPAM.

Every other case keeps the existing managedTap behavior (rename and dummy
device): primary networks, attachments with IPAM, attachments that carry a
global unicast address, and attachments prepared by an older version.

## Guest MAC

The forwarding entry must use the MAC the guest actually uses:

1. an explicit VMI spec MAC, otherwise
2. the VMI status MAC (migration target: the migrated domain keeps the source
   MAC), otherwise
3. the CNI interface MAC (normal start; the guest inherits it).

This mirrors the domain spec generator fallback (spec MAC, otherwise the pod
interface MAC) and the MAC resolution already used by the bridge binding.

## Lifecycle

- Setup is state driven and runs once per attachment inside the existing
  `Pending -> Started -> Finished` flow.
- The forwarding entry is installed after the nmstate desired state is applied
  and before the attachment is marked finished. A failure leaves the attachment
  started, which turns a restart into a critical error (existing behavior; no
  repair loop).
- The entry survives interface link flaps, but not an out of band detach and
  re-attach of the CNI interface, or writing the same MAC to it. KubeVirt does
  not perform such operations during the attachment lifetime.
- A migration target prepares a fresh pod with deterministic bridge and TAP
  names, the same bridge MAC (derived from the VMI UID) and the source guest
  MAC (from the VMI status), so forwarding is restored there.

## MultiNetworkPolicy

The CNI interface keeps its Multus-reported name and, together with the TAP, is
a real bridge port. A bridge family `FORWARD` hook therefore observes the same
interface names that policies reference; policy objects need no changes. The
companion change adds native bridge forwarding enforcement to the
multi-networkpolicy nftables backend, without any KubeVirt specific detection.

## Limitations

- The pod side keeps the CNI interface addresses (including IPv6 link-local)
  only to keep the CNI and the pod sandbox happy; the pod does not serve DHCP
  or DNS to the guest. The guest must obtain its configuration externally,
  which is the purpose of this mode.
- Only fresh attachments take this path; existing attachments are not migrated
  to the new topology.
- VLAN filtering on the bridge is not configured; the topology covers the
  untagged/default VLAN.
- Like managedTap, there is no continuous repair loop for out of band changes.
