// Package flair is a client of Flair's cloud API, the one interface to a
// Flair system of vents and pucks: the structures, their rooms, vents and
// pucks with what they measure, and the writes that open a vent, set a
// room's target and change a structure's mode. It is JSON:API over HTTPS
// under an OAuth2 client-credentials token.
//
// The contract is Flair's documentation, a Postman collection, and
// Flair's own Python client; where both are silent, the Home Assistant
// integration. docs/design.md says which rule comes from where.
package flair

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BaseURL is where Flair's documentation puts the API.
const BaseURL = "https://api.flair.co"

// DefaultScope is what a token is asked for when Options names no scope:
// reading and writing structures, which rooms follow, and vents, and
// reading pucks. The documentation gives the rule the names follow,
// "<resource>.view" and "<resource>.edit", and spells out three of them.
const DefaultScope = "structures.view structures.edit vents.view vents.edit pucks.view"

const (
	// maxAnswer bounds one answer read; a page of fifty structures is some
	// tens of kilobytes.
	maxAnswer = 16 << 20
	// renewAhead is how long before its end a token is no longer used.
	renewAhead = time.Minute
	// maxPages bounds the pages followed for one list.
	maxPages = 100
)

// The errors a Client hands back, to test with errors.Is. Each carries
// what Flair said when it said something.
var (
	// ErrCredentials: Flair refused the client or the token, or the token
	// has not the scope: the token route's refusal, a 401, a 403.
	ErrCredentials = errors.New("flair refused the credentials")
	// ErrNotFound: Flair answered 404.
	ErrNotFound = errors.New("flair has no such record")
	// ErrRefused: any other answer that is not a success.
	ErrRefused = errors.New("flair refused the request")
	// ErrAnswer: the answer is not in the shape the documentation shows,
	// or is larger than an answer may be.
	ErrAnswer = errors.New("flair's answer is not in the expected shape")
	// ErrArgument: an id or a value that cannot be sent; nothing was.
	ErrArgument = errors.New("not a valid argument")
)

// Credentials is the API client the Flair app makes, under its developer
// settings. Both are secrets. A Credentials formatted, logged or
// marshalled as JSON shows neither; one kept in an unexported field of
// another struct is out of reach of these methods, and fmt prints it as it
// is.
type Credentials struct {
	ClientID     string
	ClientSecret string
}

// redacted is all a Credentials shows of itself.
const redacted = "flair credentials"

// Format keeps the credentials out of a formatted value, whatever the
// verb.
func (Credentials) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, redacted) }

// LogValue keeps them out of a log record.
func (Credentials) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON keeps them out of JSON.
func (Credentials) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// Options shape a Client. The zero value talks to BaseURL through
// http.DefaultClient, under DefaultScope.
type Options struct {
	// BaseURL is where the API is; empty is BaseURL.
	BaseURL string
	// HTTP is the client every request goes through; nil is
	// http.DefaultClient.
	HTTP *http.Client
	// Scope is the scopes the token is asked for, separated by spaces;
	// empty is DefaultScope.
	Scope string
	// Token is a token to start from, one an earlier Client's Token method
	// gave; the zero value is none. It is sent until its end, or until
	// Flair refuses it, and only then is another asked for.
	Token Token
}

// Token is an access token Flair gave, and when it ends. It is a secret:
// formatted or logged, it shows nothing of itself. Its fields are plain
// and it has no marshaller, so that the program that keeps one between
// runs can store it; and so a Token marshalled shows itself, which is what
// a struct holding one does when it is logged as JSON. One kept in an
// unexported field of another struct is out of reach of these methods
// too, and fmt prints it as it is.
//
// A token carries no scope. One kept under some scopes and given to a
// Client that names others is sent as it is until its end: the program
// that changes its scopes drops the token it kept.
type Token struct {
	// Access is the token itself.
	Access string
	// Expires is when it ends; zero when Flair did not say.
	Expires time.Time
}

// Format keeps the token out of a formatted value, whatever the verb.
func (Token) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, "flair token") }

// LogValue keeps it out of a log record.
func (Token) LogValue() slog.Value { return slog.StringValue("flair token") }

// Client is one API client's access. Its methods may be called from
// several goroutines. A Client formatted shows nothing of what it holds.
//
//nolint:recvcheck // Format alone has a value receiver, so that a Client held by value is formatted by it too
type Client struct {
	base  string
	http  *http.Client
	scope string
	// Behind a func, not a pointer: where fmt cannot call Format, a Client
	// in another struct's unexported field, it prints through a pointer
	// for a verb it does not know, and never calls a func.
	held func() *secrets
}

