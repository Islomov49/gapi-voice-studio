# How the Studio is made

One Go process (`server/cmd/studio`) with Postgres and a React interface (`web/`). It is a client of Gapi's
public voice API and of nothing else: every call goes out through the Gapi project whose token it was given.

| Package | What it does |
| --- | --- |
| `internal/gapi` | Client of Gapi's voice API: options, lines, a call to a phone, its text and recording. `gapitest` is the stand-in the tests use |
| `internal/compile` | A Studio agent version into the agent Gapi is called with: the script plus the parts nobody can leave out (the AI greeting, "do not call me" ends the call, the form's questions); checks before a call |
| `internal/rules` | The calling rules as functions of the clock: hours, retries, what becomes of a person after a call, the guard between portions |
| `internal/dialer` | The one place a number is dialled from: channel limit, stop list, portions, following each call to its end |
| `internal/analyze` | Reads a finished call's text. Without a language model: the greeting check, the plain ways people say "do not call me", and the agent's promise to call again with its time (`callback.go`). With the workspace's own Gemini key (`ai.go`): the form, a summary, doubts; every answer is checked against the call's text before it is kept |
| `internal/gemini` | The Gemini Interactions API as the Studio uses it: one question answered as JSON of a given shape, not stored on Google's side; the model list checks a key |
| `internal/sheets`, `internal/phone` | Upload of .xlsx/.csv, numbers to `+998...`, results as .xlsx |
| `internal/templates` | The four task types: feedback, lead qualification, reminder, win-back |
| `internal/crm` | Bitrix24 (inbound webhook) and amoCRM (long-lived token, API v4) behind one interface: pipelines, the cards at a stage, a note, a stage change, a to-do. `crmtest` stands in for both in tests |
| `internal/api` | The interface's HTTP API (session by cookie) and the MCP server (`mcp.go`), whose tools are the same endpoints called from inside the process |

## A call

The dialer takes a person who is due, checks the stop list, builds the agent from the campaign's version and
the person's columns, takes one of the workspace's channels and asks Gapi to call (`POST /v1/voice/calls`
with the agent described whole, `to`, `from` and `variables`). Agents and versions live in the Studio only;
Gapi gets the one a call is made with. Every two seconds the dialer reads the open calls' state from Gapi;
when one has ended it fetches the text, runs the checks, and decides the person's next step.

The greeting is not sent as the call's first line. On a call the agent places, the person who picks up says
"Alo" first and Gapi's own short "Alo?" answers it; the greeting stands word for word inside the script's
first stage (`{{studio_greeting}}`), or as a rule at the end of a script that has no such place.

## A CRM

A person of a list may stand for a card (`ref`, "bitrix24:deal:123"). The dialer's round has two CRM steps:
`feed` reads a running campaign's stage once a minute and adds the people of cards it has not seen, and
`writeBack` takes up to three finished people a round and writes what the campaign's rules say (a note, a stage,
a to-do), which keeps a portal's request limit. Everything is a request from the Studio to the CRM; nothing
listens for the CRM.

## No language model inside

The Studio has no model of its own. Writing a script, changing it on a tester's feedback and reading the
forms out of the calls' text is the work of the person's assistant over MCP (`AGENTS.md`), or of the person
in the interface. What the service adds is the checks: an answer must quote words that are in the call's
text, a test call is accepted only if the agent introduced itself, and the order of work cannot be skipped.

## Data

A workspace is one Gapi project, keyed by the public id inside its token (two tokens of one project make
two workspaces). The token is kept sealed (AES-GCM under the service's secret). Phone numbers are stored as
they are; lists and tables show them masked unless asked, and MCP answers always mask them.

## Known gaps

- "Call me back at five" is retried after the campaign's ordinary pause; the named time is not read.
- The dialer assumes one running copy of the service.
- The CRM connectors have run against stand-in servers only, modelled on the vendors' documented answers.
- A card's result is written once, when the person's calls are over; a failed write is shown and not retried.
