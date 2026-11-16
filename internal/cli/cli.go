// Package cli is the flair command: what Flair's cloud API says of a
// home's rooms, vents and pucks, and the writes that open a vent, set a
// room's target and change the home's mode. The API client's credentials
// come from the environment and from nowhere else.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/alecthomas/kong"

	"github.com/duncanite/go-flair/flair"
)

// Exit statuses.
const (
	ExitOK    = 0
	ExitError = 1
)

// The environment variables the credentials are read from.
const (
	EnvClientID     = "FLAIR_CLIENT_ID"
	EnvClientSecret = "FLAIR_CLIENT_SECRET" // #nosec G101 -- the name of an environment variable, not a credential
)

// clock is how the command prints a time, in the home's zone.
const clock = "2006-01-02 15:04"

// errUsage is a command line, or an environment, the command cannot act
// on.
var errUsage = errors.New("cannot run")

// exitStatus is how the parser's own exit, on --help, leaves Run: kong
// calls its exit function and goes on, so the function panics with the
// status and Run recovers it.
type exitStatus int

type rootCommand struct {
	API       string `default:"https://api.flair.co" env:"FLAIR_API"                                                                                help:"Where Flair's API is."`
	Scope     string `env:"FLAIR_SCOPE"              help:"The scopes the token is asked for, separated by spaces; the module's own when left out."`
	Structure string `env:"FLAIR_STRUCTURE"          help:"The home, by its name or its id; the only one when left out."`

	Structures structuresCommand `cmd:"" help:"List the homes: their mode, heating or cooling, home or away."`
	Rooms      roomsCommand      `cmd:"" help:"List the home's rooms: temperature, target, humidity."`
	Vents      ventsCommand      `cmd:"" help:"List the home's vents and how far each is open."`
	Pucks      pucksCommand      `cmd:"" help:"List the home's pucks and what they measure."`
	Vent       ventCommand       `cmd:"" help:"Open a vent to a percentage. Flair moves it again unless the home is in manual mode."`
	Room       roomCommand       `cmd:"" help:"Set a room's target temperature. Flair acts on it in auto mode alone."`
	Mode       modeCommand       `cmd:"" help:"Say who moves the vents: Flair (auto) or this command (manual)."`
	Home       homeCommand       `cmd:"" help:"Set the home as occupied."`
	Away       awayCommand       `cmd:"" help:"Set the home as away."`
	HeatCool   heatCoolCommand   `cmd:"" help:"Set what the home's system does: heat, cool, auto, or float for neither."`

	out    io.Writer
	client *flair.Client
}

type (
	structuresCommand struct{}
	roomsCommand      struct{}
	pucksCommand      struct{}
	homeCommand       struct{}
	awayCommand       struct{}
	ventsCommand      struct {
		Readings bool `help:"Add what each vent last measured: one more request a vent."`
	}
	ventCommand struct {
		Vent    string `arg:"" help:"The vent, by its name or its id."`
		Percent int    `arg:"" help:"How far open, 0 to 100. Flair documents 0 and 100."`
	}
	roomCommand struct {
		Room        string        `arg:""                                                                                             help:"The room, by its name or its id."`
		Temperature float64       `arg:""                                                                                             help:"The target, in the home's own scale."`
		Hold        time.Duration `help:"How long the target holds, in hours and minutes: 3h, 90m. The home's default when left out."`
	}
	modeCommand struct {
		Mode string `arg:"" enum:"auto,manual" help:"auto or manual."`
	}
	heatCoolCommand struct {
		Mode string `arg:"" enum:"heat,cool,auto,float" help:"heat, cool, auto or float."`
	}
)

// Run parses args and runs the command. client makes every request to
// Flair; getenv reads the environment, where the credentials are.
func Run(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	client *http.Client,
	getenv func(string) string,
) (status int) {
	defer func() {
		if recovered := recover(); recovered != nil {
			exited, ok := recovered.(exitStatus)
			if !ok {
				panic(recovered)
			}

			status = int(exited)
		}
	}()

	root := rootCommand{out: stdout}

	parser, err := kong.New(&root,
		kong.Name("flair"),
		kong.Description("Ask Flair's cloud API about a home's rooms, vents and pucks, and drive them. "+
			"The API client's credentials are read from "+EnvClientID+" and "+EnvClientSecret+
			". Each run asks Flair for a token, and Flair gives about fifty a day."),
		kong.UsageOnError(),
		kong.Writers(stdout, stderr),
		kong.Exit(func(code int) { panic(exitStatus(code)) }),
		kong.BindTo(ctx, (*context.Context)(nil)),
	)
	if err != nil {
		fmt.Fprintln(stderr, "flair:", err)

		return ExitError
	}

	kctx, err := parser.Parse(args)
	if err != nil {
		parser.Errorf("%s", err)

		return ExitError
	}

	if err = root.connect(client, getenv); err == nil {
		err = kctx.Run(&root)
	}

	if err != nil {
		fmt.Fprintln(stderr, "flair:", err)

		return ExitError
	}

	return ExitOK
}

