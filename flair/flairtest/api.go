// Package flairtest is Flair's API for tests: an HTTP server with the
// token route and the routes this module calls, answering in the shapes
// the documentation's examples show, over a home a test reads and sets.
// It records every token asked for and every write.
//
// The second-generation puck is written as the documentation writes one:
// its room and its home as links, which the fake answers, with null for a
// puck in no room.
//
// Where the documentation is silent the fake chooses: a client it does
// not know is refused a token with 401 and OAuth2's invalid_client, a
// request under another token 401, an attribute it does not know 422 with
// its errors as a list, and a write whose type or id is not the path's
// 422 too.
package flairtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// The API client the fake knows, and the records it starts with.
const (
	ClientID     = "client-0001"
	ClientSecret = "secret-0001" // #nosec G101 -- the fake's own secret, of no account
	HomeID       = "4351"
	OfficeID     = "8704"
	BedroomID    = "8310"
	OfficeVentID = "13c44351-5883-5cbe-52cf-da0c1a1505f7"
	BedVentID    = "53d69ae0-59d4-5de0-4a86-4d468cacda63"
	PuckID       = "46eb5eef-7324-5748-2384-3f9ff8996373"
	Puck2ID      = "ae4bfdb0-acc3-4b0b-8cb4-7ce6badde4a3"
)

// The types of records.
const (
	structures = "structures"
	rooms      = "rooms"
	vents      = "vents"
	// inStructure and inRoom are the relationships of a record to its home
	// and to its room.
	inStructure = "structure"
	inRoom      = "room"
	// secondGeneration is the type of the second generation's pucks.
	secondGeneration = "puck2s"
)

const (
	maxBody = 1 << 20
	// defaultPage is the page size of the documentation's every example.
	defaultPage = 50
	// lifetime is the expires_in of the documentation's every example.
	lifetime = 3600
)

// Structure is a home the fake holds; a test changes it between requests.
type Structure struct {
	ID, Name, Mode, HeatCool, Scale, TimeZone string
	Home                                      bool
}

// Room is a room the fake holds.
type Room struct {
	ID, Name, StructureID           string
	Temperature, SetPoint, Humidity *float64
	Active                          bool
	// HoldUntil is written as the API writes a time; empty is null.
	HoldUntil string
}

// Vent is a vent the fake holds.
type Vent struct {
	ID, Name, RoomID, StructureID string
	PercentOpen                   int
	Inactive                      bool
}

// Puck is a puck the fake holds, of either generation.
type Puck struct {
	// RoomID empty is a relationship whose id is null, as the
	// documentation shows one.
	ID, Name, RoomID, StructureID string
	Second                        bool
	Temperature, Humidity         *float64
	Inactive, Gateway             bool
}

// Patch is one write the fake received: its path and its body as decoded
// JSON.
type Patch struct {
	Path string
	Body map[string]any
}

// API is the fake. A test sets its exported fields between requests; the
// server holds its lock around them.
type API struct {
	*httptest.Server

	mu         sync.Mutex
	Structures []*Structure
	Rooms      []*Room
	Vents      []*Vent
	Pucks      []*Puck
	// NoSecond refuses the second-generation pucks with a 403, as a token
	// without their scope would be.
	NoSecond bool
	// PageSize is how many records a page holds when the request names no
	// size; zero is the documentation's fifty.
	PageSize int
	// Expires is the lifetime of the tokens given, in seconds; zero is the
	// documentation's 3600, and below zero the answer tells none.
	// ExpiresAsText writes it as a string.
	Expires       int
	ExpiresAsText bool
	// Status, when not zero, is the status every request but the token's
	// is answered with, and Body its body.
	Status int
	Body   string
	// Grants is the form of every token request; Reads every GET, with
	// its query; Patches every write.
	Grants  []url.Values
	Reads   []string
	Patches []Patch

	token string
}

