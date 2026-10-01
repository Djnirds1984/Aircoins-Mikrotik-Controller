---
kind: external_dependency
name: NodeMCU coin acceptor — hardware pulse reporter
slug: nodemcu-coin-slot
category: external_dependency
category_hints:
    - vendor_identity
    - client_constraint
scope:
    - '**'
source_files:
    - hardware/nodemcu_coin_slot/nodemcu_coin_slot.ino
    - hardware/nodemcu_coin_slot/README.md
---

### NodeMCU / ESP8266/ESP32 coin acceptor
- Optional peripheral that counts pulses from a multi-coin acceptor and HTTPS-POSTs them to the controller's `/api/coin-pulse` endpoint. The phone never talks to the hardware; it polls `/api/coin-status` on the controller instead.
- Authentication is a shared secret `COIN_NODE_TOKEN` (generated with `openssl rand -hex 24`); without it every report is rejected, preventing guest-network minting of free time.
- Each POST carries a monotonic `event_id` so retries are deduplicated; pending pulses are journalled to flash before being sent, surviving power cuts.
- The sketch runs on either ESP8266 (NodeMCU) or ESP32; wiring uses an interrupt pin with a 10 kOhm pull-up (or a 1k/2.2k divider for 5 V open-collector acceptors).