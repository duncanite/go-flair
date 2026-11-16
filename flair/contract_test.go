package flair_test

// The contract. [PM] is Flair's documentation, a Postman collection
// (https://documenter.getpostman.com/view/5353571/TzsbKTAG); [PY] Flair's
// own Python client (https://github.com/flair-systems/flair-api-client-py);
// [HA] the Home Assistant integration
// (https://github.com/RobertD502/home-assistant-flair), where both are
// silent.
//
// The token [PM, PY]: POST /oauth2/token, a urlencoded form of client_id,
// client_secret, grant_type=client_credentials and scope, the scopes
// separated by spaces, each "<resource>.view" or "<resource>.edit"; the
// answer has access_token and expires_in, in seconds; requests carry
// "Authorization: Bearer <token>". [PY]: "approximately 50" tokens may be
// created a day.
//
// Reading [PM]: GET /api/structures, /api/rooms, /api/vents, /api/pucks,
// /api/puck2s, each a JSON:API list of {type, id, attributes,
// relationships}; a to-one relationship's data is {type, id}, its id null
// when it names none. A list's paging is in meta: "next" is the next
// page's path, or null [PM]; [PY] follows it until it is. A vent's last
// reading is GET /api/vents/:id/current-reading.
//
// Writing [PM]: PATCH /api/vents/:id with {"data": {"type": "vents",
// "attributes": {"percent-open": 100}}}, 0 closed and 100 open, held in
// manual mode alone; PATCH /api/rooms/:id with "set-point-c" and
// "hold-until", acted on in auto mode alone. [PY] sends the id in the
// body too. A structure's "mode", "home" and "structure-heat-cool-mode"
// are documented to read; the writes of them are [HA]'s.
//
// Errors [PM]: 404 with {"errors": {"title", "code", "detail"}}, one
// object. [HA] reads errors as a list on 403 and 422.

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/duncanite/go-flair/flair"
	"github.com/duncanite/go-flair/flair/flairtest"
)

func started(t *testing.T) (*flairtest.API, *flair.Client) {
	t.Helper()

	api := flairtest.New()
	t.Cleanup(api.Close)

	credentials := flair.Credentials{ClientID: flairtest.ClientID, ClientSecret: flairtest.ClientSecret}

	return api, flair.New(credentials, flair.Options{BaseURL: api.URL, HTTP: api.Client()})
}

// TestToken: one token is asked for, by the documented form, and kept for
// the requests that follow; one at its end is asked for again; a client
// Flair does not know is ErrCredentials; credentials that are not whole
// send nothing.
func TestToken(t *testing.T) {
	t.Parallel()

	api, client := started(t)
	ctx := t.Context()

	for range 3 {
		if _, err := client.Structures(ctx); err != nil {
			t.Fatalf("Structures: %v", err)
		}
	}

	want := "client_id=client-0001&grant_type=client_credentials&" +
		"scope=structures.view+structures.edit+vents.view+vents.edit+pucks.view"
	if len(api.Grants) != 1 || api.Grants[0].Encode() != want {
		t.Fatalf("three requests asked for tokens as %v, want one, as %s", api.Grants, want)
	}

	short := flair.New(
		flair.Credentials{ClientID: flairtest.ClientID, ClientSecret: flairtest.ClientSecret},
		flair.Options{BaseURL: api.URL + "/", HTTP: api.Client(), Scope: "vents.view"},
	)

	vents := func(what string, tokens int) {
		t.Helper()

		if _, err := short.Vents(ctx); err != nil {
			t.Fatalf("Vents, %s: %v", what, err)
		}

		if len(api.Grants) != tokens {
			t.Fatalf("%s: %d tokens asked for so far, want %d", what, len(api.Grants), tokens)
		}
	}

	// A token of one second is left halfway through its life.
	api.Expires = 1

	vents("under a token of one second", 2)
	vents("at once after", 2)
	time.Sleep(600 * time.Millisecond)

	// The next one's life is told as a string, and is a life all the same.
	api.Expires, api.ExpiresAsText = 3600, true

	vents("past half the token's life", 3)
	vents("under a token whose life is a string", 3)

	if api.Grants[2].Get("scope") != "vents.view" {
		t.Errorf("the scope asked for was %q", api.Grants[2].Get("scope"))
	}

	// A token whose life is not told is kept until Flair refuses it, and
	// then asked for again, once.
	api.Expires = -1
	api.Revoke()

	vents("after Flair ended the token", 4)
	vents("under a token whose life is not told", 4)
	vents("again", 4)
	api.Revoke()
	vents("after Flair ended that one", 5)

	unknown := flair.New(
		flair.Credentials{ClientID: flairtest.ClientID, ClientSecret: "another"},
		flair.Options{BaseURL: api.URL, HTTP: api.Client()},
	)
	if _, err := unknown.Rooms(ctx); !errors.Is(err, flair.ErrCredentials) ||
		!strings.Contains(err.Error(), "invalid_client") || strings.Contains(err.Error(), "another") {
		t.Errorf("Rooms for a client Flair does not know = %v, want ErrCredentials saying invalid_client", err)
	}

	before := len(api.Grants)

	for _, credentials := range []flair.Credentials{{}, {ClientID: "id"}, {ClientSecret: "secret"}} {
		half := flair.New(credentials, flair.Options{BaseURL: api.URL, HTTP: api.Client()})
		if _, err := half.Rooms(ctx); !errors.Is(err, flair.ErrArgument) {
			t.Errorf("Rooms with credentials that are not whole = %v, want ErrArgument", err)
		}
	}

	if len(api.Grants) != before {
		t.Errorf("credentials that are not whole were sent: %v", api.Grants[before:])
	}
}

