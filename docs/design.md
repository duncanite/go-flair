# go-flair: the design

Research into Flair's HVAC system, its smart vents and its Puck thermostats, done on
2026-11-15 from public sources, to decide what a Go module for it would be. Nothing is
built and nothing here was run against the system. The decisions at the end are open.

In short: Flair is a cloud product. Its one interface is a documented REST API on Flair's
servers, which every open integration uses. The vents and pucks talk to their gateway over
a 915 MHz radio that nobody has decoded, and the gateway has no known interface on the
LAN.

## The device

From the resources and fields of [Flair's API documentation](https://documenter.getpostman.com/view/5353571/TzsbKTAG):

- **Vents** in the registers, battery or wire powered; **Pucks**, wall units that sense
  temperature and set a room's target, the newer Puck2 a resource of its own; a
  **gateway**, which is either one of the original Pucks on Wi-Fi or the Bridge, sold
  since 2024, on Ethernet or Wi-Fi
  ([its announcement](https://www.prnewswire.com/news-releases/introducing-the-bridge-flairs-new-dedicated-plug-and-play-networking-gateway-302151074.html)).
- Vents and pucks reach the gateway over a sub-GHz radio: the API reports a 915 MHz
  signal strength for each.
- The logic is in the cloud. In the structure's "auto" mode Flair's servers move the
  vents toward each room's target, and schedules and home/away work only in that mode; in
  "manual" a client sets each vent.

## Ways in

### Flair's cloud API (official)

- `https://api.flair.co`, JSON:API.
- OAuth2 client credentials: a client id and secret made in the Flair app, under account
  settings, developer settings. Scopes come in view and edit pairs.
- Flair's [own Python client](https://github.com/flair-systems/flair-api-client-py)
  says: "Access token creation is rate-limited (approximately 50 requests/day)", and that
  refreshing a token does not count. A client keeps its token.
- Resources: structures, rooms, vents, pucks, puck2s, bridges, thermostats, the infrared
  units for mini-splits, and a current reading for each sensing device.
- Set: a vent's `percent-open`; a room's set point and how long it holds; the structure's
  mode, its heat or cool mode, home or away. The documentation gives 0 and 100 for a vent;
  the [Home Assistant integration](https://github.com/RobertD502/home-assistant-flair)
  reports 50 as the one value between.
- No request limit is stated beyond the token's.

### The radio (local, not open)

[One project](https://github.com/jonnes86/Locally-Hosted-Flair-), begun in September
2026, has captured the traffic and not decoded it: the packet layout, the encryption and
the pairing are marked unknown in its notes, and it reports that replaying a capture does
not move a vent.

### Matter, HomeKit

None found, and no announcement of either.

## What this module would be

Not decided, and there is one option today: **a client of the cloud API**. The token
kept and refreshed within the daily limit; structures, rooms, vents, pucks and their
readings read; a vent's opening, a room's target and the structure's mode set. Small.

A local route would be the radio, reverse-engineered from nothing: a large piece of work
with no certain end, and a transmitter of one's own.

[flair-client](https://github.com/jahvon/flair-client) is the one Go client found; it has
no vents.

## Open questions

- What the gateway is: an original Puck, a Bridge, or a Bridge Pro; and whether there are
  Puck2s. It decides which resources apply.
- Whether the system runs in auto or manual mode: it decides whether the command sets
  rooms or vents.

## Not verified

- Nothing above was observed on the system.
- The radio's chip and modulation, and the FCC identifiers, are the one project's notes;
  the filings could not be read.
- Flair's statements on Matter and on local control were seen as search excerpts only.
- The API's request limits, and its terms: none found.

## Sources

- [Flair API documentation](https://documenter.getpostman.com/view/5353571/TzsbKTAG):
  authentication, resources, what can be set, the radio's signal fields, the modes.
- [flair-api-client-py](https://github.com/flair-systems/flair-api-client-py): Flair's
  client; the token limit.
- [home-assistant-flair](https://github.com/RobertD502/home-assistant-flair): the cloud
  in use; the vent's three positions.
- [Introducing the Bridge](https://www.prnewswire.com/news-releases/introducing-the-bridge-flairs-new-dedicated-plug-and-play-networking-gateway-302151074.html):
  the gateway of 2024.
- [Locally-Hosted-Flair-](https://github.com/jonnes86/Locally-Hosted-Flair-): the state
  of the radio's reverse engineering.
- [flair-client](https://github.com/jahvon/flair-client): a Go client of the cloud API.
