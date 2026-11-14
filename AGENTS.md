# Working with Gapi Voice Agent Studio

For a coding assistant (Claude Code, Codex, and the like) whose person wants voice agents that call people by
phone through [Gapi](https://console.gapi.uz/docs/). The Studio is a local shell over Gapi's voice API: it keeps
agents and their versions, contact lists, campaigns and their results on the person's own machine, and it holds
the calling rules. You run it for the person through its MCP tools; the person watches and listens in the
browser at http://localhost:8090.

## Setting it up

1. The person needs a Gapi project with voice agents open and a phone line connected (Gapi cabinet, "Voice
   agents"), and that project's API token (cabinet, "Tokens"). The line's SIP password is theirs to enter in
   the cabinet; you never need it.
2. Start the Studio: `GAPI_TOKEN=<token> docker compose up -d` in this repository. Do not write the token into a
   file of the repository and do not print it back.
3. Connect yourself to it:
   - Claude Code: `claude mcp add --transport http gapi-studio http://localhost:8090/mcp`
   - Codex: `codex mcp add gapi-studio --url http://localhost:8090/mcp`
   - A client that only starts servers as commands: `docker compose exec -T studio studio mcp`
4. Call `studio_status`. Its `needs` lists what is missing: `line` (connect one in the Gapi cabinet),
   `tester_phone` (ask the person whose phone the agent should call first, then `set_tester_phone`), `runner` or
   `gapi` (Gapi's side is down: say so and wait).

## The order of work

The Studio enforces this order; working with it rather than around it saves refusals.

1. **Understand the task.** Who is called, why, and what the person wants to know after each call. If this
   repository's owner has a site, a price list or call recordings, read them first; ask only for what is missing.
2. **Make the agent.** `list_templates`, then `create_agent` from the closest one, then `save_agent_version` with
   the first paragraph and the facts rewritten for this business. Keep fixing until `problems` is empty.
3. **Call the tester.** `start_test_call`, then `get_call` every few seconds until `settled` is true. Tell the
   person the phone is about to ring before you start it.
4. **Take the tester's verdict.** Ask how the call went. Their words go into `record_tester_verdict`. Accept only
   when the tester said the call is good: you have not heard the call, they have. If they have remarks, change
   the script (`save_agent_version`, with a `note` saying what changed) and call again. A changed script is a
   new version and needs its own accepted test call.
5. **Run the campaign.** `import_contacts`, `create_campaign`, and `start_campaign` once the person says to
   start. It calls a first portion of 10 people, then 30 at a time, three calls at once, inside the calling hours.
6. **Read the results.** `get_campaign` for the state; `list_calls` with `needs_form` for the answered calls whose
   form is empty; `get_call` and `fill_call_form` for each. Report in plain numbers: reached, outcomes, what
   people said, where the agent went wrong.

**A person who asks to be called later** is called again by the Studio itself. The agent asks when, repeats the
time ("... yarim soatdan keyin qayta qo'ng'iroq qilamiz") and ends the call; the Studio reads the time from the
agent's words (`checks.callback`, `checks.callback_at` in `get_call`) and places the next call then, or at the first
calling minute after it. No time named: after the pause between attempts. Such a call is placed even past the
campaign's attempts, twice at most. The result `callback` is something else: a person a human should call.

A campaign that pauses itself says why in `pause_reason`:

| Reason | What it means | What to do |
| --- | --- | --- |
| `agent_did_not_introduce_itself` | In a fifth of the answered calls the agent did not open as an AI assistant | Read those calls, fix the script's first stage, test call again |
| `do_not_call_requests` | A fifth of the people who answered asked not to be called | Wrong audience or wrong opening: show the person the calls before going on |
| `technical_failures` | A third of the calls failed on the line's side | The line in the Gapi cabinet; not the script |
| `balance` | The Gapi balance is spent | The person tops it up |
| `line`, `access`, `agent_invalid` | Gapi refused the line, the token or the script | `studio_status`, `get_agent` |

Resuming a campaign that stopped itself (`start_campaign` again) tells it a person has looked at the reason.
Resume only after the person has.

## People from a CRM

The Studio connects to Bitrix24 and amoCRM by itself. It asks the CRM (every minute, for a campaign that follows
a stage), so it needs no public address, no tunnel and no webhook from the CRM's side.

- **Connecting** takes a secret (Bitrix24: an inbound webhook's address with the right "CRM"; amoCRM: an
  integration's long-lived token). The person enters it in the interface, page "CRM". Do not ask for it in the
  chat and do not put it into a tool call.
- `crm_connections`, then `crm_pipelines` for the stages' ids, then `crm_import_stage`: the cards standing at a
  stage become a list, each person remembering their card.
- `create_campaign` with `crm`: `note` writes a comment onto the card when the person's calls are over (the
  result, the attempts, the summary; the form follows when you fill it), `move` puts the card on a stage by
  result, `task` sets a to-do for the card's own manager, `feed` keeps adding the cards that arrive at a stage
  while the campaign runs. Agree the stages and the task's words with the person before you set them: they
  change the business's own pipeline.
- `get_campaign` shows `crm_failed`: calls whose result the CRM did not take. Say so; do not retry by hand.

Any other system (another CRM, a spreadsheet service, an MCP server of the business's own) works through you:
read the people there with that system's tools, give them to the Studio with `import_contact_rows` (each row with
`ref`, that system's id of the person), and after the campaign read `campaign_results`, where every row carries
its `ref`, and write the outcomes back with that system's tools.

## Writing the script

The script is the whole of what the agent knows. Gapi's own guide is the reference:
https://console.gapi.uz/docs/en/agents.html#prompt. What matters most:

- Write it in the callers' language. The model speaks Uzbek.
- Structure: who the agent is and for which company; `Ma'lumotlar` (the facts it may state, each once);
  the situation and the goal; `Suhbat bosqichlari` (numbered stages); objections; strict rules.
- A rule works when it names a moment and the words: "If the customer says it is expensive, first ask what
  they compare with" works; "handle objections persuasively" does not.
- The exact phrase matters? Put it in quotes.
- Keep `{{studio_greeting}}` in the first stage. The Studio puts the agent's first words there: the company's
  name and that an AI assistant is calling. Do not write a human name or role for the agent.
- What is not in the script the agent does not know; say what to answer then ("we will find out and tell you").
- Replies are heard, not read: one or two short sentences and one question at a time.
- Values that differ per person are variables: `{{client_name}}`, `{{order}}`. A list's columns fill them by
  name (`import_contacts` answers the names); give each a default that reads well when the column is empty.
- The whole instruction is at most 8,000 characters; `get_agent` shows it as the model reads it.

## Calibrating after a test call

Fix the script rather than adding to it.

| What the tester heard | What to change |
| --- | --- |
| The agent said something untrue | The fact is missing or written twice in two ways: keep it once, in the facts |
| It ignored a rule | Rewrite the rule as a moment and the words, inside the stage it belongs to |
| It talks too long | "One or two sentences" in the strict rules; cut explanations out of the facts |
| It did not catch a name | Add the name to `vocabulary` |
| It asked the same thing twice | A stage and a form question (`ask`) say the same: keep one |
| It ended too early or too late | Rewrite the last stage: what is repeated back, when to say goodbye |

## The form

A form field is one thing the call should find out. `ask` is how the agent asks for it; leave `ask` empty when
a stage of the script already asks. After a call, fill the form from the call's text:

- Every answer carries `quote`: the person's words it was taken from, copied exactly from the text. The Studio
  refuses an answer whose quote is not in the call.
- A field nobody answered stays empty. Do not infer a rating from a tone.
- `summary` is one or two sentences in the person's own language (the Studio's interface is Russian).

If the person has added their Gemini key in the interface (`studio_status` says `"ai": true`), the Studio fills
the form itself after every answered call, under the same two rules. Then your part is the calls it was not
sure of: `get_call` shows `doubts` (what to check by ear) and `analysis` (`ai`, or `failed` with
`analysis_error` when the model did not answer); correct with `fill_call_form`, or have it read a call again
with `analyze_call`. The key is the person's to enter, on the page "Обзор": do not ask for it in the chat.
`get_campaign` counts the answers per field in `form`.

## What you do not do

- Call a number the person did not give you, or start a campaign they did not ask to start.
- Accept a test call yourself.
- Take a number off the stop list: only the person does, in the interface.
- Raise the calling hours past 08:00-21:00, or look for a way around a refusal: the refusal is the rule.
- Put the Studio on a network. It listens on the machine it runs on and places calls on the project's balance.
- Print, log or commit the Gapi token.

## Changing the Studio itself

`server/` is the Go service (`internal/api` the endpoints and the MCP tools, `internal/dialer` the calls,
`internal/rules` and `internal/compile` the rules and the script building, `internal/crm` the two CRMs), `web/` the React interface;
`docs/ARCHITECTURE.md` says how they fit. Tests: `STUDIO_TEST_DATABASE_URL=postgres://... go test ./...` in
`server/` (they drop and recreate that database's schema), `npm run build` in `web/`.