// connect makes the API client from the flags and the environment; a
// credential that is not set is said by its variable's name.
func (root *rootCommand) connect(client *http.Client, getenv func(string) string) error {
	var missing []string

	for _, name := range []string{EnvClientID, EnvClientSecret} {
		if getenv(name) == "" {
			missing = append(missing, name)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("%w: not set in the environment: %s", errUsage, strings.Join(missing, ", "))
	}

	root.client = flair.New(
		flair.Credentials{ClientID: getenv(EnvClientID), ClientSecret: getenv(EnvClientSecret)},
		flair.Options{BaseURL: root.API, HTTP: client, Scope: root.Scope},
	)

	return nil
}

// named is one of several records asked for by its id, or by its name
// whatever its case: exactly one, or an error with the names there are.
func named[T any](records []T, asked, kind string, identify func(T) (id, name string)) (T, error) {
	var (
		found []T
		names []string
	)

	for _, one := range records {
		id, name := identify(one)
		names = append(names, strconv.Quote(name))

		if asked == "" || id == asked || strings.EqualFold(name, asked) {
			found = append(found, one)
		}
	}

	if len(found) == 1 {
		return found[0], nil
	}

	var none T

	if asked == "" {
		return none, fmt.Errorf(
			"%w: there are %d %ss, name one: %s",
			errUsage,
			len(found),
			kind,
			strings.Join(names, ", "),
		)
	}

	return none, fmt.Errorf(
		"%w: %d %ss are %q; there are %s",
		errUsage,
		len(found),
		kind,
		asked,
		strings.Join(names, ", "),
	)
}

// home is the structure a command acts on: the one --structure names, or
// the only one.
func (root *rootCommand) home(ctx context.Context) (flair.Structure, error) {
	structures, err := root.client.Structures(ctx)
	if err != nil {
		return flair.Structure{}, err
	}

	return named(structures, root.Structure, "home", func(one flair.Structure) (string, string) {
		return one.ID, one.Name
	})
}

// rooms is the home's rooms, and their names by their ids.
func (root *rootCommand) rooms(ctx context.Context, home *flair.Structure) ([]flair.Room, map[string]string, error) {
	all, err := root.client.Rooms(ctx)
	if err != nil {
		return nil, nil, err
	}

	var rooms []flair.Room

	names := map[string]string{}

	for _, room := range all {
		if room.StructureID == home.ID {
			rooms = append(rooms, room)
			names[room.ID] = room.Name
		}
	}

	return rooms, names, nil
}

// degrees is a temperature of the API, Celsius, in the home's scale; a
// dash when Flair gave none.
func degrees(home *flair.Structure, celsius *float64) string {
	switch {
	case celsius == nil:
		return "-"
	case home.Fahrenheit:
		return strconv.FormatFloat(toFahrenheit(*celsius), 'f', 1, 64) + "F"
	default:
		return strconv.FormatFloat(*celsius, 'f', 1, 64) + "C"
	}
}

// toFahrenheit and toCelsius turn a temperature from one scale to the
// other.
func toFahrenheit(celsius float64) float64 {
	return celsius*9/5 + 32
}

func toCelsius(fahrenheit float64) float64 {
	return (fahrenheit - 32) * 5 / 9
}

// measure is a value with its unit; a dash when Flair gave none.
func measure(value *float64, unit string) string {
	if value == nil {
		return "-"
	}

	return strconv.FormatFloat(*value, 'f', -1, 64) + unit
}

// where is the zone a home's times are printed in: its own, or this
// machine's when Flair names one this machine does not know.
func where(home *flair.Structure) *time.Location {
	if location, err := time.LoadLocation(home.TimeZone); err == nil && home.TimeZone != "" {
		return location
	}

	//nolint:gosmopolitan // the fallback for a zone this machine cannot load
	return time.Local
}

// room is a room's name for a table: quoted, its owner's text; a dash for
// a record in no room.
func room(names map[string]string, id string) string {
	if name, known := names[id]; known {
		return strconv.Quote(name)
	}

	return "-"
}

func (*structuresCommand) Run(ctx context.Context, root *rootCommand) error {
	structures, err := root.client.Structures(ctx)
	if err != nil {
		return err
	}

	table := tabwriter.NewWriter(root.out, 0, 0, 2, ' ', 0)

	fmt.Fprintln(table, "NAME\tMODE\tSYSTEM\tHOME\tSCALE\tID")

	for _, one := range structures {
		scale := "C"
		if one.Fahrenheit {
			scale = "F"
		}

		fmt.Fprintf(table, "%q\t%s\t%s\t%s\t%s\t%s\n",
			one.Name, plain(string(one.Mode)), plain(string(one.HeatCool)), yesNo(one.Home), scale, plain(one.ID))
	}

	return flush(table)
}

func (*roomsCommand) Run(ctx context.Context, root *rootCommand) error {
	home, err := root.home(ctx)
	if err != nil {
		return err
	}

	rooms, _, err := root.rooms(ctx, &home)
	if err != nil {
		return err
	}

	table := tabwriter.NewWriter(root.out, 0, 0, 2, ' ', 0)

	fmt.Fprintln(table, "NAME\tTEMPERATURE\tTARGET\tHUMIDITY\tACTIVE\tHELD UNTIL\tID")

	for _, one := range rooms {
		held := "-"
		if !one.HoldUntil.IsZero() {
			held = one.HoldUntil.In(where(&home)).Format(clock)
		}

		fmt.Fprintf(table, "%q\t%s\t%s\t%s\t%s\t%s\t%s\n",
			one.Name, degrees(&home, one.TemperatureC), degrees(&home, one.SetPointC), measure(one.Humidity, "%"),
			yesNo(one.Active), held, plain(one.ID))
	}

	return flush(table)
}

func (cmd *ventsCommand) Run(ctx context.Context, root *rootCommand) error {
	home, err := root.home(ctx)
	if err != nil {
		return err
	}

	_, names, err := root.rooms(ctx, &home)
	if err != nil {
		return err
	}

	vents, err := root.client.Vents(ctx)
	if err != nil {
		return err
	}

	table := tabwriter.NewWriter(root.out, 0, 0, 2, ' ', 0)

	header := "NAME\tROOM\tOPEN\tHEARD"
	if cmd.Readings {
		header += "\tDUCT\tPRESSURE\tBATTERY\tSIGNAL\tMEASURED"
	}

	fmt.Fprintln(table, header+"\tID")

	for _, one := range vents {
		if one.StructureID != home.ID {
			continue
		}

		fmt.Fprintf(table, "%q\t%s\t%d%%\t%s", one.Name, room(names, one.RoomID), one.PercentOpen, yesNo(!one.Inactive))

		if cmd.Readings {
			reading, err := root.client.VentReading(ctx, one.ID)
			if err != nil {
				return err
			}

			measured := "-"
			if !reading.At.IsZero() {
				measured = reading.At.In(where(&home)).Format(clock)
			}

			fmt.Fprintf(
				table,
				"\t%s\t%s\t%s\t%s\t%s",
				degrees(&home, reading.DuctTemperatureC),
				measure(
					reading.DuctPressure,
					"kPa",
				),
				measure(reading.Voltage, "V"),
				measure(reading.RSSI, ""),
				measured,
			)
		}

		fmt.Fprintf(table, "\t%s\n", plain(one.ID))
	}

	return flush(table)
}

func (*pucksCommand) Run(ctx context.Context, root *rootCommand) error {
	home, err := root.home(ctx)
	if err != nil {
		return err
	}

	_, names, err := root.rooms(ctx, &home)
	if err != nil {
		return err
	}

	pucks, err := root.client.Pucks(ctx)
	if err != nil {
		return err
	}

	// The first generation's are printed whatever becomes of the second's:
	// the documentation names no scope for those, and a token may not
	// reach them.
	seconds, secondErr := root.client.Puck2s(ctx)

	table := tabwriter.NewWriter(root.out, 0, 0, 2, ' ', 0)

	fmt.Fprintln(table, "NAME\tKIND\tROOM\tTEMPERATURE\tHUMIDITY\tHEARD\tGATEWAY\tID")

	for _, one := range append(pucks, seconds...) {
		// A puck whose home Flair does not tell is listed under any.
		if one.StructureID != "" && one.StructureID != home.ID {
			continue
		}

		kind := "puck"
		if one.Second {
			kind = "puck2"
		}

		fmt.Fprintf(table, "%q\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			one.Name, kind, room(names, one.RoomID), degrees(&home, one.TemperatureC), measure(one.Humidity, "%"),
			yesNo(!one.Inactive), yesNo(one.Gateway), plain(one.ID))
	}

	if err = flush(table); err != nil {
		return err
	}

	if secondErr != nil {
		return fmt.Errorf("the second-generation pucks were not read: %w", secondErr)
	}

	return nil
}

func (cmd *ventCommand) Run(ctx context.Context, root *rootCommand) error {
	home, err := root.home(ctx)
	if err != nil {
		return err
	}

	all, err := root.client.Vents(ctx)
	if err != nil {
		return err
	}

	var vents []flair.Vent

	for _, one := range all {
		if one.StructureID == home.ID {
			vents = append(vents, one)
		}
	}

	if cmd.Vent == "" {
		return fmt.Errorf("%w: a vent with no name", errUsage)
	}

	vent, err := named(vents, cmd.Vent, "vent", func(one flair.Vent) (string, string) { return one.ID, one.Name })
	if err != nil {
		return err
	}

	if err = root.client.SetVent(ctx, vent.ID, cmd.Percent); err != nil {
		return err
	}

	if home.Mode != flair.Manual {
		fmt.Fprintf(root.out, "the home is in %s mode: Flair moves the vent again\n", plain(string(home.Mode)))
	}

	return nil
}

func (cmd *roomCommand) Run(ctx context.Context, root *rootCommand) error {
	if cmd.Hold < 0 || cmd.Room == "" {
		return fmt.Errorf("%w: a room with no name, or a hold below zero", errUsage)
	}

	home, err := root.home(ctx)
	if err != nil {
		return err
	}

	rooms, _, err := root.rooms(ctx, &home)
	if err != nil {
		return err
	}

	asked, err := named(rooms, cmd.Room, "room", func(one flair.Room) (string, string) { return one.ID, one.Name })
	if err != nil {
		return err
	}

	celsius := cmd.Temperature
	if home.Fahrenheit {
		celsius = toCelsius(cmd.Temperature)
	}

	var until time.Time
	if cmd.Hold > 0 {
		until = time.Now().Add(cmd.Hold)
	}

	if err = root.client.SetRoom(ctx, asked.ID, celsius, until); err != nil {
		return err
	}

	if home.Mode != flair.Auto {
		fmt.Fprintf(
			root.out,
			"the home is in %s mode: Flair does not act on a room's target\n",
			plain(string(home.Mode)),
		)
	}

	return nil
}

func (cmd *modeCommand) Run(ctx context.Context, root *rootCommand) error {
	home, err := root.home(ctx)
	if err != nil {
		return err
	}

	return root.client.SetMode(ctx, home.ID, flair.Mode(cmd.Mode))
}

func (*homeCommand) Run(ctx context.Context, root *rootCommand) error {
	home, err := root.home(ctx)
	if err != nil {
		return err
	}

	return root.client.SetHome(ctx, home.ID, true)
}

func (*awayCommand) Run(ctx context.Context, root *rootCommand) error {
	home, err := root.home(ctx)
	if err != nil {
		return err
	}

	return root.client.SetHome(ctx, home.ID, false)
}

func (cmd *heatCoolCommand) Run(ctx context.Context, root *rootCommand) error {
	home, err := root.home(ctx)
	if err != nil {
		return err
	}

	return root.client.SetHeatCool(ctx, home.ID, flair.HeatCool(cmd.Mode))
}

func yesNo(yes bool) string {
	if yes {
		return "yes"
	}

	return "no"
}

// plain is a word of Flair's, an id, a mode, as the command prints it: as
// it is when it is letters, digits, dashes and underscores, quoted when it
// is anything else, and a dash when it is empty.
func plain(value string) string {
	if value == "" {
		return "-"
	}

	for _, char := range value {
		if char != '-' && char != '_' && !unicode.IsLetter(char) && !unicode.IsDigit(char) {
			return strconv.Quote(value)
		}
	}

	return value
}

func flush(table *tabwriter.Writer) error {
	if err := table.Flush(); err != nil {
		return fmt.Errorf("writing the table: %w", err)
	}

	return nil
}
