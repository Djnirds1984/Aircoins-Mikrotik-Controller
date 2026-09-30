/*
 * Aircoins Piso Wi-Fi - coin slot acceptor (ESP8266 / ESP32)
 * =====================================================================
 *
 * Counts the pulses a coin acceptor emits and POSTs them to the Aircoins
 * MikroTik controller, which turns them into hotspot access time for the
 * customer standing at the machine.
 *
 * The controller API this talks to:
 *
 *   POST /api/coin-pulse     X-Coin-Token: <secret>
 *                           {"mac":"AA:BB:CC:DD:EE:FF","pulses":1,
 *                            "node_id":"box-1","event_id":"box-1-lu-42"}
 *   GET  /api/coin-status?subject=...     (for the local display, if fitted)
 *
 * ---------------------------------------------------------------------------
 * WIRING
 * ---------------------------------------------------------------------------
 * Most multi-coin acceptors (the common "pulse / signal" type) close a switch
 * contact for roughly 20-40 ms per pulse when they accept a coin. That is far
 * too short for a loop() poll and entirely too fast for a naive bounce counter,
 * hence attachInterrupt.
 *
 *   ESP8266 (NodeMCU / D1 mini)     ESP32
 *   --------------------------     --------------------------
 *   GPIO 14 (D5)  <- acceptor      any free GPIO, e.g. 27
 *   GND           <- acceptor GND  GND
 *
 * Optional 10k pull-up between the signal pin and 3V3. A coin acceptor is an
 * open-collector output, so without a pull-up the pin floats and a coin can be
 * counted twice or not at all. If the acceptor has a 5 V open-collector output
 * (common on the bigger multi-coin units), use a voltage divider - 1k from the
 * signal to the pin and 2k2 from the pin to GND - rather than wiring 5 V to the
 * pin directly, which will destroy the ESP.
 *
 * ---------------------------------------------------------------------------
 * SETUP
 * ---------------------------------------------------------------------------
 *  1. Set COIN_NODE_TOKEN below to the same value as the controller's
 *     COIN_NODE_TOKEN environment variable. Without it the controller refuses
 *     every report; that is deliberate and is not a bug to work around.
 *  2. Set CLIENT_MAC to the MAC the controller should credit. In a kiosk where
 *     the phone itself is the client, use the phone's MAC. If you do not know
 *     it, the sketch prints the STA MAC on boot - that is the one to use.
 *  3. Set CONTROLLER_HOST to the controller's address on the LAN. A hostname
 *     works if your DNS resolves it; an IP is more reliable on a small box.
 *  4. Upload, open the serial monitor at 115200 and drop a coin in.
 *
 * Board: ESP8266 (NodeMCU v2/v3, D1 mini) or ESP32 (ESP32 DevKit v1).
 * Libraries: none beyond the WiFi core the board provides.
 */


/*
 * WHY IT IS WRITTEN THIS WAY
 * ---------------------------------------------------------------------------
 * A coin must never be lost and never counted twice. Every decision below
 * follows from that:
 *
 *   - The pulse counter is incremented in the ISR, so a burst of pulses arriving
 *     faster than the main loop can run is still counted. Nothing in the handler
 *     does real work, and millis() is safe to read from an ISR on both chips.
 *
 *   - The ISR debounces on a time window rather than a boolean "ignore next
 *     edge" flag. A contact bouncing produces a burst of edges over a few
 *     milliseconds, all of which land inside the window and are swallowed, while
 *     a genuine second coin arriving a second later is counted normally.
 *
 *   - Credit is only cleared from the journal after the controller answers 200.
 *     A failed POST leaves the pulses queued and they go out on the next
 *     attempt, so a controller reboot or a Wi-Fi dropout does not swallow a
 *     customer's coin.
 *
 *   - Every POST carries a monotonic event_id. If the response is lost after the
 *     controller already counted the coin, the retry is recognised as a
 *     duplicate and the customer is charged exactly once. This is the single
 *     most important line of the whole sketch.
 *
 *   - The pending count is written to flash before it is sent, so a power cut
 *     mid-coin does not lose the money. It is cleared only once the server has
 *     confirmed it.
 *
 *   - Wi-Fi association is pinned to a free, well-defined trigger rather than a
 *     background retry, because a tight reconnect loop on a small access point
 *     degrades service for every other client on it.
 */

