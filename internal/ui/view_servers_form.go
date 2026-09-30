package ui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
)

// serverEntryAt reads the entry a form's url, fingerprint, ca and insecure
// fields describe, starting at index off.
func serverEntryAt(fields []formField, off int) remote.ServerEntry {
	val := func(i int) string { return strings.TrimSpace(fields[i].input.Value()) }
	return remote.ServerEntry{
		URL:         val(off),
		Fingerprint: val(off + 1),
		CA:          val(off + 2),
		Insecure:    serverYes(val(off + 3)),
	}
}

// serverYes reads the form's insecure field: yes, y, true, 1 or on is true.
func serverYes(s string) bool {
	switch strings.ToLower(s) {
	case "yes", "y", "true", "1", "on":
		return true
	}
	return false
}

// serverYesText is the insecure field's prefilled value for an edit.
func serverYesText(insecure bool) string {
	if insecure {
		return "yes"
	}
	return "no"
}

// serverFieldCheck validates a form's entry the way the config layer will, by
// asking the same AddServer or EditServer the submit applies: an invalid url
// or trust setting keeps the form open on the field that carries the check.
func serverFieldCheck(doc relevo.ConfigDoc, name string, fields []formField, off int) error {
	entry := serverEntryAt(fields, off)
	if name == "" {
		_, err := relevo.AddServer(doc, strings.TrimSpace(fields[0].input.Value()), entry)
		return err
	}
	_, err := relevo.EditServer(doc, name, entry)
	return err
}

// serverNameField validates the name alone: AddServer is asked about a
// placeholder entry that always passes, so only the name's own refusals -- an
// empty name, the reserved local, or one already taken -- come back.
func serverNameField(doc relevo.ConfigDoc, fields []formField) error {
	_, err := relevo.AddServer(doc, strings.TrimSpace(fields[0].input.Value()),
		remote.ServerEntry{URL: "https://placeholder:7777", CA: "system"})
	return err
}

// newAddServerForm builds the a key's overlay: the new server's name, url and
// trust fields.
func newAddServerForm(env Env, doc relevo.ConfigDoc) formBox {
	nameIn := newFormInput(true)
	nameIn.Placeholder = "lowercase, e.g. zen"
	fields := []formField{
		{label: "name", input: nameIn},
		{label: "url", input: newFormInput(false), hint: "e.g. https://host:7777"},
		{label: "fingerprint", input: newFormInput(false), hint: "the sha256 pin, or empty with ca"},
		{label: "ca", input: newFormInput(false), hint: `"system", or empty with a fingerprint`},
		{label: "insecure", input: newFormInput(false), hint: "yes or no; a plain http url needs yes"},
	}
	fields[0].validate = func(string) error { return serverNameField(doc, fields) }
	fields[1].validate = func(string) error { return serverFieldCheck(doc, "", fields, 1) }

	return formBox{
		kind:   "add server",
		submit: "add",
		header: []string{accentStyle.Bold(true).Render("Add a remote builder's server")},
		note:   []string{"The client key stays with relevo config server key; this view never edits it."},
		fields: fields,
		onSubmit: func([]string) tea.Cmd {
			edit, err := relevo.AddServer(doc, strings.TrimSpace(fields[0].input.Value()), serverEntryAt(fields, 1))
			if err != nil {
				return notice(err.Error())
			}
			return runAction(env.Ctx, "add server", edit.Name, func(ctx context.Context) Result {
				return env.Actions.ApplyConfig(ctx, edit)
			})
		},
	}
}

// newEditServerForm builds the e key's overlay: the selected server's url and
// trust fields. The name cannot change, so it is the header's, not a field.
func newEditServerForm(env Env, doc relevo.ConfigDoc, r serverRow) formBox {
	urlIn := newFormInput(true)
	urlIn.SetValue(r.url)
	urlIn.CursorEnd()
	fpIn := newFormInput(false)
	fpIn.SetValue(r.full)
	caIn := newFormInput(false)
	caIn.SetValue(r.ca)
	insIn := newFormInput(false)
	insIn.SetValue(serverYesText(r.insecure))

	fields := []formField{
		{label: "url", input: urlIn, hint: "e.g. https://host:7777"},
		{label: "fingerprint", input: fpIn, hint: "the sha256 pin, or empty with ca"},
		{label: "ca", input: caIn, hint: `"system", or empty with a fingerprint`},
		{label: "insecure", input: insIn, hint: "yes or no"},
	}
	fields[0].validate = func(string) error { return serverFieldCheck(doc, r.name, fields, 0) }

	return formBox{
		kind:   "edit server",
		submit: "save",
		header: []string{accentStyle.Bold(true).Render("Edit server " + r.name)},
		fields: fields,
		onSubmit: func([]string) tea.Cmd {
			edit, err := relevo.EditServer(doc, r.name, serverEntryAt(fields, 0))
			if err != nil {
				return notice(err.Error())
			}
			return runAction(env.Ctx, "edit server", r.name, func(ctx context.Context) Result {
				return env.Actions.ApplyConfig(ctx, edit)
			})
		},
	}
}