// Format keeps what a Client holds out of a formatted value, whatever
// the verb, and whether the Client is held by value or by pointer.
func (Client) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, "flair client") }

// secrets is what a Client must not show: the credentials, and the token
// they bought.
type secrets struct {
	credentials Credentials

	mu sync.Mutex
	// token is the token held, and leave when it is no longer sent: before
	// its end, or never when its life was not told.
	token Token
	leave time.Time
}

// hold keeps a token, to be left a minute before its end, or halfway
// through what is left of a life shorter than two.
func (s *secrets) hold(token Token) {
	s.token, s.leave = token, time.Time{}

	if !token.Expires.IsZero() {
		left := max(time.Until(token.Expires), 0)
		s.leave = token.Expires.Add(-min(renewAhead, left/2))
	}
}

// New is a client for the credentials. Nothing is sent until a method is
// called: the first asks for a token, unless Options gave one, and a call
// made within a minute of that token's end asks for another.
//
// Flair gives about fifty tokens a day and no more. A program keeps its
// Client; one that must not ask again at each start keeps the token too,
// reading it with Token and giving it back in Options. Where it is kept
// is that program's business: nothing here writes one anywhere.
func New(credentials Credentials, opts Options) *Client {
	state := &secrets{credentials: credentials}
	state.hold(opts.Token)

	client := &Client{
		base:  strings.TrimSuffix(opts.BaseURL, "/"),
		http:  opts.HTTP,
		scope: opts.Scope,
		held:  func() *secrets { return state },
	}

	if client.base == "" {
		client.base = BaseURL
	}

	if client.http == nil {
		client.http = http.DefaultClient
	}

	if client.scope == "" {
		client.scope = DefaultScope
	}

	return client
}

// granted is the token route's answer.
type granted struct {
	//nolint:tagliatelle // OAuth2's own name
	AccessToken string `json:"access_token"`
	//nolint:tagliatelle // OAuth2's own name
	ExpiresIn lifetime `json:"expires_in"`
}

// lifetime is a token's life in seconds as the token route tells it. The
// documentation shows a number; a string of one is read too, and anything
// else is no life told.
type lifetime int64

// UnmarshalJSON reads the life, and never fails: a token whose life is
// not told is still a token.
func (l *lifetime) UnmarshalJSON(raw []byte) error {
	*l = 0

	if told, err := strconv.ParseFloat(strings.Trim(string(raw), `"`), 64); err == nil && told > 0 {
		*l = lifetime(told)
	}

	return nil
}

// usable is whether the token held may be sent: there is one, and it is
// not at its end. One whose life was not told is used until Flair refuses
// it, as Flair's own client does.
func (s *secrets) usable() bool {
	return s.token.Access != "" && (s.leave.IsZero() || time.Now().Before(s.leave))
}

// Token is the token the Client holds now, and whether it holds one: the
// one it was given, or the last one Flair gave it. It may be at its end.
func (c *Client) Token() (Token, bool) {
	held := c.held()

	held.mu.Lock()
	defer held.mu.Unlock()

	return held.token, held.token.Access != ""
}

// bearer is the token to send, and whether it was asked for just now. One
// is asked for when there is none, when the one held is at its end, or
// when the one held is spent, the token Flair has just refused.
func (c *Client) bearer(ctx context.Context, spent string) (string, bool, error) {
	held := c.held()

	held.mu.Lock()
	defer held.mu.Unlock()

	if spent != "" && held.token.Access == spent {
		held.hold(Token{})
	}

	if held.usable() {
		return held.token.Access, false, nil
	}

	if held.credentials.ClientID == "" || held.credentials.ClientSecret == "" {
		return "", false, fmt.Errorf("%w: the credentials are not whole", ErrArgument)
	}

	form := url.Values{
		"client_id":     {held.credentials.ClientID},
		"client_secret": {held.credentials.ClientSecret},
		"grant_type":    {"client_credentials"},
		"scope":         {c.scope},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+tokenPath, strings.NewReader(form.Encode()))
	if err != nil {
		return "", false, fmt.Errorf("%w: the token request: %w", ErrArgument, err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	raw, err := c.send(req, tokenPath)
	if err != nil {
		return "", false, err
	}

	var token granted
	if err = json.Unmarshal(raw, &token); err != nil || token.AccessToken == "" {
		return "", false, fmt.Errorf("%w: the token route gave no token", ErrAnswer)
	}

	given := Token{Access: token.AccessToken}
	if life := time.Duration(token.ExpiresIn) * time.Second; life > 0 {
		given.Expires = time.Now().Add(life)
	}

	held.hold(given)

	return given.Access, true, nil
}

// tokenPath is the token route; the documentation calls /oauth/token the
// deprecated one.
const tokenPath = "/oauth2/token" // #nosec G101 -- a path, not a credential

// call sends one request of the API under the token and returns the
// answer's body. target is a path, with its query when it has one. A
// token held from before that Flair answers 401 to is asked for again,
// once.
func (c *Client) call(ctx context.Context, method, target string, body any) ([]byte, error) {
	var payload []byte

	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("%w: the body for %s: %w", ErrArgument, target, err)
		}

		payload = raw
	}

	token, fresh, err := c.bearer(ctx, "")
	if err != nil {
		return nil, err
	}

	raw, err := c.under(ctx, token, method, target, payload)

	var refused *refusedError
	if fresh || !errors.As(err, &refused) || refused.status != http.StatusUnauthorized {
		return raw, err
	}

	if token, _, err = c.bearer(ctx, token); err != nil {
		return nil, err
	}

	return c.under(ctx, token, method, target, payload)
}