// New starts the fake with one home in Fahrenheit and auto mode, two
// rooms, a vent in each, a first-generation puck that is the gateway and
// a second-generation one in no room. The caller closes it.
func New() *API {
	//nolint:mnd // the home the fake starts with: the values of the documentation's examples
	api := &API{
		Structures: []*Structure{{
			ID: HomeID, Name: "My Home", Mode: "auto", HeatCool: "cool", Scale: "F",
			TimeZone: "America/Los_Angeles", Home: true,
		}},
		Rooms: []*Room{
			{
				ID: OfficeID, Name: "Office", StructureID: HomeID, Temperature: new(20.0), SetPoint: new(21.12),
				Humidity: new(60.0), Active: true,
			},
			{ID: BedroomID, Name: "Bedroom", StructureID: HomeID},
		},
		Vents: []*Vent{
			{ID: OfficeVentID, Name: "Office Vent", RoomID: OfficeID, StructureID: HomeID},
			{ID: BedVentID, Name: "Bed Vent", RoomID: BedroomID, StructureID: HomeID, PercentOpen: 100, Inactive: true},
		},
		Pucks: []*Puck{
			{
				ID: PuckID, Name: "Bedroom Puck", RoomID: BedroomID, StructureID: HomeID,
				Temperature: new(20.4766666666667), Humidity: new(61.0), Gateway: true,
			},
			{ID: Puck2ID, Name: "Kitchen-ae4b", StructureID: HomeID, Second: true, Temperature: new(22.57)},
		},
	}
	api.Server = httptest.NewServer(http.HandlerFunc(api.serve))

	return api
}

// Revoke makes the token given no token any more, as Flair does when one
// ends.
func (a *API) Revoke() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.token = ""
}

