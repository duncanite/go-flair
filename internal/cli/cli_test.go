package cli_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/duncanite/go-flair/flair/flairtest"
	"github.com/duncanite/go-flair/internal/cli"
)

func started(t *testing.T) *flairtest.API {
	t.Helper()

	api := flairtest.New()
	t.Cleanup(api.Close)

	return api
}

// run is the command against the fake under the client's credentials,
// with secret as its secret; its output and what it wrote of errors, then
// its status.
func run(t *testing.T, api *flairtest.API, secret string, args ...string) (out, errs string, status int) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	env := map[string]string{cli.EnvClientID: flairtest.ClientID, cli.EnvClientSecret: secret}

	args = append([]string{"--api", api.URL}, args...)
	status = cli.Run(t.Context(), args, &stdout, &stderr, api.Client(), func(name string) string { return env[name] })

	return stdout.String(), stderr.String(), status
}

// rows is the output as lines of fields, the spacing of a table aside.
func rows(out string) string {
	var lines []string

	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		lines = append(lines, strings.Join(strings.Fields(line), " "))
	}

	return strings.Join(lines, "\n")
}

// TestReading: each command that reads, its output as a person reads it:
// temperatures in the home's scale, times in its zone, a dash where Flair
// gave nothing.
func TestReading(t *testing.T) {
	t.Parallel()

	api := started(t)
	api.Rooms[1].HoldUntil = "2018-12-18T02:18:02.724772+00:00"

	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"structures"}, "NAME MODE SYSTEM HOME SCALE ID\n" + `"My Home" auto cool yes F 4351`},
		{[]string{"rooms"}, strings.Join([]string{
			"NAME TEMPERATURE TARGET HUMIDITY ACTIVE HELD UNTIL ID",
			`"Office" 68.0F 70.0F 60% yes - 8704`,
			`"Bedroom" - - - no 2018-12-17 18:18 8310`,
		}, "\n")},
		{[]string{"vents"}, strings.Join([]string{
			"NAME ROOM OPEN HEARD ID",
			`"Office Vent" "Office" 0% yes ` + flairtest.OfficeVentID,
			`"Bed Vent" "Bedroom" 100% no ` + flairtest.BedVentID,
		}, "\n")},
		{[]string{"--structure", "my home", "vents", "--readings"}, strings.Join([]string{
			"NAME ROOM OPEN HEARD DUCT PRESSURE BATTERY SIGNAL MEASURED ID",
			`"Office Vent" "Office" 0% yes 78.9F 101.2985kPa 3.02V -81 2019-09-26 16:31 ` + flairtest.OfficeVentID,
			`"Bed Vent" "Bedroom" 100% no 78.9F 101.2985kPa 3.02V -81 2019-09-26 16:31 ` + flairtest.BedVentID,
		}, "\n")},
		{[]string{"--structure", flairtest.HomeID, "pucks"}, strings.Join([]string{
			"NAME KIND ROOM TEMPERATURE HUMIDITY HEARD GATEWAY ID",
			//nolint:dupword // two columns of the table
			`"Bedroom Puck" puck "Bedroom" 68.9F 61% yes yes ` + flairtest.PuckID,
			`"Kitchen-ae4b" puck2 - 72.6F - yes no ` + flairtest.Puck2ID,
		}, "\n")},
	} {
		out, errs, status := run(t, api, flairtest.ClientSecret, step.args...)
		if status != cli.ExitOK || errs != "" || rows(out) != step.want {
			t.Errorf("%v exits %d, saying %q and\n%s\nwant\n%s", step.args, status, errs, rows(out), step.want)
		}
	}

	// A second-generation puck whose home Flair does not tell is listed all
	// the same; when Flair refuses that generation, the first is printed
	// and the command says what it could not read.
	api.Pucks[1].StructureID = ""

	if out, errs, status := run(t, api, flairtest.ClientSecret, "pucks"); status != cli.ExitOK || errs != "" ||
		!strings.Contains(out, "Kitchen-ae4b") {
		t.Errorf("pucks with one whose home is not told exits %d saying %q and\n%s", status, errs, out)
	}

	api.NoSecond = true

	if out, errs, status := run(t, api, flairtest.ClientSecret, "pucks"); status != cli.ExitError ||
		!strings.Contains(out, "Bedroom Puck") || strings.Contains(out, "Kitchen-ae4b") ||
		!strings.Contains(errs, "the second-generation pucks were not read: flair refused the credentials") {
		t.Errorf("pucks with the second generation refused exits %d saying %q and\n%s", status, errs, out)
	}

	// A home in Celsius reads as the API speaks.
	api.Structures[0].Scale = "C"

	if out, _, _ := run(
		t,
		api,
		flairtest.ClientSecret,
		"rooms",
	); !strings.Contains(
		rows(out),
		`"Office" 20.0C 21.1C 60%`,
	) {
		t.Errorf("rooms of a home in Celsius:\n%s", out)
	}
}

