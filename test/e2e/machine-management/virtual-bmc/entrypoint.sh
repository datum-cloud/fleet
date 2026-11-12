#!/usr/bin/env bash
set -euo pipefail

mkdir -p /run/libvirt /var/lib/libvirt/images /var/lib/sushy-emulator /evidence
# Docker/Lima exposes cgroup v2 to this privileged test appliance, but nested
# per-domain cgroup creation is not portable across runners. Libvirt still owns
# the QEMU process lifecycle; resource isolation is supplied by the container.
sed -i 's/^#cgroup_controllers = .*/cgroup_controllers = []/' /etc/libvirt/qemu.conf
# The persisted test disks are bind-mounted so diagnostics survive appliance
# recreation. macOS/virtiofs does not permit libvirt's DAC ownership rewrites;
# the appliance is already isolated and privileged, so keep QEMU root-owned and
# disable dynamic ownership inside this test-only container.
sed -i \
  -e 's/^#security_driver = .*/security_driver = "none"/' \
  -e 's/^#user = .*/user = "root"/' \
  -e 's/^#group = .*/group = "root"/' \
  -e 's/^#dynamic_ownership = .*/dynamic_ownership = 0/' \
  /etc/libvirt/qemu.conf
virtlogd >/evidence/virtlogd.log 2>&1 &
libvirtd >/evidence/libvirtd.log 2>&1 &
for _ in $(seq 1 60); do
  virsh -c qemu:///system list >/dev/null 2>&1 && break
  sleep 1
done
virsh -c qemu:///system list >/dev/null

domain_type=qemu
if [[ ${FLEET_FORCE_QEMU:-0} != 1 && -c /dev/kvm && -r /dev/kvm && -w /dev/kvm ]]; then
  domain_type=kvm
fi
echo "domain_type=${domain_type}" > /evidence/acceleration.txt

mkdir -p /var/lib/libvirt/dnsmasq/tftp/pxelinux.cfg /tmp/initramfs/{bin,dev,etc,proc,sys,lib}
cp /usr/lib/PXELINUX/pxelinux.0 /var/lib/libvirt/dnsmasq/tftp/
cp /usr/lib/syslinux/modules/bios/ldlinux.c32 /var/lib/libvirt/dnsmasq/tftp/
kernel_path=$(find /boot -maxdepth 1 -name 'vmlinuz-*' -type f | sort | tail -1)
kernel_version=${kernel_path##*/vmlinuz-}
cp "${kernel_path}" /var/lib/libvirt/dnsmasq/tftp/vmlinuz
e1000_module=$(modinfo -k "${kernel_version}" -F filename e1000)
case "${e1000_module}" in
  *.zst) zstd -q -d -c "${e1000_module}" > /tmp/initramfs/lib/e1000.ko ;;
  *.ko) cp "${e1000_module}" /tmp/initramfs/lib/e1000.ko ;;
  *) echo "unsupported e1000 module format: ${e1000_module}" >&2; exit 1 ;;
esac
cp /bin/busybox /tmp/initramfs/bin/busybox
for app in sh mount ip udhcpc wget poweroff sleep insmod; do ln -sf busybox "/tmp/initramfs/bin/${app}"; done
cat >/tmp/initramfs/bin/udhcpc-script <<'DHCP'
#!/bin/sh
case "$1" in
  bound|renew)
    ip addr flush dev "$interface"
    ip addr add "$ip/24" dev "$interface"
    ip link set "$interface" up
    ;;
