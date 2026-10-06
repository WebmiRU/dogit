package api

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/store"
)

// Where a row in the list came from, which is the difference between a registry this
// instance runs and a registry somebody wrote down. A row that is not marked is not
// editable, and one that is marked is not the reader's to change here.
const (
	registrySourceModule  = "module"
	registrySourceWritten = "written"
)

// dockerRegistriesPerPage is what one page of the list holds.
//
// Ten, because the list is a table an administrator reads down rather than searches
// through, and because a page of fifty addresses is a page nobody reads, while a page of
// two is a page that sends you back for more on every click.
const dockerRegistriesPerPage = 10

// dockerRegistryInput is one registry as a form sends it.
//
// Every field is a pointer because a form sends some fields and not others, and a handler
// that cannot tell "not sent" from "sent as false" turns every switch off the moment
// somebody edits a name.
//
// The password is the one field that is here rather than absent, and it is here for both
// halves of its life: what a form sends in, and the empty string that means "clear it".
// It is never what comes back out.
type dockerRegistryInput struct {
	Name        *string `json:"name"`
	URL         *string `json:"url"`
	Login       *string `json:"login"`
	Password    *string `json:"password"`
	InsecureTLS *bool   `json:"insecure_tls"`
	ReadOnly    *bool   `json:"read_only"`
	Default     *bool   `json:"is_default"`
	Note        *string `json:"note"`
	Enabled     *bool   `json:"enabled"`
}

// handleListDockerRegistries is the administrator's list of Docker registries.
//
// Ten to a page, with the size of the whole list alongside so the pager can say which ten
// of how many. Nothing here polls: a registry does not push itself, roll out, or run out,
// and a list that refetches itself every second would be a page pretending to be a
// deployment page.
// handleListDockerRegistries is the administrator's list of Docker registries.
//
// Two populations in one list, and this is the shape of it: the registries this instance
// runs as a module, and the ones somebody wrote an address down for. The module's own
// registries come first, because they are where images actually go, and each row says
// which kind it is — a row that can be edited here is an address somebody chose, and a row
// that cannot is one a module brought with it, and a list that did not say which was which
// would have a reader editing the wrong one.
//
// Ten to a page, with the size of the whole list alongside so the pager can say which ten
// of how many. Nothing here polls: a registry does not push itself, roll out, or run out,
// and a list that refetches itself every second would be a page pretending to be a
// deployment page.
func (s *Server) handleListDockerRegistries(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	modules, err := s.dockerRegistryModules(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	page := queryInt(r, "page", 1, 10_000)

	// The module's registries are not paged away from the first page. They are few, they
	// are what a reader came for, and a registry that exists but sits on page four is a
	// registry this page cannot show to somebody who has come to fix it. So they take the
	// top of the first page and the written-down addresses fill what is left of it, and
	// every page after the first is only those addresses, offset by however many module
	// registries the first page was carrying. That arithmetic is the whole of paging a
	// list with two kinds of row in it, and it is done here once rather than at each call
	// site where it would be done slightly differently.
	writtenPerPage := dockerRegistriesPerPage
	offset := (page - 1) * dockerRegistriesPerPage
	if page == 1 {
		writtenPerPage = dockerRegistriesPerPage - len(modules)
		if writtenPerPage < 0 {
			writtenPerPage = 0
		}
	} else {
		offset -= len(modules)
		if offset < 0 {
			offset = 0
		}
	}

	written, totalWritten, err := s.store.DockerRegistries().List(r.Context(),
		store.DockerRegistryListFilter{Limit: writtenPerPage, Offset: offset})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	rows := make([]map[string]any, 0, len(modules)+len(written))
	// Only the first page carries the module's registries. Putting them on every page would
	// list them as many times as there are pages, and a registry that a reader can find
	// three times is a list they have to check for duplicates in.
	if page == 1 {
		rows = append(rows, modules...)
	}
	for _, reg := range written {
		rows = append(rows, dockerRegistryView(reg))
	}

	total := len(modules) + totalWritten
	pages := (total + dockerRegistriesPerPage - 1) / dockerRegistriesPerPage
	if pages < 1 {
		pages = 1
	}

	// A page past the end — a stale link, or a list that has shrunk since the address was
	// written. Answering it as an empty list would be true and useless: there are
	// registries, and this page would be claiming there are none.
	pastTheEnd := len(rows) == 0 && total > 0 && page > pages

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"registries":   rows,
		"total":        total,
		"page":         page,
		"pages":        pages,
		"per_page":     dockerRegistriesPerPage,
		"past_the_end": pastTheEnd,
	})
}

