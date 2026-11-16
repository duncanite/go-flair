package flair

import (
	"context"
	"fmt"
	"math"
	"time"
)

// Mode is who moves the vents: Flair, toward each room's target, or the
// client.
type Mode string

// The modes the documentation gives.
const (
	Auto   Mode = "auto"
	Manual Mode = "manual"
)

// HeatCool is what a structure's system is doing.
type HeatCool string

// The documentation gives heat, cool and auto; float, which is neither,
// is what the Home Assistant integration sends for off.
const (
	Heat     HeatCool = "heat"
	Cool     HeatCool = "cool"
	HeatAuto HeatCool = "auto"
	Float    HeatCool = "float"
)

// Structure is a home.
type Structure struct {
	ID   string
	Name string
	// Mode says who moves the vents. A vent holds the opening a client
	// sets in Manual alone; a room's target is acted on in Auto alone.
	Mode     Mode
	HeatCool HeatCool
	// Home is false when the structure is away.
	Home bool
	// Fahrenheit is the scale the structure shows temperatures in; the API
	// speaks Celsius whatever it is.
	Fahrenheit bool
	// TimeZone is where the structure is, as an IANA name.
	TimeZone string
}

// Room is a room of a structure. A measure Flair gave none for is nil.
type Room struct {
	ID          string
	Name        string
	StructureID string
	// TemperatureC is the room's temperature over its sensors, SetPointC
	// its target, both Celsius; Humidity is relative, in percent.
	TemperatureC *float64
	SetPointC    *float64
	Humidity     *float64
	// Active is false for a room Flair leaves out in Auto.
	Active bool
	// HoldUntil is when a target set by hand ends; zero when none holds.
	HoldUntil time.Time
}

// Vent is a register.
type Vent struct {
	ID          string
	Name        string
	RoomID      string
	StructureID string
	// PercentOpen is 0 closed to 100 open.
	PercentOpen int
	// Inactive is true for a vent Flair does not hear.
	Inactive bool
}

// Puck is a wall unit, of either generation: the first is the type pucks
// in the API, read by Pucks, the second puck2s, read by Puck2s.
type Puck struct {
	ID          string
	Name        string
	RoomID      string
	StructureID string
	// Second is true for a Puck of the second generation.
	Second       bool
	TemperatureC *float64
	Humidity     *float64
	Inactive     bool
	// Gateway is true for a first-generation Puck that is the system's
	// gateway.
	Gateway bool
}

// VentReading is what a vent last measured.
type VentReading struct {
	At time.Time
	// DuctTemperatureC is Celsius, DuctPressure kilopascals, Voltage the
	// battery's volts. RSSI is the 915 MHz signal's strength; the
	// documentation gives it no unit.
	DuctTemperatureC *float64
	DuctPressure     *float64
	Voltage          *float64
	RSSI             *float64
	PercentOpen      int
}

// The attributes, as the documentation's examples show them.
type (
	structureAttributes struct {
		Name             string `json:"name"`
		Mode             string `json:"mode"`
		HeatCoolMode     string `json:"structure-heat-cool-mode"`
		Home             bool   `json:"home"`
		TemperatureScale string `json:"temperature-scale"`
		TimeZone         string `json:"time-zone"`
	}
	roomAttributes struct {
		Name                string   `json:"name"`
		CurrentTemperatureC *float64 `json:"current-temperature-c"`
		SetPointC           *float64 `json:"set-point-c"`
		CurrentHumidity     *float64 `json:"current-humidity"`
		Active              bool     `json:"active"`
		HoldUntil           *string  `json:"hold-until"`
	}
	ventAttributes struct {
		Name        string  `json:"name"`
		PercentOpen float64 `json:"percent-open"`
		Inactive    bool    `json:"inactive"`
	}
	puckAttributes struct {
		Name                string   `json:"name"`
		CurrentTemperatureC *float64 `json:"current-temperature-c"`
		CurrentHumidity     *float64 `json:"current-humidity"`
		Inactive            bool     `json:"inactive"`
		IsGateway           bool     `json:"is-gateway"`
	}
	ventReadingAttributes struct {
		CreatedAt        string   `json:"created-at"`
		DuctTemperatureC *float64 `json:"duct-temperature-c"`
		DuctPressure     *float64 `json:"duct-pressure"`
		SystemVoltage    *float64 `json:"system-voltage"`
		RSSI             *float64 `json:"rssi"`
		PercentOpen      float64  `json:"percent-open"`
	}
)

