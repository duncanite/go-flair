# go-flair

A Go library and command for Flair's smart vents and Puck thermostats, over Flair's cloud
API, the one way into them: the homes with their rooms, vents and pucks and what they
measure, and the writes that open a vent, set a room's target and change a home's mode.
Written against Flair's documentation and Flair's own client, and not yet run against
Flair's servers: [docs/design.md](docs/design.md) says which rule comes from where.

The command reads the API client's credentials from `FLAIR_CLIENT_ID` and
`FLAIR_CLIENT_SECRET` and takes them as no flag. They are made in the Flair app, under the
account's developer settings. With them set:

```bash
just flair structures          # the homes: mode, heating or cooling, home or away
just flair rooms               # temperature, target and humidity of each room
just flair vents               # how far each vent is open
just flair vents --readings    # with duct temperature, pressure, battery and signal
just flair pucks               # the pucks and what they measure
just flair mode manual         # this command moves the vents; `auto` gives them back to Flair
just flair vent "Office Vent" 100   # open a vent; 0 closes it
just flair room Office 70 --hold 3h # a room's target, in the home's own scale
just flair away                # and `home`
just flair heat-cool cool      # heat, cool, auto, or float for neither
```

**Each run asks Flair for a token, and Flair gives about fifty a day.** The command keeps
none between runs. A program built on the library keeps its `Client`, and when it must
outlive its process it keeps the token itself: `Client.Token` reads it, `Options.Token`
gives it back. The library stores nothing.

A vent holds the opening it is given in manual mode alone, and a room's target is acted
on in auto mode alone; the command says so when the home's mode undoes what it was asked.
A home with several structures names one with `--structure`, by its name or its id.

[docs/design.md](docs/design.md) is the research the module starts from, what the system
offers and the ways into it, and the shape of the module with its decisions.