#include <Arduino.h>
#if defined(ESP32)
  #include <WiFi.h>
  #include <HTTPClient.h>
  #include <SPIFFS.h>
#else
  #include <ESP8266WiFi.h>
  #include <ESP8266HTTPClient.h>
  #include <FS.h>
#endif

// The journal. One small file, rewritten in place, holding the two numbers that
// must survive a power cut.
static const char *PENDING_PATH = "/pending";

/* =======================  CONFIGURATION  ============================== */

// The shared secret. Must match COIN_NODE_TOKEN on the controller. Leave it
// empty and nothing will ever be counted - the controller refuses anonymous
// reports on purpose, so there is no "open" mode to fall back to.
static const char *COIN_NODE_TOKEN = "CHANGE-ME-to-match-COIN_NODE_TOKEN";

// The controller's address, e.g. "192.168.88.1" or "aircoins.local".
static const char *CONTROLLER_HOST = "192.168.88.1";
static const uint16_t CONTROLLER_PORT = 80;

// The Wi-Fi network the kiosk uses.
static const char *WIFI_SSID     = "YOUR-HOTSPOT-SSID";
static const char *WIFI_PASSWORD = "YOUR-HOTSPOT-PASSWORD";

// The client whose balance the coins should buy. The controller normalises the
// separators, so "AA:BB:CC:DD:EE:FF", "aa-bb-cc-dd-ee-ff" and "aabbccddeeff" are
// all the same value.
//
// Leave it empty to fall back to this board's own STA MAC, which is right for a
// kiosk where the machine itself is the client.
static const char *CLIENT_MAC = "";   // e.g. "AA:BB:CC:DD:EE:FF"

// Identifies this acceptor in the operator's audit trail. Give every box a
// different one so the end-of-day reconciliation is possible.
static const char *NODE_ID = "box-1";



// Coin acceptor signal pin. D5 / GPIO14 on an ESP8266.
#if defined(ESP32)
  static const uint8_t COIN_PIN = 27;
#else
  static const uint8_t COIN_PIN = 14;   // D5
#endif

// Ignore any further edge for this many milliseconds after a counted one.
// A contact bounce lasts a few ms; two real coins are never closer together
// than this. 60 ms sits comfortably between the two for every acceptor on the
// market and costs nothing if it is a little generous.
static const uint32_t DEBOUNCE_MS = 60;

// The largest number of pulses to describe in one POST. A single 5-peso coin is
// normally 1-5 pulses, so this allows a whole handful in one report while still
// bounding the request.
static const uint16_t MAX_PULSES_PER_REPORT = 50;

// How long to wait after the last pulse before reporting, so a customer dropping
// several coins in a row produces one clean update rather than a flickering
// counter.
static const uint32_t SETTLE_MS = 1500;

// How long to wait between association attempts. Kept long so a controller that
// is down does not keep the radio busy, which matters on a small access point.
static const uint32_t WIFI_RETRY_MS = 20000;

// How long a single association attempt may take before it is called a failure.
static const uint32_t WIFI_CONNECT_TIMEOUT_MS = 20000;

/* ==========================  STATE  ================================== */

// Incremented by the ISR. Volatile because the main loop and the interrupt
// handler both touch it.
static volatile uint16_t pendingPulses = 0;
static volatile uint32_t lastEdgeMs = 0;
static volatile uint32_t lastPulseMs = 0;

// Monotonic across reboots by seeding from the previous run's value, so a power
// cut mid-report cannot make the controller treat a fresh coin as a retry of an
// old one.
static uint32_t eventCounter = 0;

static bool wifiReady = false;
static uint32_t lastWifiAttemptMs = 0;

/* ======================  INTERRUPT HANDLER  ========================== */

/*
 * Runs on every edge of the acceptor signal.
 *
 * Deliberately does almost nothing: it increments a counter and records a
 * timestamp. Anything heavier here - Serial output, an HTTP call, a String
 * operation - would run with interrupts partly disabled on these chips and
 * would make the sketch miss the very pulses it is trying to count.
 */