// moment is a time as the API writes one, ISO 8601 with an offset; what
// is not one is the zero time.
func moment(written *string) time.Time {
	if written == nil {
		return time.Time{}
	}

	read, err := time.Parse(time.RFC3339Nano, *written)
	if err != nil {
		return time.Time{}
	}

	return read
}

// Structures lists the homes the client may see.
func (c *Client) Structures(ctx context.Context) ([]Structure, error) {
	records, err := c.list(ctx, "/api/structures")
	if err != nil {
		return nil, err
	}

	structures := make([]Structure, 0, len(records))

	for _, one := range records {
		var attributes structureAttributes
		if err = one.attributes(&attributes); err != nil {
			return nil, err
		}

		structures = append(structures, Structure{
			ID:         one.ID,
			Name:       attributes.Name,
			Mode:       Mode(attributes.Mode),
			HeatCool:   HeatCool(attributes.HeatCoolMode),
			Home:       attributes.Home,
			Fahrenheit: attributes.TemperatureScale == "F",
			TimeZone:   attributes.TimeZone,
		})
	}

	return structures, nil
}

// Rooms lists the rooms of every structure the client may see.
func (c *Client) Rooms(ctx context.Context) ([]Room, error) {
	records, err := c.list(ctx, "/api/rooms")
	if err != nil {
		return nil, err
	}

	rooms := make([]Room, 0, len(records))

	for _, one := range records {
		var attributes roomAttributes
		if err = one.attributes(&attributes); err != nil {
			return nil, err
		}

		home, err := c.related(ctx, &one, "structure")
		if err != nil {
			return nil, err
		}

		rooms = append(rooms, Room{
			ID:           one.ID,
			Name:         attributes.Name,
			StructureID:  home,
			TemperatureC: attributes.CurrentTemperatureC,
			SetPointC:    attributes.SetPointC,
			Humidity:     attributes.CurrentHumidity,
			Active:       attributes.Active,
			HoldUntil:    moment(attributes.HoldUntil),
		})
	}

	return rooms, nil
}

// Vents lists the vents.
func (c *Client) Vents(ctx context.Context) ([]Vent, error) {
	records, err := c.list(ctx, "/api/vents")
	if err != nil {
		return nil, err
	}

	vents := make([]Vent, 0, len(records))

	for _, one := range records {
		var attributes ventAttributes
		if err = one.attributes(&attributes); err != nil {
			return nil, err
		}

		room, home, err := c.placed(ctx, &one)
		if err != nil {
			return nil, err
		}

		vents = append(vents, Vent{
			ID:          one.ID,
			Name:        attributes.Name,
			RoomID:      room,
			StructureID: home,
			PercentOpen: int(math.Round(attributes.PercentOpen)),
			Inactive:    attributes.Inactive,
		})
	}

	return vents, nil
}

// placed is the room and the home a vent or a puck is in.
func (c *Client) placed(ctx context.Context, one *resource) (room, home string, err error) {
	if room, err = c.related(ctx, one, "room"); err != nil {
		return "", "", err
	}

	if home, err = c.related(ctx, one, "structure"); err != nil {
		return "", "", err
	}

	return room, home, nil
}

// Pucks lists the pucks of the first generation.
func (c *Client) Pucks(ctx context.Context) ([]Puck, error) {
	return c.pucksOf(ctx, "pucks")
}

// Puck2s lists the pucks of the second generation, which the API keeps as
// a type of its own. The documentation writes such a puck's room and home
// as links: each is followed, two requests a puck. It names no scope for
// them.
func (c *Client) Puck2s(ctx context.Context) ([]Puck, error) {
	return c.pucksOf(ctx, secondGeneration)
}

// secondGeneration is the type of the second generation's pucks.
const secondGeneration = "puck2s"

// pucksOf lists the pucks of one generation, by its type.
func (c *Client) pucksOf(ctx context.Context, kind string) ([]Puck, error) {
	records, err := c.list(ctx, "/api/"+kind)
	if err != nil {
		return nil, err
	}

	pucks := make([]Puck, 0, len(records))

	for _, one := range records {
		var attributes puckAttributes
		if err = one.attributes(&attributes); err != nil {
			return nil, err
		}

		room, home, err := c.placed(ctx, &one)
		if err != nil {
			return nil, err
		}

		pucks = append(pucks, Puck{
			ID:           one.ID,
			Name:         attributes.Name,
			RoomID:       room,
			StructureID:  home,
			Second:       kind == secondGeneration,
			TemperatureC: attributes.CurrentTemperatureC,
			Humidity:     attributes.CurrentHumidity,
			Inactive:     attributes.Inactive,
			Gateway:      attributes.IsGateway,
		})
	}

	return pucks, nil
}