// under sends one request under token.
func (c *Client) under(ctx context.Context, token, method, target string, payload []byte) ([]byte, error) {
	var body io.Reader = http.NoBody
	if payload != nil {
		body = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+target, body)
	if err != nil {
		return nil, fmt.Errorf("%w: %s %s: %w", ErrArgument, method, target, err)
	}

	// The headers Flair's own client sends.
	req.Header.Set("Accept", "application/vnd.api+json")
	req.Header.Set("Authorization", "Bearer "+token)

	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.send(req, target)
}

// send does the request and returns a success's body; what is not a
// success is the error of its class.
func (c *Client) send(req *http.Request, target string) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", req.Method, target, err)
	}

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return nil, fmt.Errorf("%s %s: reading the answer: %w", req.Method, target, err)
	}

	if len(raw) > maxAnswer {
		return nil, fmt.Errorf("%w: %s %s: over %d bytes", ErrAnswer, req.Method, target, maxAnswer)
	}

	// The documentation shows 200 for every success; any 2xx is taken as
	// one.
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, refusal(req.Method, target, resp.StatusCode, raw)
	}

	return raw, nil
}

// The bodies of a refusal. The documentation shows errors as one object;
// the Home Assistant integration reads a list; the token route answers in
// OAuth2's words.
type (
	failure struct {
		Errors json.RawMessage `json:"errors"`
		Error  string          `json:"error"`
		//nolint:tagliatelle // OAuth2's own name
		Description string `json:"error_description"`
	}
	problem struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
)

// refusedError is an answer that is not a success: an error of its class,
// which keeps the status.
type refusedError struct {
	class  error
	status int
	said   string
}

func (e *refusedError) Error() string { return e.class.Error() + ": " + e.said }

func (e *refusedError) Unwrap() error { return e.class }

// refusal is the error for an answer that is not a success: its class by
// the status, with what Flair said. The token route refuses a client it
// does not know with a 400 or a 401.
func refusal(method, target string, status int, raw []byte) error {
	class := ErrRefused

	switch {
	case status == http.StatusNotFound:
		class = ErrNotFound
	case status == http.StatusUnauthorized, status == http.StatusForbidden,
		status == http.StatusBadRequest && target == tokenPath:
		class = ErrCredentials
	}

	said := fmt.Sprintf("%s %s: HTTP %d", method, target, status)
	if words := spoken(raw); words != "" {
		said += fmt.Sprintf(": %q", words)
	}

	return &refusedError{class: class, status: status, said: said}
}

// spoken is what a refusal's body says, in whichever of its shapes.
func spoken(raw []byte) string {
	var body failure
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}

	var (
		one  problem
		many []problem
	)

	if json.Unmarshal(body.Errors, &many) == nil && len(many) > 0 {
		one = many[0]
	} else {
		_ = json.Unmarshal(body.Errors, &one)
	}

	return strings.TrimSpace(strings.Join([]string{one.Title, one.Detail, body.Error, body.Description}, " "))
}