// TestActing: each command that changes something sends its one write, to
// the record its argument names, a temperature turned to Celsius, and
// says when the home's mode makes Flair ignore or undo it.
func TestActing(t *testing.T) {
	t.Parallel()

	api := started(t)

	for _, step := range []struct {
		args []string
		want string
		says string
	}{
		{
			[]string{"vent", "office vent", "100"},
			"/api/vents/" + flairtest.OfficeVentID + " map[percent-open:100]",
			"the home is in auto mode: Flair moves the vent again\n",
		},
		{[]string{"room", "Office", "72"}, "/api/rooms/8704 map[set-point-c:22.22222222222222]", ""},
		{[]string{"mode", "manual"}, "/api/structures/4351 map[mode:manual]", ""},
		{[]string{"vent", flairtest.BedVentID, "50"}, "/api/vents/" + flairtest.BedVentID + " map[percent-open:50]", ""},
		{
			[]string{"room", "8310", "68"},
			"/api/rooms/8310 map[set-point-c:20]",
			"the home is in manual mode: Flair does not act on a room's target\n",
		},
		{[]string{"away"}, "/api/structures/4351 map[home:false]", ""},
		{[]string{"home"}, "/api/structures/4351 map[home:true]", ""},
		{[]string{"heat-cool", "float"}, "/api/structures/4351 map[structure-heat-cool-mode:float]", ""},
	} {
		before := len(api.Patches)

		out, errs, status := run(t, api, flairtest.ClientSecret, step.args...)
		if status != cli.ExitOK || out != step.says || errs != "" {
			t.Fatalf("%v exits %d, saying %q and %q, want %q", step.args, status, out, errs, step.says)
		}

		sent := api.Patches[before:]
		if len(sent) != 1 || fmt.Sprint(sent[0].Path, " ", attributes(sent[0])) != step.want {
			t.Errorf("%v sent %v, want %s", step.args, sent, step.want)
		}
	}

	before := len(api.Patches)

	if _, errs, status := run(
		t,
		api,
		flairtest.ClientSecret,
		"room",
		"Office",
		"70",
		"--hold",
		"3h",
	); status != cli.ExitOK {
		t.Fatalf("room with a hold exits %d saying %q", status, errs)
	}

	held := attributes(api.Patches[before])
	if until, ok := held["hold-until"].(string); !ok || !strings.HasSuffix(until, "+00:00") || len(held) != 2 {
		t.Errorf("a room with a hold sent %v", held)
	}
}

// attributes is the attributes a write carried; none when it carried
// nothing of that shape.
func attributes(patch flairtest.Patch) map[string]any {
	data, ok := patch.Body["data"].(map[string]any)
	if !ok {
		return nil
	}

	set, ok := data["attributes"].(map[string]any)
	if !ok {
		return nil
	}

	return set
}

// TestRefusing: what the command cannot do, it says on the error stream
// and exits 1, with no write sent: credentials that are not set or that
// Flair refuses, a name no record or two records answer to, with the
// names there are, a value Flair has no meaning for. No error shows the
// secret. --help exits 0 and names the variables.
func TestRefusing(t *testing.T) {
	t.Parallel()

	api := started(t)
	api.Vents = append(api.Vents, &flairtest.Vent{ID: "twin", Name: "OFFICE VENT", StructureID: flairtest.HomeID})

	for _, step := range []struct {
		secret string
		args   []string
		want   string
	}{
		{"", []string{"rooms"}, "not set in the environment: " + cli.EnvClientSecret},
		{"another-secret", []string{"rooms"}, "flair refused the credentials"},
		{flairtest.ClientSecret, []string{"vent", "Attic", "100"}, `0 vents are "Attic"; there are "Office Vent", "Bed Vent", "OFFICE VENT"`},
		{flairtest.ClientSecret, []string{"vent", "office vent", "100"}, `2 vents are "office vent"`},
		{flairtest.ClientSecret, []string{"vent", "Bed Vent", "101"}, "a vent open at 101 percent"},
		{flairtest.ClientSecret, []string{"vent", "Bed Vent", "half"}, "half"},
		{flairtest.ClientSecret, []string{"room", "Garage", "70"}, `0 rooms are "Garage"; there are "Office", "Bedroom"`},
		{flairtest.ClientSecret, []string{"room", "Office", "70", "--hold=-1h"}, "a hold below zero"},
		{flairtest.ClientSecret, []string{"room", "Office", "700"}, "a target of 371.1 Celsius, outside 5 to 40"},
		{flairtest.ClientSecret, []string{"room", "Office", "21"}, "a target of -6.1 Celsius, outside 5 to 40"},
		{flairtest.ClientSecret, []string{"mode", "eco"}, "eco"},
		{flairtest.ClientSecret, []string{"--structure", "Cabin", "rooms"}, `0 homes are "Cabin"; there are "My Home"`},
	} {
		out, errs, status := run(t, api, step.secret, step.args...)
		if status != cli.ExitError || out != "" || !strings.Contains(errs, step.want) {
			t.Errorf("%v exits %d, saying %q and %q, want an error with %q", step.args, status, out, errs, step.want)
		}

		if strings.Contains(errs, "another-secret") || strings.Contains(errs, flairtest.ClientSecret) {
			t.Errorf("%v shows the secret: %q", step.args, errs)
		}
	}

	api.Structures = append(api.Structures, &flairtest.Structure{ID: "9", Name: "Cabin", Mode: "auto"})

	if _, errs, status := run(t, api, flairtest.ClientSecret, "away"); status != cli.ExitError ||
		!strings.Contains(errs, `there are 2 homes, name one: "My Home", "Cabin"`) {
		t.Errorf("away with two homes exits %d saying %q", status, errs)
	}

	if len(api.Patches) != 0 {
		t.Errorf("the refused commands sent %v", api.Patches)
	}

	if _, errs, status := run(t, api, flairtest.ClientSecret, "--structure", "9", "away"); status != cli.ExitOK ||
		len(api.Patches) != 1 || api.Patches[0].Path != "/api/structures/9" {
		t.Errorf("away for a home named by its id exits %d saying %q, sent %v", status, errs, api.Patches)
	}

	out, _, status := run(t, api, "", "--help")
	if status != cli.ExitOK || !strings.Contains(out, cli.EnvClientID) || !strings.Contains(out, cli.EnvClientSecret) {
		t.Errorf("--help exits %d saying %q", status, out)
	}
}