// dockerRegistryModules are the Docker registries this instance runs as a module.
//
// Read, never copied. A row in the table of addresses is something an administrator wrote
// down; a module is something somebody installed, and it can be forbidden, re-addressed or
// uninstalled at any moment without telling anybody. A copy would be wrong the day after it
// was made and would go on listing a registry that no longer exists — and a list that
// names a registry which is not there is worse than no list, because it is believed.
//
// A module that is forbidden is listed anyway, marked as one. Somebody reading this page is
// asking where images go, and "your registry is switched off" is an answer; a row that was
// quietly not there would be a question.
func (s *Server) dockerRegistryModules(r *http.Request) ([]map[string]any, error) {
	integrations, err := s.store.Integrations().List(r.Context())
	if err != nil {
		return nil, err
	}

	out := make([]map[string]any, 0, len(integrations))
	for _, integration := range integrations {
		if integration.Kind != registryKind {
			continue
		}
		address, err := s.registryAddress(r, integration)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"source":         registrySourceModule,
			"integration_id": integration.ID,
			"module_kind":    integration.Kind,
			"name":           integration.Name,
			"url":            address,
			"published":      address != "",
			"enabled":        integration.Enabled,
			"status":         integration.Status,
		})
	}
	return out, nil
}

// handleGetDockerRegistry is one registry, for the page that edits it.
//
// Its own request rather than a search through the list: the list is ten to a page and
// sorted, so a registry whose name puts it on the fourth page is not in the first page's
// answer, and an edit form that could not find its record would be an edit form that
// reports a registry as missing when the administrator has it open in front of them.
func (s *Server) handleGetDockerRegistry(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	id, err := dockerRegistryIDFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	reg, err := s.store.DockerRegistries().ByID(r.Context(), id)
	if err != nil {
		s.writeError(w, r, dockerRegistryError(err))
		return
	}

	s.writeJSON(w, r, http.StatusOK, dockerRegistryView(*reg))
}

// handleCreateDockerRegistry writes a new registry down.
//
// The address is the only thing required, because it is the only thing every registry has:
// a private one wants a login and a public one must not be asked for one, so a record that
// could not be saved without a credential would be a record unable to describe half of what
// this table is for.
func (s *Server) handleCreateDockerRegistry(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	var body dockerRegistryInput
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	if body.URL == nil {
		s.writeError(w, r, errBadRequest("a registry needs an address"))
		return
	}

	reg := store.DockerRegistry{Enabled: true}
	if err := applyDockerRegistryInput(&reg, body, true); err != nil {
		s.writeError(w, r, err)
		return
	}

	if err := s.store.DockerRegistries().Create(r.Context(), &reg); err != nil {
		s.writeError(w, r, dockerRegistryError(err))
		return
	}

	s.writeJSON(w, r, http.StatusCreated, dockerRegistryView(reg))
}

// handleUpdateDockerRegistry writes an edited registry back.
//
// Read, change, write — the whole record goes back rather than a patch of it, because the
// switches are switches and a partial update that left them alone would have to guess what
// "alone" meant.
//
// The password is the exception, and the exception has to be deliberate in both directions.
// The page is never given a password to hold, so editing a name cannot blank the
// credential; and an empty password sent on purpose clears it, because a form with no way
// to remove a credential leaves an administrator who pasted the wrong one with no way out.
func (s *Server) handleUpdateDockerRegistry(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	id, err := dockerRegistryIDFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	reg, err := s.store.DockerRegistries().ByID(r.Context(), id)
	if err != nil {
		s.writeError(w, r, dockerRegistryError(err))
		return
	}

	var body dockerRegistryInput
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := applyDockerRegistryInput(reg, body, false); err != nil {
		s.writeError(w, r, err)
		return
	}

	if err := s.store.DockerRegistries().Update(r.Context(), reg); err != nil {
		s.writeError(w, r, dockerRegistryError(err))
		return
	}

	s.writeJSON(w, r, http.StatusOK, dockerRegistryView(*reg))
}