// TestReading: the structures, rooms, vents and pucks, each with the
// records it belongs to; a measure Flair gave none for is nil, a
// relationship that names none is empty; a list in pages is read whole.
func TestReading(t *testing.T) {
	t.Parallel()

	api, client := started(t)
	ctx := t.Context()

	api.PageSize = 1
	api.Rooms[1].HoldUntil = "2018-12-18T02:18:02.724772+00:00"

	structures, err := client.Structures(ctx)
	if want := []flair.Structure{{
		ID: flairtest.HomeID, Name: "My Home", Mode: flair.Auto, HeatCool: flair.Cool, Home: true, Fahrenheit: true,
		TimeZone: "America/Los_Angeles",
	}}; err != nil || !slices.Equal(structures, want) {
		t.Errorf("Structures = %+v, %v\nwant %+v", structures, err, want)
	}

	rooms, err := client.Rooms(ctx)
	if want := []flair.Room{
		{
			ID: flairtest.OfficeID, Name: "Office", StructureID: flairtest.HomeID, TemperatureC: new(20.0),
			SetPointC: new(21.12), Humidity: new(60.0), Active: true,
		},
		{
			ID: flairtest.BedroomID, Name: "Bedroom", StructureID: flairtest.HomeID,
			HoldUntil: time.Date(2018, time.December, 18, 2, 18, 2, 724772000, time.UTC),
		},
	}; err != nil || len(rooms) != 2 || !reflect.DeepEqual(rooms[0], want[0]) ||
		rooms[1].TemperatureC != nil || rooms[1].Active || !rooms[1].HoldUntil.Equal(want[1].HoldUntil) {
		t.Errorf("Rooms = %+v, %v\nwant %+v", rooms, err, want)
	}

	vents, err := client.Vents(ctx)
	if want := []flair.Vent{
		{ID: flairtest.OfficeVentID, Name: "Office Vent", RoomID: flairtest.OfficeID, StructureID: flairtest.HomeID},
		{
			ID: flairtest.BedVentID, Name: "Bed Vent", RoomID: flairtest.BedroomID, StructureID: flairtest.HomeID,
			PercentOpen: 100, Inactive: true,
		},
	}; err != nil || !slices.Equal(vents, want) {
		t.Errorf("Vents = %+v, %v\nwant %+v", vents, err, want)
	}

	pucks, err := client.Pucks(ctx)
	if want := []flair.Puck{{
		ID: flairtest.PuckID, Name: "Bedroom Puck", RoomID: flairtest.BedroomID, StructureID: flairtest.HomeID,
		TemperatureC: new(20.4766666666667), Humidity: new(61.0), Gateway: true,
	}}; err != nil || !reflect.DeepEqual(pucks, want) {
		t.Errorf("Pucks = %+v, %v\nwant %+v", pucks, err, want)
	}

	// A second-generation puck's home and room are links, followed: this
	// one's home answers a record, its room null.
	seconds, err := client.Puck2s(ctx)
	if want := []flair.Puck{{
		ID: flairtest.Puck2ID, Name: "Kitchen-ae4b", StructureID: flairtest.HomeID, Second: true,
		TemperatureC: new(22.57),
	}}; err != nil || !reflect.DeepEqual(seconds, want) {
		t.Errorf("Puck2s = %+v, %v\nwant %+v", seconds, err, want)
	}

	reads := []string{
		"/api/structures",
		"/api/rooms", "/api/rooms?page%5Bsize%5D=1&page%5Bpage%5D=2",
		"/api/vents", "/api/vents?page%5Bsize%5D=1&page%5Bpage%5D=2",
		"/api/pucks",
		"/api/puck2s", "/api/puck2s/" + flairtest.Puck2ID + "/room", "/api/puck2s/" + flairtest.Puck2ID + "/structure",
	}
	if !slices.Equal(api.Reads, reads) {
		t.Errorf("the lists were read as %v, want %v", api.Reads, reads)
	}

	api.Pucks[1].RoomID = flairtest.OfficeID

	if seconds, err = client.Puck2s(ctx); err != nil || len(seconds) != 1 || seconds[0].RoomID != flairtest.OfficeID {
		t.Errorf("Puck2s of a puck in a room = %+v, %v", seconds, err)
	}

	reading, err := client.VentReading(ctx, flairtest.BedVentID)
	if want := (flair.VentReading{
		At:               time.Date(2019, time.September, 26, 23, 31, 18, 133106000, time.UTC),
		DuctTemperatureC: new(26.08), DuctPressure: new(101.2985),
		Voltage: new(3.02), RSSI: new(-81.0), PercentOpen: 100,
	}); err != nil || !reading.At.Equal(want.At) || *reading.DuctTemperatureC != 26.08 ||
		*reading.DuctPressure != 101.2985 || *reading.Voltage != 3.02 || *reading.RSSI != -81 ||
		reading.PercentOpen != 100 {
		t.Errorf("VentReading = %+v, %v\nwant %+v", reading, err, want)
	}
}