// VentReading is what the vent last measured.
func (c *Client) VentReading(ctx context.Context, vent string) (VentReading, error) {
	path, err := itemPath("vents", vent, "/current-reading")
	if err != nil {
		return VentReading{}, err
	}

	one, err := c.one(ctx, path)
	if err != nil {
		return VentReading{}, err
	}

	var attributes ventReadingAttributes
	if err = one.attributes(&attributes); err != nil {
		return VentReading{}, err
	}

	return VentReading{
		At:               moment(&attributes.CreatedAt),
		DuctTemperatureC: attributes.DuctTemperatureC,
		DuctPressure:     attributes.DuctPressure,
		Voltage:          attributes.SystemVoltage,
		RSSI:             attributes.RSSI,
		PercentOpen:      int(math.Round(attributes.PercentOpen)),
	}, nil
}

// fullyOpen is a vent wide open.
const fullyOpen = 100

// The attributes a write sets.
type (
	ventChange struct {
		PercentOpen int `json:"percent-open"`
	}
	roomChange struct {
		SetPointC float64 `json:"set-point-c"`
		HoldUntil string  `json:"hold-until,omitempty"`
	}
	modeChange struct {
		Mode Mode `json:"mode"`
	}
	homeChange struct {
		Home bool `json:"home"`
	}
	heatCoolChange struct {
		HeatCoolMode HeatCool `json:"structure-heat-cool-mode"`
	}
)

// SetVent opens a vent to percent, 0 closed to 100 open. The
// documentation gives 0 and 100; the Home Assistant integration also
// sends 50; what Flair does with another value is not known. Flair moves
// the vent again unless the structure is in Manual.
func (c *Client) SetVent(ctx context.Context, vent string, percent int) error {
	if percent < 0 || percent > fullyOpen {
		return fmt.Errorf("%w: a vent open at %d percent", ErrArgument, percent)
	}

	return c.patch(ctx, "vents", vent, ventChange{PercentOpen: percent})
}

// holdLayout is a time as the documentation's example writes one.
const holdLayout = "2006-01-02T15:04:05.000000-07:00"

// The targets SetRoom takes, in Celsius. The documentation gives a room's
// target no range: these are this module's own, wide of any room, and
// what they refuse is a target written in the other scale.
const (
	MinSetPointC = 5.0
	MaxSetPointC = 40.0
)

// SetRoom sets a room's target, in Celsius, MinSetPointC to MaxSetPointC,
// held until until; a zero until leaves the hold to the structure's own
// default. Flair acts on it in Auto alone.
func (c *Client) SetRoom(ctx context.Context, room string, setPointC float64, until time.Time) error {
	// Written so that what is no number is refused too.
	if !(setPointC >= MinSetPointC && setPointC <= MaxSetPointC) {
		return fmt.Errorf("%w: a target of %.1f Celsius, outside %.0f to %.0f",
			ErrArgument, setPointC, MinSetPointC, MaxSetPointC)
	}

	set := roomChange{SetPointC: setPointC}

	if !until.IsZero() {
		set.HoldUntil = until.UTC().Format(holdLayout)
	}

	return c.patch(ctx, "rooms", room, set)
}

// SetMode says who moves a structure's vents. The documentation says to
// set the mode and shows no request for it; this is the one the Home
// Assistant integration sends.
func (c *Client) SetMode(ctx context.Context, structure string, mode Mode) error {
	if mode != Auto && mode != Manual {
		return fmt.Errorf("%w: the mode %q", ErrArgument, mode)
	}

	return c.patch(ctx, "structures", structure, modeChange{Mode: mode})
}

// SetHome sets a structure home, or away. The request is the Home
// Assistant integration's; the documentation shows none.
func (c *Client) SetHome(ctx context.Context, structure string, home bool) error {
	return c.patch(ctx, "structures", structure, homeChange{Home: home})
}

// SetHeatCool sets what a structure's system does. The request is the
// Home Assistant integration's; the documentation shows none.
func (c *Client) SetHeatCool(ctx context.Context, structure string, mode HeatCool) error {
	switch mode {
	case Heat, Cool, HeatAuto, Float:
		return c.patch(ctx, "structures", structure, heatCoolChange{HeatCoolMode: mode})
	default:
		return fmt.Errorf("%w: the heat and cool mode %q", ErrArgument, mode)
	}
}