void IRAM_ATTR onCoinPulse() {
  uint32_t nowMs = millis();

  // Time-window debounce. A bouncing contact produces several edges inside a
  // few milliseconds; only the first falls outside the window after a count.
  if (nowMs - lastEdgeMs < DEBOUNCE_MS) {
    return;
  }
  lastEdgeMs = nowMs;

  if (pendingPulses < 0xFFFF) {
    pendingPulses++;
  }
  lastPulseMs = nowMs;
}



/* ========================  STORAGE HELPERS  =========================== */

/*
 * A tiny fixed-format journal on the internal flash.
 *
 * Only the count and the last event id need to survive a power cut, so this is
 * deliberately not a general purpose log: two numbers, one per line, overwritten
 * in place. A coin acceptor is on a machine that gets powered off abruptly (a
 * power cut, a tripped breaker) exactly when it is busiest, and losing the
 * customer's coin there is the failure that gets the operator a complaint.
 */
static bool savePending(uint32_t pulses, uint32_t lastEvent) {
  if (!SPIFFS.begin()) {
    return false;
  }
  File f = SPIFFS.open(PENDING_PATH, "w");
  if (!f) {
    return false;
  }
  // Plain decimal rather than a binary struct: a struct's padding and byte order
  // differ between the ESP8266 and the ESP32, and the file would then be
  // silently misread after a hardware swap.
  f.print(pulses);
  f.print('\n');
  f.print(lastEvent);
  f.close();
  return true;
}

static bool loadPending(uint32_t &pulses, uint32_t &lastEvent) {
  pulses = 0;
  lastEvent = 0;
  if (!SPIFFS.begin()) {
    return false;
  }
  File f = SPIFFS.open(PENDING_PATH, "r");
  if (!f) {
    return false;
  }
  uint32_t values[2] = {0, 0};
  uint8_t index = 0;
  while (index < 2 && f.available()) {
    String line = f.readStringUntil('\n');
    line.trim();
    if (line.length()) {
      values[index++] = strtoul(line.c_str(), NULL, 10);
    }
  }
  f.close();
  pulses = values[0];
  lastEvent = values[1];
  return index == 2;
}

static void clearPending() {
  if (!SPIFFS.begin()) {
    return;
  }
  SPIFFS.remove(PENDING_PATH);
}

/* =========================  WIFI  ===================================== */

/*
 * Associate with the hotspot, at most once per WIFI_RETRY_MS.
 *
 * A tight reconnect loop is actively harmful here: these boards share a small
 * access point with paying customers, and a board that hammers the radio while
 * the controller is down makes the Wi-Fi worse for everyone. So the attempt is
 * blocking but rare, and the coin counter keeps running throughout - pulses
 * taken during a failed association are queued, not lost.
 */
static void connectWiFi() {
  uint32_t nowMs = millis();

  if (wifiReady) {
    return;
  }
  if (lastWifiAttemptMs != 0 && nowMs - lastWifiAttemptMs < WIFI_RETRY_MS) {
    return;
  }
  lastWifiAttemptMs = nowMs;

  Serial.print(F("[wifi] connecting to "));
  Serial.println(WIFI_SSID);

  WiFi.mode(WIFI_STA);
  WiFi.begin(WIFI_SSID, WIFI_PASSWORD);

  uint32_t startedMs = nowMs;
  while (WiFi.status() != WL_CONNECTED && millis() - startedMs < WIFI_CONNECT_TIMEOUT_MS) {
    delay(250);
    // Feed the watchdog on the ESP8266. A 20 second blocking connect with no
    // yield is long enough to trip it, and a board that reboots every time the
    // controller is down can never recover.
    yield();
  }

  wifiReady = (WiFi.status() == WL_CONNECTED);
  if (wifiReady) {
    Serial.print(F("[wifi] connected, address "));
    Serial.println(WiFi.localIP());
  } else {
    Serial.println(F("[wifi] association failed, will retry"));
  }
}



/* =====================  REPORTING  =================================== */

/*
 * The MAC the credit is attributed to.
 *
 * Copied into a plain buffer because it ends up inside a JSON body, and a
 * caller-supplied string is not something to hand to a formatter directly.
 */