// TestWriting: each write is one PATCH on its record, the type, the id
// and the one attribute in its body, a hold as the documentation writes a
// time.
func TestWriting(t *testing.T) {
	t.Parallel()

	api, client := started(t)
	ctx := t.Context()

	until := time.Date(2018, time.December, 17, 15, 42, 49, 311698000, time.FixedZone("PST", -8*3600))

	for _, step := range []struct {
		do   func() error
		want string
	}{
		{
			func() error { return client.SetVent(ctx, flairtest.OfficeVentID, 100) },
			"/api/vents/" + flairtest.OfficeVentID + " map[data:map[attributes:map[percent-open:100] id:" +
				flairtest.OfficeVentID + " type:vents]]",
		},
		{
			func() error { return client.SetVent(ctx, flairtest.BedVentID, 0) },
			"/api/vents/" + flairtest.BedVentID + " map[data:map[attributes:map[percent-open:0] id:" +
				flairtest.BedVentID + " type:vents]]",
		},
		{
			func() error { return client.SetRoom(ctx, flairtest.BedroomID, 24, until) },
			"/api/rooms/8310 map[data:map[attributes:map[hold-until:2018-12-17T23:42:49.311698+00:00 " +
				"set-point-c:24] id:8310 type:rooms]]",
		},
		{
			func() error { return client.SetRoom(ctx, flairtest.OfficeID, 19.5, time.Time{}) },
			"/api/rooms/8704 map[data:map[attributes:map[set-point-c:19.5] id:8704 type:rooms]]",
		},
		{
			func() error { return client.SetMode(ctx, flairtest.HomeID, flair.Manual) },
			"/api/structures/4351 map[data:map[attributes:map[mode:manual] id:4351 type:structures]]",
		},
		{
			func() error { return client.SetHome(ctx, flairtest.HomeID, false) },
			"/api/structures/4351 map[data:map[attributes:map[home:false] id:4351 type:structures]]",
		},
		{
			func() error { return client.SetHeatCool(ctx, flairtest.HomeID, flair.Float) },
			"/api/structures/4351 map[data:map[attributes:map[structure-heat-cool-mode:float] id:4351 " +
				"type:structures]]",
		},
	} {
		before := len(api.Patches)

		if err := step.do(); err != nil {
			t.Fatalf("the write for %s: %v", step.want, err)
		}

		sent := api.Patches[before:]
		if len(sent) != 1 || fmt.Sprint(sent[0].Path, " ", sent[0].Body) != step.want {
			t.Errorf("sent %v\nwant %s", sent, step.want)
		}
	}

	home, bedroom := api.Structures[0], api.Rooms[1]
	if api.Vents[0].PercentOpen != 100 || api.Vents[1].PercentOpen != 0 || *bedroom.SetPoint != 24 ||
		bedroom.HoldUntil != "2018-12-17T23:42:49.311698+00:00" || home.Mode != "manual" || home.Home ||
		home.HeatCool != "float" {
		t.Errorf("after the writes: vents %+v %+v, the bedroom %+v, the home %+v",
			api.Vents[0], api.Vents[1], bedroom, home)
	}
}