// The documents the fake writes.
type (
	document struct {
		Data any  `json:"data"`
		Meta meta `json:"meta"`
	}
	meta struct {
		Self string  `json:"self"`
		Next *string `json:"next"`
	}
	resource struct {
		Type          string                  `json:"type"`
		ID            string                  `json:"id"`
		Attributes    any                     `json:"attributes"`
		Relationships map[string]relationship `json:"relationships,omitempty"`
	}
	// relationship is written with the other record in data, or, as the
	// documentation writes a second-generation puck's, with a link alone.
	relationship struct {
		Data  *identifier `json:"data,omitempty"`
		Links *links      `json:"links,omitempty"`
	}
	links struct {
		Related string `json:"related"`
	}
	identifier struct {
		Type string  `json:"type"`
		ID   *string `json:"id"`
	}
	structureAttributes struct {
		Name     string `json:"name"`
		Mode     string `json:"mode"`
		HeatCool string `json:"structure-heat-cool-mode"`
		Home     bool   `json:"home"`
		Scale    string `json:"temperature-scale"`
		TimeZone string `json:"time-zone"`
	}
	roomAttributes struct {
		Name        string   `json:"name"`
		Temperature *float64 `json:"current-temperature-c"`
		SetPoint    *float64 `json:"set-point-c"`
		Humidity    *float64 `json:"current-humidity"`
		Active      bool     `json:"active"`
		HoldUntil   *string  `json:"hold-until"`
	}
	ventAttributes struct {
		Name        string `json:"name"`
		PercentOpen int    `json:"percent-open"`
		Inactive    bool   `json:"inactive"`
	}
	puckAttributes struct {
		Name        string   `json:"name"`
		Temperature *float64 `json:"current-temperature-c"`
		Humidity    *float64 `json:"current-humidity"`
		Inactive    bool     `json:"inactive"`
		Gateway     bool     `json:"is-gateway"`
	}
	readingAttributes struct {
		CreatedAt   string  `json:"created-at"`
		Temperature float64 `json:"duct-temperature-c"`
		Pressure    float64 `json:"duct-pressure"`
		Voltage     float64 `json:"system-voltage"`
		RSSI        int     `json:"rssi"`
		PercentOpen int     `json:"percent-open"`
	}
	// problem is the error the documentation shows: one object, its code a
	// string.
	problem struct {
		Title  string `json:"title"`
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
)

func (a *API) serve(writer http.ResponseWriter, req *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	writer.Header().Set("Content-Type", "application/json; charset=utf-8")

	status, answer := a.route(req)

	if a.Status != 0 && req.URL.Path != "/oauth2/token" {
		writer.WriteHeader(a.Status)
		_, _ = io.WriteString(writer, a.Body)

		return
	}

	writer.WriteHeader(status)

	if answer != nil {
		_ = json.NewEncoder(writer).Encode(answer)
	}
}

func refused(status int, title, detail string) (int, any) {
	return status, map[string]problem{"errors": {Title: title, Code: strconv.Itoa(status), Detail: detail}}
}

func (a *API) route(req *http.Request) (int, any) {
	if req.URL.Path == "/oauth2/token" {
		return a.grant(req)
	}

	if a.token == "" || req.Header.Get("Authorization") != "Bearer "+a.token {
		return refused(http.StatusUnauthorized, "Unauthorized", "no such token")
	}

	if req.Method == http.MethodPatch {
		return a.patch(req)
	}

	a.Reads = append(a.Reads, req.URL.RequestURI())

	if a.NoSecond && strings.HasPrefix(req.URL.Path, "/api/"+secondGeneration) {
		return listed(http.StatusForbidden, "Forbidden", "no scope for "+secondGeneration)
	}

	return a.read(req)
}

// read is the routes that read: a list, a second-generation puck's link,
// a vent's reading.
func (a *API) read(req *http.Request) (int, any) {
	parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")

	const collection, measured = 2, 4

	switch {
	case len(parts) == collection && parts[0] == "api":
		if records, known := a.records(parts[1]); known {
			return a.page(req, records)
		}
	case len(parts) == measured && parts[1] == secondGeneration:
		if answer, known := a.placed(parts[2], parts[3]); known {
			return http.StatusOK, document{Data: answer, Meta: meta{Self: req.URL.Path}}
		}
	case len(parts) == measured && parts[1] == vents && parts[3] == "current-reading":
		for _, vent := range a.Vents {
			if vent.ID == parts[2] {
				return http.StatusOK, document{Data: reading(vent), Meta: meta{Self: req.URL.Path}}
			}
		}
	}

	return refused(http.StatusNotFound, "Not Found", req.URL.Path+" was not found")
}

// reading is what a vent last measured: the values of the documentation's
// example, and the vent's own opening.
func reading(vent *Vent) resource {
	//nolint:mnd // the values of the documentation's example
	return resource{Type: "vent-sensor-readings", ID: "reading-" + vent.ID, Attributes: readingAttributes{
		CreatedAt: "2019-09-26T23:31:18.133106+00:00", Temperature: 26.08, Pressure: 101.2985, Voltage: 3.02,
		RSSI: -81, PercentOpen: vent.PercentOpen,
	}}
}

// placed is what a second-generation puck's link to its room or its home
// answers: the record, or null when it has none; and whether there is
// such a puck and such a link.
func (a *API) placed(puck, tie string) (any, bool) {
	for _, one := range a.Pucks {
		if !one.Second || one.ID != puck {
			continue
		}

		switch {
		case tie == inStructure && one.StructureID != "":
			return resource{Type: structures, ID: one.StructureID}, true
		case tie == inRoom && one.RoomID != "":
			return resource{Type: rooms, ID: one.RoomID}, true
		case tie == inStructure || tie == inRoom:
			return nil, true
		}
	}

	return nil, false
}

// grant is the token route: a form with the client's id and secret, the
// grant's type and the scopes.
func (a *API) grant(req *http.Request) (int, any) {
	raw, _ := io.ReadAll(io.LimitReader(req.Body, maxBody))

	form, err := url.ParseQuery(string(raw))
	if err != nil || req.Method != http.MethodPost ||
		req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		return http.StatusBadRequest, map[string]string{"error": "invalid_request"}
	}

	// The secret is not kept, so that a test may print what was asked.
	asked := url.Values{}

	for name, values := range form {
		if name != "client_secret" {
			asked[name] = values
		}
	}

	a.Grants = append(a.Grants, asked)

	if form.Get("client_id") != ClientID || form.Get("client_secret") != ClientSecret ||
		form.Get("grant_type") != "client_credentials" {
		return http.StatusUnauthorized, map[string]string{"error": "invalid_client"}
	}

	a.token = fmt.Sprintf("token-%d", len(a.Grants))

	answer := map[string]any{"access_token": a.token, "token_type": "Bearer", "scope": form.Get("scope")}

	switch {
	case a.Expires == 0:
		answer["expires_in"] = lifetime
	case a.Expires > 0 && a.ExpiresAsText:
		answer["expires_in"] = strconv.Itoa(a.Expires)
	case a.Expires > 0:
		answer["expires_in"] = a.Expires
	}

	return http.StatusOK, answer
}

