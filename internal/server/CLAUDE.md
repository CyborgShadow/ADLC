# internal/server

The operator's dashboard. Every figure on it is a projection of the ledger; it writes back a
deliberately small number of things — an answer to a question, a decision on an approval, a
sign-off, a lane's cadence or pause switch, and the console's own turns.

Handlers live in `server.go` and one file per page (`roadmap.go`, `item.go`, `history.go`,
`roles_page.go`, `config_page.go`, `console.go`, `cost.go`, `about.go`, …). The HTML lives in the
matching `*_tmpl.go` files and in `templates.go`.

## What goes wrong here

**A fragment that is not in the list.** `templates.go` builds one template by concatenating a fixed
slice of string constants. A new page's HTML that is not added to that slice renders as nothing at
all, with no error. `consoleDockHTML` and `fieldsHTML` come **after** `endHTML` because they are
their own templates — a `define` nested inside another `define` is a parse error.

**An unbalanced or unguarded fragment.** Every page fragment is
`{{if eq .Page "x"}}{{with .Body}} … {{end}}{{end}}`, and all of them sit inside the single
`{{define "page"}}`. A `{{with .Data}}` written outside its page guard evaluates on every other
page, where that field does not exist. Because `page` executes straight into the
`http.ResponseWriter`, the error arrives after bytes are already on the wire: every page silently
truncates at that point, headers are already sent, and the 500 never renders. Keep the guard, keep
the `{{end}}`s balanced, and run `TestEveryPageRendersOverARealLedger`.

**Binding to anything but loopback.** This surface writes to the ledger and has no authentication.
Those two facts are welded together and `Bind` refuses to separate them, including an empty host,
which listens on every interface.

**Editing a render.** Nothing here may hold a figure the ledger does not. If a number is wrong, the
projection or the report is wrong; a value patched on the way to the page is a second answer to a
question the record already answers.

**No JavaScript, no external assets.** Every control is a plain form and the refresh is a meta tag.
This is a constraint, not a taste: the dashboard has to work when something has gone wrong, which
is the only time anybody opens it.

**Writing on a GET.** The write paths check the method. A dashboard whose links mutate state gets
mutated by a browser prefetch.

## Tests

`server_test.go` walks every page over a seeded ledger (a page that 500s is one an operator meets
at the worst possible moment), pins the attention banner, and pins the write paths: an answer is
recorded verbatim, an empty answer is refused, an approval requires a name, an unknown verdict is
refused, a GET changes nothing, a paused lane is persisted and recorded. `console_test.go` pins
that a turn is recorded either way, that authority decides what runs and what waits, and that the
record tells the agent apart from the person. `signoff_test.go` pins that the sign-off control is
not a shortcut past research or plan validation.
