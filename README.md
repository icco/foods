# foods

A remote [MCP](https://modelcontextprotocol.io) server for logging food, water and workouts by talking to Gemini (or any MCP client). Say "I had oatmeal with blueberries for breakfast" and it looks the food up in [USDA FoodData Central](https://fdc.nal.usda.gov/) or [Open Food Facts](https://world.openfoodfacts.org/), saves it to your Postgres, and does the nutrition math itself.

It is a Go and Postgres take on [Bradley Dettmer's food tracking MCP](https://www.bradleydettmer.me/blog/deploy-your-own-food-tracking-mcp/) ([template](https://github.com/redSlug/healthy-mcp-template)), with Google sign-in instead of GitHub.

## Install

```sh
docker run -p 8080:8080 \
  -e DATABASE_URL=postgres://alice:secret@db:5432/foods \
  -e BASE_URL=https://foods.example.com \
  -e GOOGLE_CLIENT_ID=... -e GOOGLE_CLIENT_SECRET=... \
  -e ALLOWED_EMAILS=alice@example.com \
  -e USDA_API_KEY=... \
  ghcr.io/icco/foods:main
```

Or `go install go.icco.me/foods@latest`. The server creates its tables on startup.

| Variable | Value |
| --- | --- |
| `DATABASE_URL` | Postgres connection string. |
| `BASE_URL` | Public HTTPS address of this server. |
| `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | A Google OAuth client of type Web application, with `BASE_URL/callback` as its authorized redirect URI ([console](https://console.cloud.google.com/apis/credentials)). |
| `ALLOWED_EMAILS` | Comma-separated Google accounts that may sign in. Everyone else is refused. |
| `USDA_API_KEY` | Free key from [fdc.nal.usda.gov/api-key-signup](https://fdc.nal.usda.gov/api-key-signup). |
| `REDIRECT_HOSTS` | Optional. Extra HTTPS hosts MCP clients may redirect to after sign-in. Gemini, Claude and loopback addresses are allowed already. |
| `PORT` | Defaults to `8080`. |

## Usage

In Gemini, add `BASE_URL/mcp` as a custom app under Settings, Connected Apps, and sign in with Google when asked ([Google's steps](https://developers.google.com/connected-apps/mcp/dev-mode/usage)). It registers itself, so leave the client ID and secret empty. Claude works the same way as a [custom connector](https://claude.ai/settings/connectors).

Then try "log 150 grams of cottage cheese for lunch", "I drank a glass of water", "I ran for 30 minutes", or "how am I doing today?".

| Tool | Does |
| --- | --- |
| `search_foods` | Searches your saved foods, then USDA and Open Food Facts. |
| `import_food` | Saves a USDA or Open Food Facts result to your foods. |
| `add_food` | Saves a food from a label, a meal made of saved foods, or a stated total ("about 600 calories"). |
| `update_food` | Fixes or archives a food. Past logs follow the fix. |
| `log_entries` | Logs foods under a meal, and water, all or nothing. Retries with the same `client_request_id` are saved once. |
| `update_log`, `archive_log` | Changes or removes a logged entry. |
| `get_day` | A day's entries, workouts, totals, and what is left of your targets. |
| `log_workout`, `update_workout`, `archive_workout` | Workouts: a type and minutes. |
| `update_settings` | Your time zone and daily targets. |

Logs store grams and point at a food; nutrients live on foods per 100 g and are computed by the server. Nothing is deleted, only archived.

## How it works

One binary serves the MCP endpoint (`/mcp`, Streamable HTTP via the [official Go SDK](https://github.com/modelcontextprotocol/go-sdk)) and a small OAuth 2.1 authorization server: discovery, dynamic client registration, PKCE, refresh tokens that are never rotated so Gemini stays linked, and revocation, as [Google's guide](https://developers.google.com/connected-apps/mcp/oauth) asks. Sign-in itself is handed to Google. Tokens are stored hashed.

## Development

```sh
go test ./...
```

Tests run against a real Postgres: `TEST_DATABASE_URL` if set, otherwise an embedded one downloaded on first run.