esac
DHCP
chmod +x /tmp/initramfs/bin/udhcpc-script
cat >/tmp/initramfs/init <<'INIT'
#!/bin/sh
mount -t proc proc /proc
mount -t sysfs sysfs /sys
mount -t devtmpfs devtmpfs /dev
insmod /lib/e1000.ko
ip link set eth0 up
udhcpc -i eth0 -q -n -s /bin/udhcpc-script
mac=$(cat /sys/class/net/eth0/address | tr ':' '-')
wget -q -O /dev/null "http://192.168.124.1:8080/pxe/${mac}"
sleep 2
poweroff -f
INIT
chmod +x /tmp/initramfs/init
(cd /tmp/initramfs && find . -print0 | cpio --null -o --format=newc | gzip -9) >/var/lib/libvirt/dnsmasq/tftp/initramfs.gz
cat >/var/lib/libvirt/dnsmasq/tftp/pxelinux.cfg/default <<'PXE'
DEFAULT fleet
PROMPT 0
TIMEOUT 1
LABEL fleet
  KERNEL vmlinuz
  INITRD initramfs.gz
  APPEND console=ttyS0 ip=dhcp
PXE

cat >/tmp/provisioning.xml <<'XML'
<network>
  <name>fleet-provisioning</name>
  <bridge name='virbr-pxe'/>
  <forward mode='nat'/>
  <ip address='192.168.124.1' netmask='255.255.255.0'>
    <tftp root='/var/lib/libvirt/dnsmasq/tftp'/>
    <dhcp>
      <range start='192.168.124.100' end='192.168.124.200'/>
      <bootp file='http://192.168.124.1:8080/boot.ipxe'/>
    </dhcp>
  </ip>
</network>
XML
virsh net-define /tmp/provisioning.xml >/dev/null 2>&1 || true
virsh net-start fleet-provisioning >/dev/null 2>&1 || true

define_domain() {
  local name=$1 mac=$2
  if [[ ! -f "/var/lib/libvirt/images/${name}.qcow2" ]]; then
    qemu-img create -f qcow2 "/var/lib/libvirt/images/${name}.qcow2" 128M >/dev/null
  fi
  cat >"/tmp/${name}.xml" <<XML
<domain type='${domain_type}'>
  <name>${name}</name>
  <memory unit='MiB'>256</memory>
  <vcpu>1</vcpu>
  <os><type arch='x86_64' machine='pc'>hvm</type><boot dev='hd'/></os>
  <features><acpi/><apic/></features>
  <devices>
    <disk type='file' device='disk'><driver name='qemu' type='qcow2'/><source file='/var/lib/libvirt/images/${name}.qcow2'/><target dev='vda' bus='virtio'/></disk>
    <interface type='network'><mac address='${mac}'/><source network='fleet-provisioning'/><model type='e1000'/></interface>
    <serial type='file'><source path='/evidence/${name}-console.log'/><target port='0'/></serial>
    <console type='file'><source path='/evidence/${name}-console.log'/><target type='serial' port='0'/></console>
  </devices>
</domain>
XML
  virsh define "/tmp/${name}.xml" >/dev/null
}

define_domain allocated 52:54:00:aa:00:01
define_domain unallocated 52:54:00:aa:00:02

/usr/local/bin/pxe-callback &
# The optional qualification profile moves gateway traffic onto a real tun
# interface. The default profile admits only the attached location Kind node.
if [[ ${FLEET_OPENVPN:-0} == 1 ]]; then
  if [[ ! -f /evidence/openvpn.key ]]; then
    openvpn --genkey secret /evidence/openvpn.key
    chmod 0600 /evidence/openvpn.key
  fi
  openvpn --daemon --dev tun --ifconfig 10.88.0.1 10.88.0.2 \
    --secret /evidence/openvpn.key --proto udp --local 172.30.0.10 --port 1194 \
    --cipher AES-256-CBC --auth SHA256
  iptables -I INPUT -p tcp --dport 8000 -s 10.88.0.2 -j ACCEPT
else
  iptables -I INPUT -p tcp --dport 8000 -s 172.30.0.20 -j ACCEPT
fi
iptables -A INPUT -p tcp --dport 8000 -j DROP

exec /opt/sushy/bin/sushy-emulator --config /etc/sushy-emulator.conf
