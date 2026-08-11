# excursion funnel

Get an overview of your token spending in Claude Code and Codex. See which models and tools they use. `ef serve` is designed to run as a background service (systemd or equivalent) and stores everything locally in SQLite.

![dashboard](./assets/dashboard.png)
## Install

```sh
go install github.com/dlnilsson/excursion-funnel/cmd/ef@latest
```


### Linux systemd service

Install the user service and enable it for the graphical session:


```sh
mkdir -p ~/.config/systemd/user
cp excursion-funnel.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now excursion-funnel.service
```

The service runs `ef serve`; its output is available with:

```sh
journalctl --user -u excursion-funnel.service
```


### Windows

Create a Task Scheduler task that starts `ef serve` at sign-in and restarts it
if it exits. Run this in PowerShell as the user who uses Codex and Claude Code:

```powershell
$ef = Join-Path $HOME 'go\bin\ef.exe'
$script = Join-Path $HOME 'go\bin\excursion-funnel.vbs'

$vbs = @'
Set shell = CreateObject("WScript.Shell")
WScript.Quit shell.Run("""" & WScript.Arguments(0) & """ serve", 0, True)
'@
Set-Content -LiteralPath $script -Value $vbs -Encoding ASCII

$action = New-ScheduledTaskAction `
  -Execute (Join-Path $env:WINDIR 'System32\wscript.exe') `
  -Argument "`"$script`" `"$ef`"" `
  -WorkingDirectory (Join-Path $HOME 'go\bin')

$trigger = New-ScheduledTaskTrigger -AtLogOn -User "$env:USERDOMAIN\$env:USERNAME"

$settings = New-ScheduledTaskSettingsSet `
  -StartWhenAvailable `
  -RestartCount 999 `
  -RestartInterval (New-TimeSpan -Minutes 1) `
  -ExecutionTimeLimit ([TimeSpan]::Zero) `
  -MultipleInstances IgnoreNew

Register-ScheduledTask `
  -TaskName 'Excursion Funnel' `
  -Description 'Runs the local excursion-funnel proxy.' `
  -Action $action `
  -Trigger $trigger `
  -Settings $settings `
  -User "$env:USERDOMAIN\$env:USERNAME" `
  -RunLevel Limited `
  -Force
```

`$HOME` resolves to the current user's profile directory, so this runs
`$HOME\go\bin\ef.exe serve`. The snippet creates a small `wscript.exe` wrapper
that hides the console window and waits for `ef`, so Task Scheduler can restart
it if it exits. Keep the task configured to run as that user; it needs access
to the same profile, Codex credentials, and `%LOCALAPPDATA%` database as the
client tools.

Start it immediately, or verify it after signing in:

```powershell
Start-ScheduledTask -TaskName 'Excursion Funnel'
Invoke-WebRequest http://127.0.0.1:8787/ui/
```
## Configuration


### Codex

Configure `excursion-funnel` as a Codex model provider profile.
Create `~/.codex/excursion-funnel.config.toml`:

```toml
model_provider = "excursion-funnel"

[model_providers."excursion-funnel"]
name = "Excursion Funnel Proxy"
base_url = "http://127.0.0.1:8787/v1"
wire_api = "responses"
requires_openai_auth = true
```
## Claude code
Update `$HOME/.claude/settings.json`

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:8787"
  }
}
```


<details>

Configuration precedence is: CLI flags > environment variables > built-in defaults.

| Environment variable | Flag | Default | Description |
| --- | --- | --- | --- |
| `EF_ADDR` | `-addr` | `127.0.0.1:8787` | Listen address in `host:port` form. |
| `EF_OPENAI_UPSTREAM` | `-openai-upstream` | `https://chatgpt.com/backend-api/codex` | OpenAI upstream root for Codex / Responses API requests. The proxy strips the client's leading `/v1`, so this root must carry its own prefix. Default targets the ChatGPT Codex backend (works with Codex ChatGPT-subscription auth). For a platform API key, set `https://api.openai.com/v1`. |
| `EF_ANTHROPIC_UPSTREAM` | `-anthropic-upstream` | `https://api.anthropic.com` | Anthropic upstream root for Claude Code / Messages API requests. Use the bare root, without `/v1`. |
| `EF_DB` | `-db` | `%LOCALAPPDATA%\excursion-funnel\usage.sqlite` | SQLite database path. If `LOCALAPPDATA` is unset, the default falls back to the user home directory, then `.`. |
| `EF_SHUTDOWN_TIMEOUT` | `-shutdown-timeout` | `5s` | Graceful HTTP shutdown timeout. |
| `EF_REQUEST_TIMEOUT` | `-request-timeout` | `2m` | Upstream response-header timeout. Set to `0` to disable. |
| `EF_IDLE_TIMEOUT` | `-idle-timeout` | `2m` | Idle client-write timeout while proxying responses. Set to `0` to disable. |
| `EF_QUEUE_DRAIN_TIMEOUT` | `-queue-drain-timeout` | `5s` | Usage queue drain timeout during shutdown. |
| `EF_RETENTION_DAYS` | `-retention-days` | `0` | Delete usage rows older than this many days at startup. Set to `0` to disable retention cleanup. |
| `EF_UI_ENABLED` | `-ui-enabled` | `true` | Serve the read-only dashboard at `/ui/`. Accepts Go boolean values such as `true`, `false`, `1`, or `0`. |

Duration values use Go duration syntax, such as `500ms`, `5s`, or `2m`.

</details>

## Reading usage


```sh
# Today, one row per model (the default grouping).
ef usage today

# Totals per provider, across an explicit date range.
ef usage --since 2026-08-01 --until 2026-08-07 --group-by provider

# Day-by-day breakdown.
ef usage --since 2026-08-01 --group-by day
```


```sh
ef inspect resp_abc123
ef inspect --limit 50 req_abc123
```

## Dashboard

With `serve` running and `EF_UI_ENABLED` left at its default, a read-only
dashboard is available at <http://127.0.0.1:8787/ui/>. It shows today's token
totals, cached-token share, requests by model, and recent errors, backed by
`GET /ui/api/summary` and `GET /ui/api/errors`. Only `GET` is served; there are
no admin controls. Disable it with `-ui-enabled=false`.



![excursion-funnel](./assets/ef.png)
>[!CAUTION] 
> "Asbestos is harmless!" is a trademark of Aperture Science dba Aperture Laboratories