// handleDeleteDockerRegistry takes a registry off the list.
//
// The record and nothing else, which the handler is careful about: this deletes a row in a
// table of addresses. It does not reach into the registry, does not remove an image, and
// does not undo anything anybody did with the address. A button that looked as though it
// would empty a registry and emptied a table instead is the sort of surprise an
// administrator finds out about from a customer's missing tag.
func (s *Server) handleDeleteDockerRegistry(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	id, err := dockerRegistryIDFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.store.DockerRegistries().Delete(r.Context(), id); err != nil {
		s.writeError(w, r, dockerRegistryError(err))
		return
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{"deleted": true})
}

// dockerRegistryView is one registry as a browser is given it.
//
// The password is not here. It is stored and it is editable, and it is not returned: a list
// of ten registries would otherwise put ten passwords into the page, into the browser's
// memory and into whatever took a photograph of the screen. What the page gets is
// has_password, which is what it needs to render a field that says "set" rather than
// showing a value nobody should be shown.
func dockerRegistryView(reg store.DockerRegistry) map[string]any {
	return map[string]any{
		"source":       registrySourceWritten,
		"id":           reg.ID,
		"name":         reg.Name,
		"url":          reg.URL,
		"login":        reg.Login,
		"has_password": reg.Password != "",
		"insecure_tls": reg.InsecureTLS,
		"read_only":    reg.ReadOnly,
		"is_default":   reg.Default,
		"note":         reg.Note,
		"enabled":      reg.Enabled,
		"created_at":   reg.CreatedAt,
		"updated_at":   reg.UpdatedAt,
	}
}

// applyDockerRegistryInput copies what a form sent over what was there, and refuses what
// cannot be a registry.
//
// required is the difference between the two callers: a new record must arrive with an
// address, while an edit that does not mention one keeps the address it had. An edit that
// sends an empty address is a mistake worth reporting rather than storing, since the
// alternative is a record with nothing in it that the list then cannot address.
func applyDockerRegistryInput(reg *store.DockerRegistry, body dockerRegistryInput, required bool) error {
	if body.URL != nil {
		url := strings.TrimSpace(*body.URL)
		if url == "" && !required {
			return errBadRequest("a registry needs an address; to keep the one it has, leave the field out")
		}
		if err := checkDockerRegistryURL(url); err != nil {
			return err
		}
		reg.URL = url
	} else if required {
		return errBadRequest("a registry needs an address")
	}

	if body.Name != nil {
		if n := utf8.RuneCountInString(strings.TrimSpace(*body.Name)); n > 200 {
			return errBadRequestf("the name is %d characters long, and the limit is 200", n)
		}
		reg.Name = strings.TrimSpace(*body.Name)
	}
	if body.Login != nil {
		if n := utf8.RuneCountInString(strings.TrimSpace(*body.Login)); n > 200 {
			return errBadRequestf("the login is %d characters long, and the limit is 200", n)
		}
		reg.Login = strings.TrimSpace(*body.Login)
	}
	if body.Password != nil {
		if len(*body.Password) > 2000 {
			return errBadRequest("the password is longer than 2000 bytes")
		}
		reg.Password = *body.Password
	}
	if body.Note != nil {
		if n := utf8.RuneCountInString(strings.TrimSpace(*body.Note)); n > 5000 {
			return errBadRequestf("the note is %d characters long, and the limit is 5000", n)
		}
		reg.Note = strings.TrimSpace(*body.Note)
	}
	if body.InsecureTLS != nil {
		reg.InsecureTLS = *body.InsecureTLS
	}
	if body.ReadOnly != nil {
		reg.ReadOnly = *body.ReadOnly
	}
	if body.Default != nil {
		reg.Default = *body.Default
	}
	if body.Enabled != nil {
		reg.Enabled = *body.Enabled
	}
	return nil
}

