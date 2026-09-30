# Piso Wi-Fi coin slot (NodeMCU / ESP32)

The coin acceptor on a piso Wi-Fi box. It counts the pulses the acceptor emits
and reports them to the Aircoins controller, which turns them into hotspot
access time for the customer standing at the machine.

```
nodemcu_coin_slot.ino    the sketch (ESP8266 and ESP32)
```

## How it fits together

```
  coin acceptor --signal--> NodeMCU --HTTPS POST--> Aircoins controller
                             (counts)                /api/coin-pulse
                                                       |
  customer's phone <--polls-- /api/coin-status <--------+
       |
       +-- "Done / Connect now" --> /portal/coin/connect --> MikroTik
```

The phone never talks to the hardware. It only asks the controller what has been
credited, which means the flow keeps working across a phone changing network,
the hotspot restarting, or the NodeMCU rebooting.

## Wiring

Most multi-coin acceptors (the common "pulse / signal" type) close a switch
contact for roughly 20-40 ms per pulse when they accept a coin. That is far too
short for a `loop()` poll, which is why the sketch uses `attachInterrupt`.

| NodeMCU (ESP8266) | ESP32 | Connects to |
| --- | --- | --- |
| `D5` / GPIO 14 | GPIO 27 (any free pin) | acceptor signal |
| `GND` | `GND` | acceptor GND |

**Fit a 10 kOhm pull-up between the signal pin and 3V3.** A coin acceptor is an
open-collector output; without a pull-up the pin floats, and a coin will
sometimes be counted twice or not at all.

> If your acceptor has a **5 V** open-collector output (common on the larger
> multi-coin units), use a voltage divider - 1 kOhm from the signal to the pin
> and 2.2 kOhm from the pin to GND. **Do not wire 5 V to the pin directly**; it
> will destroy the ESP.

The sketch already enables the internal pull-up (`INPUT_PULLUP`) as a secondary
safety net, but an external 10 kOhm is far more reliable next to a coin
mechanism.

## Setup

1. **Give the controller a token.** In `/etc/aircoins/aircoins.env`:

   ```sh
   COIN_NODE_TOKEN=$(openssl rand -hex 24)
   COIN_SECONDS_PER_PULSE=300
   COIN_IDLE_TTL=20m
   ```

   `COIN_SECONDS_PER_PULSE` is the important one: it is how much access time one
   pulse buys. The usual acceptor emits one pulse per ~25 seconds of time for a
   5-peso coin, so `300` is a good starting point. Adjust after watching a few
   real coins through the portal.

   Restart the service (`systemctl restart aircoins`).

2. **Edit the sketch** at the top of the file:

   | Constant | Set it to |
   | --- | --- |
   | `COIN_NODE_TOKEN` | the same value as the controller's |
   | `CONTROLLER_HOST` | the controller's IP, e.g. `192.168.88.1` |
   | `WIFI_SSID` / `WIFI_PASSWORD` | the hotspot's network |
   | `CLIENT_MAC` | the client to credit (see below) |
   | `NODE_ID` | a unique name for this box, e.g. `piso-3` |
   | `COIN_PIN` | the pin you wired, if not the default |

3. **Decide which client to credit.**

   - **Kiosk (the machine is the client).** Leave `CLIENT_MAC` empty. The sketch
     reports the board's own station MAC, which is printed on boot.
   - **Customer's own phone.** Set `CLIENT_MAC` to the phone's MAC. Every phone
     has a different one, so on a shared machine use the kiosk form.

   The controller keys the balance on the MAC when it has one and falls back to
   the DHCP address otherwise, so a mis-set MAC shows up as a balance that never
   moves rather than as a lost coin.

4. **Flash it** and open the serial monitor at **115200 baud**. The boot banner
   prints the MAC being credited and the pin being watched. Drop a coin in - you
   should see:

   ```
   [coin] reported 1 pulse(s), balance now 5 min
   ```


## Verifying without hardware

The endpoints can be exercised from a laptop, which is the fastest way to tell a
wiring fault from a controller fault:

```sh
# Would this be counted?
curl -i -X POST http://192.168.88.1/api/coin-pulse \
  -H 'X-Coin-Token: <your token>' \
  -H 'Content-Type: application/json' \
  -d '{"mac":"AA:BB:CC:DD:EE:FF","pulses":1,"node_id":"box-1","event_id":"test-1"}'

# What does the portal see?
curl 'http://192.168.88.1/api/coin-status?mac=AA:BB:CC:DD:EE:FF'
```

`401` means the token is wrong. `400` with a `message` naming a field means the
sketch and the controller disagree. A `200` with `"duplicate":true` means that
`event_id` was already counted - which is what a *retry* looks like, and is the
expected answer when you deliberately send the same `event_id` twice.

## Why the sketch is written this way

A coin must never be lost and never counted twice. Each of these exists for that
reason:

- **Counting happens in the ISR.** A burst of pulses arriving faster than the
  main loop can run is still counted. The handler does nothing but increment and
  timestamp - a `Serial.print` or an HTTP call in an ISR would make the sketch
  miss the pulses it is trying to count.
- **Debouncing is a time window, not a flag.** A bouncing contact produces a
  burst of edges over a few milliseconds, all swallowed by the 60 ms window,
  while a genuine second coin a second later is counted normally.
- **Credit is cleared only after a `200`.** A failed POST leaves the pulses
  queued and they go out on the next attempt, so a controller reboot or a Wi-Fi
  dropout does not swallow a coin.
- **Every POST carries a monotonic `event_id`.** If the response is lost *after*
  the controller counted the coin, the retry is recognised as a duplicate and the
  customer is charged exactly once. This is the single most important line.
- **Pending pulses are journalled to flash first.** A power cut mid-coin - which
  on a coin machine is exactly when it happens - does not lose the money.
- **The read-modify-write of the counter runs with interrupts off**, so an edge
  arriving mid-subtraction cannot be silently discarded.
- **Wi-Fi association is rate limited.** A board hammering the radio in a
  reconnect loop makes the Wi-Fi worse for every paying customer on the same
  access point.

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| `[wifi] association failed` on every boot | Wrong SSID/password, or the box is out of range. The coin counter still runs; the pulses are queued in flash. |
| `401` from the controller | `COIN_NODE_TOKEN` differs from the controller's, or the controller has none set. |
| Balance never moves although the POST returns `200` | The MAC is wrong. Compare the MAC printed on boot with the one the portal polls for. |
| One coin counted as two, or as none | Missing or incorrect pull-up. See the wiring note. |
| `HTTP 400` naming a field | The sketch was edited without the controller being restarted, or a value is not a whole number. |