func to(kind, id string) relationship {
	if id == "" {
		return relationship{Data: &identifier{Type: kind}}
	}

	return relationship{Data: &identifier{Type: kind, ID: &id}}
}

// linked is a relationship as a link alone.
func linked(path string) relationship {
	return relationship{Links: &links{Related: path}}
}

// records is a collection as resources, and whether there is one of that
// name.
func (a *API) records(kind string) ([]resource, bool) {
	records := []resource{}

	switch kind {
	case structures:
		for _, one := range a.Structures {
			records = append(records, resource{Type: kind, ID: one.ID, Attributes: structureAttributes{
				Name: one.Name, Mode: one.Mode, HeatCool: one.HeatCool, Home: one.Home, Scale: one.Scale,
				TimeZone: one.TimeZone,
			}})
		}
	case rooms:
		for _, one := range a.Rooms {
			attributes := roomAttributes{
				Name: one.Name, Temperature: one.Temperature, SetPoint: one.SetPoint, Humidity: one.Humidity,
				Active: one.Active,
			}

			if one.HoldUntil != "" {
				attributes.HoldUntil = &one.HoldUntil
			}

			records = append(records, resource{
				Type: kind, ID: one.ID, Attributes: attributes,
				Relationships: map[string]relationship{inStructure: to(structures, one.StructureID)},
			})
		}
	case vents:
		for _, one := range a.Vents {
			records = append(records, resource{
				Type: kind, ID: one.ID,
				Attributes: ventAttributes{Name: one.Name, PercentOpen: one.PercentOpen, Inactive: one.Inactive},
				Relationships: map[string]relationship{
					inStructure: to(structures, one.StructureID), inRoom: to(rooms, one.RoomID),
				},
			})
		}
	case "pucks", secondGeneration:
		return a.pucks(kind), true
	default:
		return nil, false
	}

	return records, true
}

// pucks is the pucks of one generation as resources: the first's with
// their room and home in data, the second's with links alone.
func (a *API) pucks(kind string) []resource {
	records := []resource{}

	for _, one := range a.Pucks {
		if one.Second != (kind == secondGeneration) {
			continue
		}

		placed := map[string]relationship{
			inStructure: to(structures, one.StructureID), inRoom: to(rooms, one.RoomID),
		}

		if one.Second {
			placed = map[string]relationship{
				inStructure: linked("/api/" + kind + "/" + one.ID + "/" + inStructure),
				inRoom:      linked("/api/" + kind + "/" + one.ID + "/" + inRoom),
			}
		}

		records = append(records, resource{
			Type: kind, ID: one.ID,
			Attributes: puckAttributes{
				Name: one.Name, Temperature: one.Temperature, Humidity: one.Humidity, Inactive: one.Inactive,
				Gateway: one.Gateway,
			},
			Relationships: placed,
		})
	}

	return records
}