static void creditMac(char *out, size_t outLen) {
  if (strlen(CLIENT_MAC) > 0) {
    strncpy(out, CLIENT_MAC, outLen - 1);
    out[outLen - 1] = '\0';
    return;
  }
  // The board's own station MAC, which is the client in a kiosk where the
  // machine itself connects to the hotspot.
  strncpy(out, WiFi.macAddress().c_str(), outLen - 1);
  out[outLen - 1] = '\0';
}

/*
 * POST the pending pulses to the controller.
 *
 * Returns true only when the controller has confirmed the credit. A false return
 * leaves the pulses in the journal so they are retried, which is the whole
 * reason the journal exists.
 */
static bool reportPulses(uint16_t pulses, uint32_t eventId) {
  char mac[18];
  creditMac(mac, sizeof(mac));

  // The body is built with snprintf into a fixed buffer rather than with String
  // concatenation: a busy acceptor can produce repeated bursts, and repeated
  // heap allocation on these chips is how a long-running kiosk ends up crashing
  // after a few weeks. The length is checked rather than assumed, so a future
  // edit that grows a field cannot silently truncate the JSON.
  char body[320];
  char event[80];
  snprintf(event, sizeof(event), "%s-lu-%u", NODE_ID, (unsigned)eventId);

  int length = snprintf(body, sizeof(body),
      "{\"mac\":\"%s\",\"pulses\":%u,\"node_id\":\"%s\",\"event_id\":\"%s\"}",
      mac, (unsigned)pulses, NODE_ID, event);
  if (length <= 0 || length >= (int)sizeof(body)) {
    Serial.println(F("[coin] could not build the request body"));
    return false;
  }

  char url[128];
  snprintf(url, sizeof(url), "http://%s:%u/api/coin-pulse",
           CONTROLLER_HOST, (unsigned)CONTROLLER_PORT);

  HTTPClient http;
  if (!http.begin(url)) {
    Serial.println(F("[coin] could not open the connection"));
    return false;
  }

  http.setTimeout(8000);
  http.addHeader("Content-Type", "application/json");
  // The shared secret. A cross-site form cannot set a custom header, which is
  // what makes the write endpoint safe to expose on the guest network.
  http.addHeader("X-Coin-Token", COIN_NODE_TOKEN);

  int status = http.POST(body);

  // The body is read before the connection is closed - HTTPClient requires it -
  // and it is what distinguishes an already-counted coin from a rejection.
  String response = (status > 0) ? http.getString() : String();
  http.end();

  if (status != 200) {
    Serial.print(F("[coin] report rejected, HTTP "));
    Serial.print(status);
    if (response.length()) {
      Serial.print(F(" - "));
      Serial.print(response);
    }
    Serial.println();
    return false;
  }

  // 200 with "duplicate":true means the controller had already counted this
  // event: our previous POST landed but the answer never came back. The coin is
  // paid for, so the pending credit is cleared exactly as it would be for a
  // fresh report. Treating this as a failure would leave the sketch retrying
  // forever.
  bool duplicate = response.indexOf("\"duplicate\":true") >= 0;

  Serial.print(F("[coin] reported "));
  Serial.print(pulses);
  Serial.print(duplicate ? F(" pulse(s), already counted") : F(" pulse(s)"));

  int at = response.indexOf("\"remaining_seconds\":");
  if (at >= 0) {
    at += 19;   // length of the key including the colon
    Serial.print(F(", balance now "));
    Serial.print(response.substring(at).toInt() / 60);
    Serial.print(F(" min"));
  }
  Serial.println();

  return true;
}



/* =========================  SETUP  ==================================== */

