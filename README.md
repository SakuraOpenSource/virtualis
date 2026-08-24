# Virtualis

Virtualis is a single-admin master for remote VM and container agents.

- Instances are created on a selected agent, never on the master host.
- Agents report installed drivers and execute lifecycle operations locally.
- QEMU instances expose VNC through the master's noVNC WebSocket proxy.
- Metrics and network inspection are collected from the assigned agent.
- Instance networking supports NAT, bridge, MAC, IPv4, gateway, DNS, and bandwidth settings where the selected driver supports them.

If the administrator password is forgotten, stop the service and run:

```bash
/opt/virtualis/virtualis -data /var/lib/virtualis --reset-password
```