// page is one page of a collection, its paging in meta as the
// documentation's examples have it, the brackets percent-encoded.
func (a *API) page(req *http.Request, records []resource) (int, any) {
	size := a.PageSize
	if size == 0 {
		size = defaultPage
	}

	if asked, err := strconv.Atoi(req.URL.Query().Get("page[size]")); err == nil && asked > 0 {
		size = asked
	}

	number := 1
	if asked, err := strconv.Atoi(req.URL.Query().Get("page[page]")); err == nil && asked > 0 {
		number = asked
	}

	start := min((number-1)*size, len(records))
	end := min(start+size, len(records))

	paging := meta{Self: req.URL.Path}

	if end < len(records) {
		next := fmt.Sprintf("%s?page%%5Bsize%%5D=%d&page%%5Bpage%%5D=%d", req.URL.Path, size, number+1)
		paging.Next = &next
	}

	return http.StatusOK, document{Data: records[start:end], Meta: paging}
}

// The body of a write.
type (
	change struct {
		Data changed `json:"data"`
	}
	changed struct {
		Type       string         `json:"type"`
		ID         string         `json:"id"`
		Attributes map[string]any `json:"attributes"`
	}
)

// patch applies a write: the attributes this module sets, on the record
// the path names.
func (a *API) patch(req *http.Request) (int, any) {
	raw, _ := io.ReadAll(io.LimitReader(req.Body, maxBody))

	var (
		body  map[string]any
		asked change
	)

	if json.Unmarshal(raw, &body) != nil || json.Unmarshal(raw, &asked) != nil {
		return refused(http.StatusBadRequest, "Bad Request", "not JSON")
	}

	// The path as it was sent, so that a test sees an id's escaping.
	a.Patches = append(a.Patches, Patch{Path: req.URL.EscapedPath(), Body: body})

	if req.URL.Path != "/api/"+asked.Data.Type+"/"+asked.Data.ID || len(asked.Data.Attributes) == 0 {
		return unprocessable("the body is not for " + req.URL.Path)
	}

	for name, value := range asked.Data.Attributes {
		if !a.set(asked.Data.Type, asked.Data.ID, name, value) {
			return unprocessable("cannot set " + name + " on " + req.URL.Path)
		}
	}

	return http.StatusOK, document{
		Data: resource{Type: asked.Data.Type, ID: asked.Data.ID},
		Meta: meta{Self: req.URL.Path},
	}
}

// listed is a refusal with its errors as a list, the shape the Home
// Assistant integration reads.
func listed(status int, title, detail string) (int, any) {
	return status, map[string][]problem{"errors": {{Title: title, Code: strconv.Itoa(status), Detail: detail}}}
}

// unprocessable is a 422, its errors a list.
func unprocessable(detail string) (int, any) {
	return listed(http.StatusUnprocessableEntity, "Unprocessable Entity", detail)
}

// set applies one attribute of a write, and says whether there is such a
// record with such an attribute of that type.
func (a *API) set(kind, record, name string, value any) bool {
	switch kind {
	case vents:
		return a.setVent(record, name, value)
	case rooms:
		return a.setRoom(record, name, value)
	case structures:
		return a.setStructure(record, name, value)
	default:
		return false
	}
}

func (a *API) setVent(id, name string, value any) bool {
	percent, numeric := value.(float64)

	for _, one := range a.Vents {
		if one.ID == id && name == "percent-open" && numeric {
			one.PercentOpen = int(percent)

			return true
		}
	}

	return false
}

func (a *API) setRoom(id, name string, value any) bool {
	for _, one := range a.Rooms {
		if one.ID != id {
			continue
		}

		switch set := value.(type) {
		case float64:
			if name == "set-point-c" {
				one.SetPoint = &set

				return true
			}
		case string:
			if name == "hold-until" {
				one.HoldUntil = set

				return true
			}
		}
	}

	return false
}

func (a *API) setStructure(id, name string, value any) bool {
	for _, one := range a.Structures {
		if one.ID != id {
			continue
		}

		switch set := value.(type) {
		case string:
			switch name {
			case "mode":
				one.Mode = set

				return true
			case "structure-heat-cool-mode":
				one.HeatCool = set

				return true
			}
		case bool:
			if name == "home" {
				one.Home = set

				return true
			}
		}
	}

	return false
}