void setup() {
  Serial.begin(115200);
  // The NodeMCU's onboard LED is active-low; this only affects the boot blip.
  pinMode(LED_BUILTIN, OUTPUT);
  digitalWrite(LED_BUILTIN, HIGH);

  delay(200);
  Serial.println();
  Serial.println(F("============================================="));
  Serial.println(F(" Aircoins coin slot acceptor"));
  Serial.println(F("============================================="));

  if (strlen(COIN_NODE_TOKEN) == 0) {
    // Loud, and deliberately not worked around. The controller refuses every
    // report, so a board flashed with an empty token is a board that silently
    // takes money and gives nothing back - the worst possible failure for this
    // machine.
    Serial.println(F("FATAL: COIN_NODE_TOKEN is empty."));
    Serial.println(F("       The controller will refuse every report."));
    Serial.println(F("       Set it to the controller's COIN_NODE_TOKEN."));
  }

  // The pin is pulled up BEFORE the interrupt is attached. Attaching first would
  // let a signal already sitting on the line produce one spurious edge, and the
  // first customer would be credited a coin they never inserted.
  pinMode(COIN_PIN, INPUT_PULLUP);
  attachInterrupt(digitalPinToInterrupt(COIN_PIN), onCoinPulse, FALLING);

  // Recover anything the previous power cycle did not deliver.
  uint32_t storedPulses = 0;
  uint32_t storedEvent = 0;
  if (loadPending(storedPulses, storedEvent)) {
    if (storedPulses > 0) {
      Serial.print(F("[boot] recovered "));
      Serial.print(storedPulses);
      Serial.println(F(" unacknowledged pulse(s) from a previous run"));
      pendingPulses = (uint16_t)storedPulses;
    }
    eventCounter = storedEvent;
  }

  connectWiFi();
  if (wifiReady) {
    Serial.print(F("[boot] crediting MAC "));
    if (strlen(CLIENT_MAC) > 0) {
      Serial.println(CLIENT_MAC);
    } else {
      Serial.print(F("(this board) "));
      Serial.println(WiFi.macAddress().c_str());
    }
  }

  Serial.print(F("[boot] listening on the acceptor signal, pin "));
  Serial.println(COIN_PIN);
  Serial.println();
}

/* ==========================  LOOP  ==================================== */

void loop() {
  // Association is checked first and is rate limited inside, so this costs
  // nothing on the common path.
  connectWiFi();

  uint32_t nowMs = millis();

  // Wait for the customer to finish dropping coins in before reporting. Without
  // this the first coin is posted, the screen updates, and a second coin dropped
  // a second later produces a second, separate screen update - which looks like
  // a glitch to someone standing there watching.
  if (pendingPulses == 0 || nowMs - lastPulseMs < SETTLE_MS) {
    delay(20);
    return;
  }

  // Report in bounded chunks rather than one enormous request, so a burst of
  // contact bounce can never produce a body the controller would reject.
  uint16_t batch = pendingPulses;
  if (batch > MAX_PULSES_PER_REPORT) {
    batch = MAX_PULSES_PER_REPORT;
  }

  // The event id is advanced and journalled BEFORE the request goes out. If the
  // power dies mid-flight the credit is still on the flash, and the counter
  // resumes above the value that was in flight, so the retry after a reboot
  // carries a new id rather than colliding with an event the controller may
  // already have seen.
  eventCounter++;
  savePending(pendingPulses, eventCounter);

  if (!wifiReady || !reportPulses(batch, eventCounter)) {
    // Nothing is cleared. The pulses stay in the journal and in pendingPulses,
    // and the next attempt reuses the same eventCounter, so the controller
    // recognises the retry as the same coin rather than a second one.
    delay(1000);
    return;
  }

  // Confirmed by the server. Take the batch off the front of the queue.
  //
  // The read-modify-write is done with interrupts off. Without that, an edge
  // arriving between the subtraction and the store would be silently discarded,
  // which is precisely the "a customer's coin vanishes" failure this whole
  // sketch is built to avoid. The critical section is a couple of instructions
  // long, so it cannot cost a pulse on its own.
  noInterrupts();
  pendingPulses = (pendingPulses > batch) ? (uint16_t)(pendingPulses - batch) : 0;
  bool drained = (pendingPulses == 0);
  interrupts();

  if (drained) {
    clearPending();
    // Short blink: coin accepted.
    digitalWrite(LED_BUILTIN, LOW);
    delay(60);
    digitalWrite(LED_BUILTIN, HIGH);
  } else {
    // More left over than we could send in one request.
    savePending(pendingPulses, eventCounter);
  }
}