// TestArguments: an id that is empty and a value Flair has no meaning for
// are ErrArgument, with nothing sent, not even for a token.
func TestArguments(t *testing.T) {
	t.Parallel()

	api, client := started(t)
	ctx := t.Context()

	for name, do := range map[string]func() error{
		"a vent below closed":      func() error { return client.SetVent(ctx, flairtest.BedVentID, -1) },
		"a vent beyond open":       func() error { return client.SetVent(ctx, flairtest.BedVentID, 101) },
		"no vent":                  func() error { return client.SetVent(ctx, "", 100) },
		"a target that is nothing": func() error { return client.SetRoom(ctx, flairtest.OfficeID, math.NaN(), time.Time{}) },
		"a target in Fahrenheit":   func() error { return client.SetRoom(ctx, flairtest.OfficeID, 70, time.Time{}) },
		"a target below any room":  func() error { return client.SetRoom(ctx, flairtest.OfficeID, 4.9, time.Time{}) },
		"a target above any room":  func() error { return client.SetRoom(ctx, flairtest.OfficeID, 40.1, time.Time{}) },
		"no room":                  func() error { return client.SetRoom(ctx, "", 20, time.Time{}) },
		"a mode that is none":      func() error { return client.SetMode(ctx, flairtest.HomeID, "eco") },
		"a heat mode that is none": func() error { return client.SetHeatCool(ctx, flairtest.HomeID, "warm") },
		"no structure":             func() error { return client.SetHome(ctx, "", true) },
		"no vent to read": func() error {
			_, err := client.VentReading(ctx, "")

			return err
		},
	} {
		if err := do(); !errors.Is(err, flair.ErrArgument) {
			t.Errorf("%s = %v, want ErrArgument", name, err)
		}
	}

	if len(api.Grants)+len(api.Patches)+len(api.Reads) != 0 {
		t.Errorf("the refused arguments sent %v, %v, %v", api.Grants, api.Patches, api.Reads)
	}
}

