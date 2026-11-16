# go-flair: the design

Research into Flair's HVAC system, its smart vents and its Puck thermostats, done on
2026-11-15 from public sources, and the module built from it. The module follows Flair's
documentation and Flair's own client; nothing here was run against the system or against
Flair's servers.

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

## What this module is

**A client of the cloud API**, the one way in.

- `flair`: `New` takes the API client's id and secret. `Structures`, `Rooms`, `Vents`,
  `Pucks` and `Puck2s` read the homes and what is in them; `VentReading` what a vent
  last measured. The writes: `SetVent`, `SetRoom`, `SetMode`, `SetHome`, `SetHeatCool`.
  `Token` reads the token held, for a consumer to keep. The standard library alone.
- `flair/flairtest`: the API for tests, the token route with it.
- `cmd/flair`: the command, over `internal/cli`.

### Decisions

- **The credentials and the token are held where nothing prints them.** A client
  formatted, by value, by pointer or inside another struct, shows nothing of them. The
  command reads the credentials from `FLAIR_CLIENT_ID` and `FLAIR_CLIENT_SECRET` and
  takes them as no flag.
- **A token is kept for its life** and asked for again a minute before its end, or
  halfway through a life shorter than two, by the credentials: the documentation shows no
  refresh token for this grant. A life told as a string is read; a token whose life is
  not told is kept until Flair answers 401 to it, as Flair's own client does, and then
  asked for again, once. Flair's client says a refresh "doesn't count toward" the limit;
  whether this grant is given a refresh token is not known.
- **Keeping a token beyond a process is the consumer's, never the library's.** Flair
  gives fifty or so tokens a day, so a program that starts often cannot ask for one at
  each start. The library writes nothing anywhere: `Client.Token` gives the token held
  and its end, `Options.Token` takes one to start from, and where it is kept between the
  two is the consumer's business, a plugin's state or a store of its own. A token given
  is sent until its end or until Flair refuses it. **The command keeps none**: each run
  asks for a token.
- **A second-generation puck's room and home are followed.** The documentation writes
  them as links with no id beside them, where every other record carries the id: two
  requests more a puck. One whose home Flair does not tell is listed under any home.
  The command prints the first generation whatever becomes of the second, and says what
  it could not read: the documentation names no scope for it.
- **A room's target is 5 to 40 Celsius.** The documentation gives no range; this one is
  the module's own, wide of any room, and what it refuses is a target typed in the
  other scale.
- **The next page of a list is followed only when it is a path of the API**, so the
  token goes nowhere else.
- **Temperatures are Celsius in the library**, as the API speaks; the command shows and
  takes them in the home's own scale.
- **A measure Flair gave none for is nil**, not zero: a room with no sensor has no
  temperature.
- **The command says when the home's mode undoes a write**: a vent's opening holds in
  manual mode alone, a room's target is acted on in auto mode alone.
- **Not built**: bridges, thermostats, the infrared units, a puck's own reading,
  schedules, and whether a room is active.

### Where each rule comes from

Flair's documentation is a Postman collection, [PM]; its own Python client is [PY]; the
Home Assistant integration is [HA].

- The token route, its form and its scopes; the lists and their paging in `meta`; a
  vent's reading; the writes of a vent's opening and a room's target; the 404's body:
  [PM].
- The id sent in a write's body, the headers, following `meta.next`: [PY]. About fifty
  tokens a day: [PY], and [PM] under the token route it calls deprecated.
- **The writes of a structure's mode, of home and away, and of the heating and cooling
  mode are [HA]'s.** [PM] says to set the mode and shows no request for it. `float`,
  for neither heating nor cooling, is [HA]'s too.
- A vent open at 50 percent is [HA]'s; [PM] gives 0 and 100. Any value from 0 to 100 is
  sent, and what Flair does with another is not known.
- The scopes asked for by default follow [PM]'s rule, `<resource>.view` and
  `<resource>.edit`; it spells out three, none of them for vents or pucks, and none at
  all for the second-generation pucks.
- Errors as a list, on 403 and 422, are [HA]'s reading; [PM] shows one object. Both are
  read.
- No status is given for a client refused or a token without the scope: the token
  route's 400 and 401, and any 401 and 403, are read as the credentials refused. Any 2xx
  is a success, where [PM] shows 200.

## Open questions

- What the gateway is: an original Puck, a Bridge, or a Bridge Pro; and whether there are
  Puck2s. It decides which resources apply.
- Whether the system runs in auto or manual mode: it decides whether the command sets
  rooms or vents.

## Not verified

- Nothing above was observed on the system, and the module was run against its own
  stand-in only.
- Every rule marked [HA] above, and the default scopes.
- What a second-generation puck's link answers when the puck is in no room: a null and
  a 404 are both read as none.
- Whether a hold sent with a room's target is kept: the one answer [PM] shows for it
  came from a request that misspelled the key.
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
