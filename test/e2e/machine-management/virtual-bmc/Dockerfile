FROM ubuntu:24.04@sha256:2198e4c65076e9cb0b3d695219df667c0e3b0072da64ca3c2648aca17dcecaf5
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    busybox-static cpio curl dnsmasq-base iptables ipxe libvirt-clients \
    libvirt-daemon-system linux-image-virtual nginx openvpn ovmf pxelinux python3 \
    python3-libvirt python3-venv qemu-system-x86 qemu-utils syslinux-common zstd && \
    rm -rf /var/lib/apt/lists/* && \
    python3 -m venv --system-site-packages /opt/sushy && \
    /opt/sushy/bin/pip install --no-cache-dir sushy-tools==2.2.0
COPY emulator.conf /etc/sushy-emulator.conf
COPY callback.py /usr/local/bin/pxe-callback
COPY entrypoint.sh /usr/local/bin/virtual-bmc-entrypoint
RUN chmod 0755 /usr/local/bin/virtual-bmc-entrypoint /usr/local/bin/pxe-callback
EXPOSE 8000
ENTRYPOINT ["/usr/local/bin/virtual-bmc-entrypoint"]
