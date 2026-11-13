# Gapi Voice Agent Studio

A local shell over [Gapi](https://console.gapi.uz)'s voice agents: describe a task, make an agent with a form,
hear it on a test call, then let it call a list of people in portions and read the answers as a table.
It runs on your own machine against your own Gapi project; nothing of yours is hosted anywhere else.

It has two ways in:

- **A web interface** at http://localhost:8090 (Russian): agents and their versions, test calls with the
  recording and the text, contact lists from Excel, campaigns, results, the stop list.
- **An MCP server** at http://localhost:8090/mcp: your coding assistant (Claude Code, Codex) does the same
  through tools, writes the agent's script from what it knows of your business, fills the forms from the
  calls' text, and tells you what the calls showed. [AGENTS.md](AGENTS.md) is its working guide.

## Start

You need Docker, a Gapi project with voice agents open and a phone line connected, and the project's API
token (Gapi cabinet, "Tokens").

    git clone https://github.com/Islomov49/gapi-voice-studio && cd gapi-voice-studio
    GAPI_TOKEN=gapi_... docker compose up -d

Open http://localhost:8090. Without `GAPI_TOKEN` the Studio asks for the token in the browser.

Connect an assistant:

    claude mcp add --transport http gapi-studio http://localhost:8090/mcp
    codex mcp add gapi-studio --url http://localhost:8090/mcp

and ask it, for example: "Read AGENTS.md. Make a feedback agent for my furniture shop and call me to test it."

## The rules it holds

Whoever asks, the interface or an assistant, the service checks these before each dial:

- A campaign starts only with an agent version whose test call a person accepted. A change of the script is
  a new version and needs its own test call.
- The agent opens every call by naming the company and saying that an AI assistant is calling; each call's
  text is checked for it.
- Calls are placed only inside the campaign's hours, Tashkent time, and never outside 08:00-21:00.
- Three calls at once per project, test calls included.
- The stop list: checked at upload and before each dial; a person's "do not call me" adds the number; only
  a person removes one.
- Portions: 10 people, then 30 at a time. After a portion the campaign stops itself if the calls look wrong
  (the line fails, people ask not to be called, the agent does not introduce itself).

The Studio is a convenience, not a gate: the calls are your Gapi project's, paid from its balance, and what
Gapi itself allows your project is decided there.

## Keep it local

The Studio listens on `127.0.0.1` and has no accounts of its own: whoever can open it can place calls on
your project's balance. Do not publish its port.

## Development

`server/` is the Go service, `web/` the React interface; see [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

    docker run -d --name studio-pg -e POSTGRES_PASSWORD=dev -p 127.0.0.1:55441:5432 postgres:16
    cd web && npm ci && npm run build
    cd ../server && STUDIO_DATABASE_URL=postgres://postgres:dev@127.0.0.1:55441/postgres \
      STUDIO_SECRET_FILE=.secret STUDIO_WEB_DIR=../web/dist go run ./cmd/studio

Tests: `STUDIO_TEST_DATABASE_URL=postgres://... go test ./...` in `server/`; they drop and recreate the
schema of the database they are given.

## Licence

MIT, see [LICENSE](LICENSE). The interface's fonts, Manrope and JetBrains Mono, are under the SIL Open Font
License (`web/public/fonts`).