// checkDockerRegistryURL is what makes an address an address.
//
// An optional scheme, a host, an optional port, an optional path prefix — and no spaces,
// because an address with a space in it is a typo rather than a place, and storing it
// produces a record that cannot be used and can only be found again by scrolling.
//
// This is not a hostname validator and does not pretend to be. Whether a name resolves is
// not this handler's business to decide, and a check that insisted on resolving it would
// refuse to save a registry that is behind a VPN nobody has connected to yet — which is
// exactly when somebody wants to write its address down.
func checkDockerRegistryURL(url string) error {
	if url == "" {
		return errBadRequest("a registry needs an address")
	}
	if len(url) > 500 {
		return errBadRequest("the address is longer than 500 characters")
	}
	if strings.ContainsAny(url, " \t\r\n") {
		return errBadRequest("a registry address has no spaces in it")
	}

	rest := url
	if i := strings.Index(rest, "://"); i >= 0 {
		scheme := strings.ToLower(rest[:i])
		if scheme != "http" && scheme != "https" {
			return errBadRequestf("%q is not an address this can reach: a registry is reached over http or https", url)
		}
		rest = rest[i+3:]
	}
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return errBadRequestf("%q is a scheme and nothing else; a registry needs a host", url)
	}

	host := rest
	if i := strings.Index(rest, "/"); i >= 0 {
		host = rest[:i]
	}

	// A host, and then the three ways an address may carry one.
	//
	// Brackets for IPv6, because an IPv6 address is full of colons and "::1:5000" cannot be
	// told from a host called "::1" on port 5000 — so a bracketed one is read as a whole and
	// a bracketed-less one that holds two colons is refused with the sentence that says how
	// to write it, rather than being stored as a host whose name is a colon.
	switch {
	case strings.HasPrefix(host, "["):
		end := strings.Index(host, "]")
		if end < 0 {
			return errBadRequestf("%q has an opening bracket and no closing one", url)
		}
		// The colon between "]" and the port is optional, so it is trimmed rather than
		// read as part of the port number.
		if port := strings.TrimPrefix(host[end+1:], ":"); port != "" {
			if err := checkDockerRegistryPort(port); err != nil {
				return errBadRequestf("%q: %s", url, err.Error())
			}
		}
		host = host[1:end]
	case strings.Count(host, ":") == 1:
		i := strings.Index(host, ":")
		if err := checkDockerRegistryPort(host[i+1:]); err != nil {
			return errBadRequestf("%q: %s", url, err.Error())
		}
		host = host[:i]
	case strings.Contains(host, ":"):
		return errBadRequestf("%q looks like an IPv6 address written without brackets; write it as [::1]:5000", url)
	}

	if host == "" {
		return errBadRequestf("%q has no host in it", url)
	}
	return nil
}

// checkDockerRegistryPort is the digits after a colon, if any.
func checkDockerRegistryPort(port string) error {
	if port == "" {
		return errors.New("the address ends in a colon with no port after it")
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return errors.New("what follows the colon is not a port number")
		}
	}
	return nil
}

// dockerRegistryIDFromPath reads the id of the registry being edited.
//
// A path that is not a uuid at all is a 400 and not a 404: asking for something that could
// never exist is a different mistake from asking for a registry that does not, and
// answering both the same way sends the reader looking for a record somebody deleted.
func dockerRegistryIDFromPath(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(pathParam(r, "registryID"))
	if err != nil {
		return uuid.Nil, errBadRequestf("%q is not a registry's id", pathParam(r, "registryID"))
	}
	return id, nil
}

// dockerRegistryError is what a browser is told, where the store's own words are for a log.
//
// The two cases a person caused get sentences: the same address written down twice, and a
// record that is not there. Everything else is passed through untouched, because an
// unexpected database error is exactly as interesting in a browser as it is in a log.
func dockerRegistryError(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return errNotFoundf("there is no registry with that id")
	case errors.Is(err, store.ErrConflict):
		// The store's sentence carries the sentinel so a caller can match it, and a
		// sentence a person reads should not open with the word "conflict:" — that is
		// the shape of the error, not the news.
		return errBadRequest(strings.TrimPrefix(err.Error(), store.ErrConflict.Error()+": "))
	}
	return err
}
