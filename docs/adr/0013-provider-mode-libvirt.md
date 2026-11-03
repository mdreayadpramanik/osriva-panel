# ADR-0013: Provider Mode infrastructure and the libvirt/KVM adapter

Status: accepted (Phase 10a, 2026-11-03)

## Context

Phase 10 asks for Provider Mode:

* infrastructure inventory: data centers, racks, nodes and pools;
* virtualization adapters, Proxmox first;
* the VPS lifecycle.

Proxmox VE cannot run on a GitHub Actions runner, and no Proxmox test server is
available yet. libvirt/KVM runs on the Ubuntu runner (it exposes `/dev/kvm`), so a real
virtual machine can be created, booted, reached over SSH and deleted in CI. The owner
chose libvirt/KVM as the first adapter. Proxmox follows in 10b once a test server can
be provided.

A hypervisor is somebody's production host. Creating VMs must never change the host's
networking, storage layout or firewall, and must never touch virtual machines that
OSRIVA did not create.

## Decision

### Inventory (panel)

* **Data centers** (name, location) and **racks** (in a data center, height in units).
* **Nodes:** an enrolled server registered as a hypervisor. A node records:
  * its data center and, optionally, its rack;
  * the libvirt network that VMs join;
  * CPU and memory overcommit ratios: the defaults are 4:1 for vCPUs and 1:1 for memory.
* **IP pools:** an IPv4 CIDR, gateway, first and last address, and DNS servers.
  * A VPS gets the lowest free address, recorded in `ip_addresses`. The unique key
    (pool, address) makes concurrent allocations safe.
  * A pool with allocated addresses cannot be deleted.
* **Templates:** a cloud image given by an HTTPS URL plus its SHA-256, a minimum disk
  size and the image's default user.
* **Permissions:** `provider.read` (owner, admin, auditor) and `provider.write` (owner,
  admin).
* **Panel mode:** every change requires the panel to be in Hosting Provider Mode.
* **Capacity:** a VPS is refused when the node's vCPU or memory capacity is exhausted.
  Capacity is the inventory × the overcommit ratio, minus the existing VPSes. Disk space
  is checked by the agent when it creates the disk.

### VPS lifecycle (agent tasks `vm.*`)

* **Task types:**
  * `vm.host` (read): libvirt and KVM availability, networks, free space, CPUs and memory;
  * `vm.list` (read);
  * `vm.create`, `vm.action` (start, shutdown, reboot, stop) and `vm.delete` (operate,
    so `servers.operate` is also required);
  * `stop` (force off) and `vm.delete` need re-authentication. Deleting also needs the
    VPS name typed in the panel.
* **Tools:** the agent drives libvirt with `virsh -c qemu:///system`, `qemu-img` and an
  ISO tool (`genisoimage`, `mkisofs` or `xorriso`). It calls them with explicit
  arguments, never through a shell.
  * The agent installs nothing. Missing tools or a missing `/dev/kvm` are reported as
    the reason the host is unsupported.
  * Software emulation (no KVM) is refused rather than silently being very slow.
* **Images:** downloaded over HTTPS only, verified against the template's SHA-256 before
  use, and cached under `/var/lib/libvirt/images/osriva/base/<sha256>.img`.
* **Disks:** each VPS disk is a qcow2 overlay on the cached image, resized to the
  requested size, in `/var/lib/libvirt/images/osriva/`.
  * This is libvirt's own image location, which the AppArmor and dynamic-ownership
    defaults of libvirt cover. The agent's 0700 state directory is unreadable to QEMU.
  * The directories are mode 0711 and the disks 0600; libvirt hands each disk to the
    QEMU user while it runs.
* **cloud-init:** a NoCloud seed ISO (`cidata`) carries:
  * the hostname;
  * the SSH public keys (password login disabled);
  * a static network configuration with the allocated address, gateway and DNS.

  The configuration files are generated as JSON (valid YAML), so no value can inject
  YAML structure.

  The seed is attached as a read-only virtio disk, not as a SATA or IDE CD-ROM: minimal
  cloud images run a cut-down kernel (Ubuntu `linux-kvm`) without those drivers, and the
  first system test run found that cloud-init then never saw the seed.
* **Domains:**
  * Each domain is named `osriva-<vps id>` and carries an OSRIVA metadata element.
  * Actions and deletion refuse any domain without that metadata, so the agent never
    acts on domains it did not create.
  * The MAC address is derived from the VPS id.
  * Devices: virtio disk and NIC, serial console, `host-passthrough` CPU.
  * The serial console (boot and cloud-init output) is kept by virtlogd in
    `/var/log/libvirt/qemu/osriva-<id>-console.log` and removed with the VPS.
  * The node's existing libvirt network is used; the agent never creates or changes
    networks, bridges or firewall rules.
* **State:** each lifecycle task returns the domain state afterwards. The panel records
  the pending task on the VPS and applies the result when it reads the VPS. A failed
  create leaves the VPS in `error` with the agent's message, and deleting it cleans up.
  A successful delete releases the IP address.

## Limits

* **Hypervisors:** libvirt/KVM on Linux only. Proxmox VE (10b) and other hypervisors
  (OpenStack, CloudStack, vSphere) are not implemented.
* **Networking:** IPv4 only, one NIC, the node's existing libvirt network. No VLANs,
  bridges created by OSRIVA, or IPv6.
* **Operations not implemented:** resize, rebuild, snapshots, migration, console access
  and bandwidth limits. Usage is not accounted for, and there is deliberately no billing.
* **Node hardware:** the panel trusts the node's inventory report for capacity.
* **Tests:** the CI system test uses the libvirt `default` NAT network on the runner and
  an Ubuntu cloud image. Bridged networking on a real data-center host is NOT_TESTED.