// The JSON:API documents, as far as they are read.
type (
	document struct {
		Data json.RawMessage `json:"data"`
		Meta pages           `json:"meta"`
	}
	// pages is where Flair puts a list's paging: in meta, not in links.
	pages struct {
		Next *string `json:"next"`
	}
	resource struct {
		Type          string                  `json:"type"`
		ID            string                  `json:"id"`
		Attributes    json.RawMessage         `json:"attributes"`
		Relationships map[string]relationship `json:"relationships"`
	}
	// relationship is a record's tie to another. The documentation's
	// examples write it two ways: with the other record's type and id in
	// data, or, on a second-generation puck, with a link to it and no
	// data.
	relationship struct {
		Data  json.RawMessage `json:"data"`
		Links related         `json:"links"`
	}
	related struct {
		Related string `json:"related"`
	}
	// identifier is a to-one relationship's data; the documentation's
	// examples have one with an id that is null.
	identifier struct {
		ID *string `json:"id"`
	}
)

// related is the id of the record a to-one relationship names; empty when
// it names none. One written as a link alone is followed, a request more.
func (c *Client) related(ctx context.Context, from *resource, name string) (string, error) {
	tie := from.Relationships[name]

	if len(tie.Data) > 0 {
		var named identifier
		if json.Unmarshal(tie.Data, &named) == nil && named.ID != nil {
			return *named.ID, nil
		}

		return "", nil
	}

	if tie.Links.Related == "" {
		return "", nil
	}

	if !apiPath(tie.Links.Related) {
		return "", fmt.Errorf("%w: the %s of %s %s is no path of the API: %q",
			ErrAnswer, name, from.Type, from.ID, tie.Links.Related)
	}

	// What a link answers when it leads to nothing is not documented: a
	// null and a 404 are both read as nothing.
	other, err := c.one(ctx, tie.Links.Related)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", err
	}

	return other.ID, nil
}

// apiPath is whether a link an answer carries is a path of this API, and
// so somewhere the token may go.
func apiPath(link string) bool {
	return strings.HasPrefix(link, "/") && !strings.HasPrefix(link, "//")
}

// attributes decodes the resource's attributes into into.
func (r *resource) attributes(into any) error {
	if err := json.Unmarshal(r.Attributes, into); err != nil {
		return fmt.Errorf("%w: the attributes of %s %s: %w", ErrAnswer, r.Type, r.ID, err)
	}

	return nil
}

// list reads every record of a collection, following the next page while
// there is one.
func (c *Client) list(ctx context.Context, path string) ([]resource, error) {
	var all []resource

	for range maxPages {
		raw, err := c.call(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}

		var (
			page    document
			records []resource
		)

		if err = json.Unmarshal(raw, &page); err == nil {
			err = json.Unmarshal(page.Data, &records)
		}

		if err != nil {
			return nil, fmt.Errorf("%w: GET %s: %w", ErrAnswer, path, err)
		}

		all = append(all, records...)

		if page.Meta.Next == nil || *page.Meta.Next == "" {
			return all, nil
		}

		// The token goes where the next page is: nowhere but this API.
		if path = *page.Meta.Next; !apiPath(path) {
			return nil, fmt.Errorf("%w: a next page that is no path of the API: %q", ErrAnswer, path)
		}
	}

	return nil, fmt.Errorf("%w: GET %s: over %d pages", ErrAnswer, path, maxPages)
}

// one reads a single record.
func (c *Client) one(ctx context.Context, path string) (resource, error) {
	raw, err := c.call(ctx, http.MethodGet, path, nil)
	if err != nil {
		return resource{}, err
	}

	var (
		page   document
		record resource
	)

	if err = json.Unmarshal(raw, &page); err == nil {
		err = json.Unmarshal(page.Data, &record)
	}

	if err != nil {
		return resource{}, fmt.Errorf("%w: GET %s: %w", ErrAnswer, path, err)
	}

	return record, nil
}

// The body of a write, as Flair's own client sends it.
type (
	change struct {
		Data changed `json:"data"`
	}
	changed struct {
		Type       string `json:"type"`
		ID         string `json:"id"`
		Attributes any    `json:"attributes"`
	}
)

// patch sets attributes on one record of a kind.
func (c *Client) patch(ctx context.Context, kind, record string, attributes any) error {
	path, err := itemPath(kind, record, "")
	if err != nil {
		return err
	}

	_, err = c.call(ctx, http.MethodPatch, path, change{Data: changed{Type: kind, ID: record, Attributes: attributes}})

	return err
}

// itemPath is the path of one record, the id escaped; an empty id is
// refused.
func itemPath(kind, id, rest string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("%w: an empty id for %s", ErrArgument, kind)
	}

	return "/api/" + kind + "/" + url.PathEscape(id) + rest, nil
}