// TestRefusals: each answer that is not a success is the error of its
// class, saying what Flair said, its errors one object or a list; an
// answer that is not the documented JSON, too large, or whose next page
// is not a path of the API, is ErrAnswer.
func TestRefusals(t *testing.T) {
	t.Parallel()

	api, client := started(t)
	ctx := t.Context()

	if _, err := client.VentReading(ctx, "no-such-vent"); !errors.Is(err, flair.ErrNotFound) ||
		!strings.Contains(err.Error(), "Not Found /api/vents/no-such-vent/current-reading was not found") {
		t.Errorf("VentReading of a vent that is none = %v, want ErrNotFound with Flair's words", err)
	}

	if err := client.SetVent(ctx, "no-such-vent", 0); !errors.Is(err, flair.ErrRefused) ||
		!strings.Contains(err.Error(), "cannot set percent-open") {
		t.Errorf("SetVent of a vent that is none = %v, want ErrRefused with Flair's words", err)
	}

	// An id goes into a path escaped.
	if err := client.SetVent(ctx, "a/b?c", 0); !errors.Is(err, flair.ErrRefused) ||
		api.Patches[len(api.Patches)-1].Path != "/api/vents/a%2Fb%3Fc" {
		t.Errorf("SetVent of an id with a slash in it = %v, sent to %s", err, api.Patches[len(api.Patches)-1].Path)
	}

	// Any success is one: the documentation shows 200.
	api.Status, api.Body = http.StatusCreated, ``

	if err := client.SetVent(ctx, flairtest.BedVentID, 0); err != nil {
		t.Errorf("SetVent answered 201 = %v", err)
	}

	// A link that leads off the API is not followed.
	api.Status, api.Body = http.StatusOK, `{"data": [{"type": "puck2s", "id": "p", "attributes": {}, `+
		`"relationships": {"room": {"links": {"related": "https://elsewhere.example/api/rooms/1"}}}}]}`

	if _, err := client.Puck2s(
		ctx,
	); !errors.Is(err, flair.ErrAnswer) ||
		!strings.Contains(err.Error(), "no path of the API") {
		t.Errorf("Puck2s with a room elsewhere = %v, want ErrAnswer", err)
	}

	for _, answer := range []struct {
		status int
		body   string
		want   error
		says   string
	}{
		{http.StatusForbidden, `{"errors": [{"title": "Forbidden", "detail": "no scope"}]}`, flair.ErrCredentials, "Forbidden no scope"},
		{http.StatusUnauthorized, ``, flair.ErrCredentials, "HTTP 401"},
		{http.StatusInternalServerError, `<html>`, flair.ErrRefused, "HTTP 500"},
		{http.StatusOK, `<html>`, flair.ErrAnswer, "GET /api/vents"},
		{http.StatusOK, `{"data": {"not": "a list"}}`, flair.ErrAnswer, "GET /api/vents"},
		{http.StatusOK, `{"data": [{"type": "vents", "id": "v", "attributes": "none"}]}`, flair.ErrAnswer, "the attributes of vents v"},
		{http.StatusOK, strings.Repeat(" ", 16<<20+1), flair.ErrAnswer, "over 16777216 bytes"},
		{http.StatusOK, `{"data": [], "meta": {"next": "https://elsewhere.example/api/vents"}}`, flair.ErrAnswer, "no path of the API"},
		{http.StatusOK, `{"data": [], "meta": {"next": "//elsewhere.example/api/vents"}}`, flair.ErrAnswer, "no path of the API"},
		{http.StatusOK, `{"data": [], "meta": {"next": "/api/vents"}}`, flair.ErrAnswer, "over 100 pages"},
	} {
		api.Status, api.Body = answer.status, answer.body

		_, err := client.Vents(ctx)
		if !errors.Is(err, answer.want) || !strings.Contains(err.Error(), answer.says) {
			t.Errorf(
				"Vents answered %d %.40q = %v, want %v saying %q",
				answer.status,
				answer.body,
				err,
				answer.want,
				answer.says,
			)
		}
	}
}

// holder keeps a Client where fmt cannot call its Format.
type holder struct {
	byValue   flair.Client
	byPointer *flair.Client
}

// TestTheSecretsStayIn: a Client, and the Credentials given to it,
// formatted under any verb show neither the id nor the secret, by value,
// by pointer or as JSON; inside another struct the Client still shows
// nothing.
func TestTheSecretsStayIn(t *testing.T) {
	t.Parallel()

	credentials := flair.Credentials{ClientID: "id-f3a9", ClientSecret: "secret-7c21"}
	client := flair.New(credentials, flair.Options{})

	shows := func(printed string) bool {
		return strings.Contains(printed, "f3a9") || strings.Contains(printed, "7c21") ||
			strings.Contains(printed, fmt.Sprintf("%x", "f3a9")) || strings.Contains(printed, fmt.Sprintf("%x", "7c21"))
	}

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
		for name, value := range map[string]any{
			"a client's pointer": client, "a client": *client, "credentials": credentials,
			"credentials' pointer": &credentials, "a holder": holder{byValue: *client, byPointer: client},
		} {
			if printed := fmt.Sprintf(verb, value); shows(printed) {
				t.Errorf("%s under %s shows a secret: %s", name, verb, printed)
			}
		}
	}

	if marshalled, err := credentials.MarshalJSON(); err != nil || shows(string(marshalled)) {
		t.Errorf("the credentials as JSON = %s, %v", marshalled, err)
	}

	if logged := credentials.LogValue().String(); shows(logged) {
		t.Errorf("the credentials in a log record = %s", logged)
	}
}
