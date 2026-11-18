# Proposal: make `GET /v1/models` the catalog agents pick from

Status: suggestion only. The snippets below are sketches for the author. They are not a patch. Implement only the slices you agree with.

Date: 2026-11-18. Checked against Pi 1.5.2 (`@earendil-works/pi-coding-agent`) on a machine that already uses this gateway. This note stands on its own. It does not depend on `docs/proposal-relay.md`.

Japanese summary: `GET /v1/models` は `models.yaml` のマップキーを返すだけで、`created` は毎回 `time.Now()`、`GET /v1/models/{id}` は無い。チャットが受け付ける id は `ResolveRequest` の別ルール（レガシーエイリアス、次に実モデル名）なので、一覧の id と送るべき id がずれ得る。Pi 1.5.2 の `/model` はこの経路を呼ばない。`models.json` の静的配列を読む。OpenAI SDK や Continue のように `GET {base}/models` を叩くエージェントには、一覧の `id` をチャットがそのまま受理する id にすれば足りる。Pi で「開いた瞬間に一覧を取り直す」には、同じ応答を `refreshModels` が読む必要がある。上流の `/models` は中継しない。設定済みモデルだけを返す。

## What already works

Leave chat routing alone. `Config.ResolveRequest` already accepts a legacy alias key, then the real model name (`internal/config/config.go`). A client that sends either string is routed. `POST /admin/reload` already swaps the in-memory config, so the next list call can show a file change without a process restart.

`GET /v1/models` is authenticated with the gateway key, same as chat (`cmd/gateway/main.go`). Keep that. The response is already an OpenAI list envelope (`internal/handlers/handlers.go`, `Handlers.Models`).

`models.yaml` can already store the metadata Pi needs, under `pi:` (`internal/ops/modelsyaml.go`, `PiMeta`: `contextWindow`, `input`, `reasoning`). `pi install` copies that into `models.json`. `config.Load` drops it: `ModelConfig` has only `provider` and `model`, and yaml.v3 ignores the unknown `pi` block.

## Problem

Two different clients want a list, and the current route serves neither well.

### Agents that call `GET /v1/models`

An OpenAI SDK `models.list()`, Continue, Cline, or OpenCode calls `{baseUrl}/models` and then sends one of the returned ids on `POST /v1/chat/completions`. Today's handler walks map keys:

```go
for alias := range rt.Config.Models {
    data = append(data, modelEntry{
        ID:      alias,
        Object:  "model",
        Created: now, // time.Now().Unix() on every request
        OwnedBy: rt.Config.Models[alias].Provider,
    })
}
```

When the key is `pi-default` and `model:` is `gpt-oss:120b`, the list offers `pi-default`. Chat accepts both, but the id the wizard now writes into Pi is the real model name (`internal/ops/modelsyaml.go`, `piModelsArray`, `id: a.Model`). A client that trusts the list and a client that trusts `models.json` do not see the same set. `created` changes on every poll, so a client that diffs the payload thinks the catalog changed. There is no `GET /v1/models/{id}`. Ids such as `deepseek-ai/deepseek-v4.1-flash` contain a slash, so a chi `{model}` parameter does not match them.

The README says Pi can switch providers because `/v1/models` enumerates aliases (`README.md`, the `gateway up` section and the Pi connection section). That describes chat routing. It does not describe the picker.

### Pi 1.5.2 does not call this route

Pi's `/model` loads `models.json`. A `models` entry adds or replaces a model on that provider. Opening the picker reloads the file (`docs/models.md` in the Pi package: "Opening `/model` reloads the file"). A live HTTP fetch happens only when the provider implements `refreshModels`. The documented pattern is a small provider hook that `GET`s `/v1/models` and maps `data[].id` (`docs/extensions.md`, the llama.cpp `refreshModels` example).

So a model added to `models.yaml` stays invisible in Pi until menu item 4 runs `gateway pi install` and Pi restarts. The gateway endpoint can be perfect and Pi's picker still will not see it, until something in Pi calls it.

## Out of scope

- Do not proxy the upstream provider's `/models`. Those ids are not all configured, and the call would spend the provider key on a catalog this process cannot route. NVIDIA's catalog probe in the wizard can stay a setup-time check.
- Do not add a second model registry, a database, or a UI.
- Do not change `ResolveRequest`. Old aliases keep working on chat even if the list also shows the real name.
- Do not put provider API keys, gateway keys, or `base_url` values in the model object.

## 1. List the ids chat accepts

One OpenAI model object per public id. The public id is `ModelConfig.Model` (after `Validate` fills it from the key). If the YAML key differs, also emit that key, so a client that saved `pi-default` still sees it. When key and model are equal, emit one object. Sort by id.

`owned_by` stays the gateway provider name (`groq`, `nvidia`). That is a useful picker label and it is not a secret.

`created` is the `models.yaml` modification time in Unix seconds, stored on the runtime when `buildRuntime` loads the file. A reload updates it. A poll of an unchanged file returns the same number.

Carry the `pi:` block through `config.Load`. Extra JSON fields are safe: the OpenAI SDK ignores them, and Pi's own fetch example reads `context_window` and `max_tokens`.

```yaml
# config/models.yaml — fields that already exist
models:
  qwen/qwen3.8-27b:
    provider: groq
    model: qwen/qwen3.8-27b
    pi:
      contextWindow: 131072
      input: [text]
      reasoning: true
```

```go
// internal/config/config.go — sketch
type ModelConfig struct {
    Provider string  `yaml:"provider"`
    Model    string  `yaml:"model"`
    Pi       *PiMeta `yaml:"pi,omitempty"`
}

type PiMeta struct {
    ContextWindow int      `yaml:"contextWindow"`
    MaxTokens     int      `yaml:"maxTokens"`
    Input         []string `yaml:"input"`
    Reasoning     *bool    `yaml:"reasoning"`
}
```

`PiMeta` already lives in `internal/ops`. Put the struct where both `config` and `ops` can use it, or duplicate the four fields on `ModelConfig` with the same yaml names. One definition is enough.

```json
{
  "object": "list",
  "data": [
    {
      "id": "qwen/qwen3.8-27b",
      "object": "model",
      "created": 1731283200,
      "owned_by": "groq",
      "context_window": 131072,
      "max_tokens": 8192,
      "reasoning": true,
      "input": ["text"]
    }
  ]
}
```

Omit `context_window`, `max_tokens`, `reasoning`, and `input` when `pi:` is absent, so a hand-written entry with only `provider` still returns a valid OpenAI object. When `maxTokens` is absent, omit `max_tokens` rather than inventing 4096. Pi's extension example supplies its own default if the field is missing.

`name` may be copied from `id`. Pi's example uses `name ?? id`. It is not required for OpenAI clients.

```go
// internal/handlers/handlers.go — sketch
func (h *Handlers) modelObject(id string, mc config.ModelConfig, created int64) modelEntry {
    e := modelEntry{ID: id, Object: "model", Created: created, OwnedBy: mc.Provider}
    if mc.Pi == nil {
        return e
    }
    if mc.Pi.ContextWindow > 0 {
        e.ContextWindow = mc.Pi.ContextWindow
    }
    if mc.Pi.MaxTokens > 0 {
        e.MaxTokens = mc.Pi.MaxTokens
    }
    e.Input = mc.Pi.Input
    e.Reasoning = mc.Pi.Reasoning
    return e
}
```

Use `*bool` and `omitempty` on `reasoning` so a missing flag is not encoded as `false`. A false value is meaningful for Pi (it stops the `developer` role). A missing value should stay missing.

`pi install` should keep writing `models.json` from this same struct. Menu item 4 and `GET /v1/models` then cannot disagree about `reasoning` or `contextWindow`.

## 2. `GET /v1/models/{id}`

Same auth as the list. Resolve with `ResolveRequest`, so both `pi-default` and `gpt-oss:120b` return the object whose `id` is the public id you want the client to keep (the real model name). A miss is the existing OpenAI error shape, 404, code `model_not_found`.

Chi's `{model}` stops at a slash. Capture the rest of the path:

```go
// cmd/gateway/main.go — sketch. Register beside the existing list route.
authed.Get("/v1/models", h.Models)
authed.Get("/v1/models/*", h.Model)
```

```go
func (h *Handlers) Model(w http.ResponseWriter, r *http.Request) {
    id := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
    id = strings.TrimSpace(id)
    mc, ok := h.current().Config.ResolveRequest(id)
    if !ok {
        apierr.Write(w, http.StatusNotFound, "model not found", "invalid_request_error", "model_not_found")
        return
    }
    // Respond with the public id (mc.Model), not whichever alias was in the URL.
    _ = json.NewEncoder(w).Encode(h.modelObject(mc.Model, mc, h.current().ModelsCreated))
}
```

Percent-decode the parameter. A client may send `deepseek-ai%2Fdeepseek-v4.1-flash` or `deepseek-ai/deepseek-v4.1-flash`. Both should resolve.

## 3. What each kind of client does after this

Switching is not a new route. The agent sends `"model": "<id from the list>"` on the next chat completion. `ResolveRequest` already maps that id.

| Client | What it needs from this change |
|---|---|
| OpenAI SDK, Continue, Cline, OpenCode | The list ids are valid chat ids. They already fetch `/v1/models` and send the choice. |
| Pi, with today's `models.json` | No picker change. `pi install` still registers models. The list and the file now share one metadata source. |
| Pi, if you also want a live picker | A `refreshModels` hook on the `gateway` provider. That hook is Pi configuration, not a Go route. See below. |

Correct the README sentences that say `/v1/models` is why Pi's `/model` can switch providers. Say that chat accepts the listed ids, and that Pi's picker reads `models.json` unless a `refreshModels` hook is installed.

### Optional Pi hook

This is not a Go change. It is the smallest way for `/model` to pick up a gateway reload without menu item 4. Pi calls `refreshModels` when the picker opens (`docs/extensions.md` in Pi 1.5.2). The gateway key is already the provider `apiKey`.

```javascript
// ~/.pi/agent/extensions/gateway-models.ts — sketch, not part of this repo
export default function (pi) {
  pi.registerProvider("gateway", {
    baseUrl: "http://127.0.0.1:18080/v1",
    api: "openai-completions",
    async refreshModels({ signal, credential }) {
      const key = credential?.key;
      const response = await fetch("http://127.0.0.1:18080/v1/models", {
        headers: key ? { authorization: `Bearer ${key}` } : {},
        signal,
      });
      if (!response.ok) throw new Error(`gateway models: ${response.status}`);
      const body = await response.json();
      return (body.data ?? []).map((m) => ({
        id: m.id,
        name: m.id,
        reasoning: m.reasoning === true,
        input: m.input ?? ["text"],
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
        contextWindow: m.context_window ?? 128000,
        maxTokens: m.max_tokens ?? 16384,
        // Drop this once the Groq relay rewrites developer → system.
        compat: m.owned_by === "groq" ? { supportsDeveloperRole: false } : undefined,
      }));
    },
  });
}
```

Ship that only as a documented example. Do not make `pi install` depend on an extension. A user who never installs the extension keeps today's static `models.json` behavior.

The `supportsDeveloperRole` line is a Pi-side workaround for the Groq 400 described in `docs/proposal-relay.md`. If that rewrite lands, the example should stop sending the flag.

## Compatibility

- Existing clients that only read `id`, `object`, `created`, and `owned_by` still parse the list. `created` changes meaning from "request time" to "config mtime". Call that out in `CHANGELOG.md`.
- A config with no `pi:` block returns the same four fields as today, except `id` may be the real model name and a distinct legacy key is a second object.
- `TestModels` and `modelIDs` in `internal/handlers/handlers_test.go` assert the map key (`pi-fast`). Update them to assert the public id, and add a case where the key and `model:` differ.
- Auth, egress allowlist, and the audit rule (no message text) stay as they are. This route does not call upstream.
- `STRICT_KEYS` is unchanged. A provider with no key can still appear in the list. Chat to that provider already fails. The list is the configured catalog, not a liveness probe.

## Files this would touch

- `internal/config/config.go` — keep `pi:` on `ModelConfig`.
- `internal/handlers/handlers.go` — build the list from public ids plus legacy keys. Add `Model`. Stable `created`.
- `cmd/gateway/main.go` — `GET /v1/models/*`.
- `internal/ops/modelsyaml.go` — `piModelsArray` reads the same metadata struct the handler serves.
- `internal/handlers/handlers_test.go` — slash ids, alias plus real name, omitted `pi:` fields, 404.
- `README.md`, `CHANGELOG.md` — what the list is, and that Pi's picker does not call it unless `refreshModels` is installed.

## Done when

- `GET /v1/models` with the gateway key returns each configured real model name, and also a legacy key when it differs. `POST /v1/chat/completions` with any returned `id` resolves.
- A second call, with the file unchanged, returns the same `created` value.
- `GET /v1/models/deepseek-ai/deepseek-v4.1-flash` returns that one object. An unknown id returns 404 `model_not_found`. The body contains no key material.
- A `pi:` block on the YAML entry shows up as `context_window`, `max_tokens`, `reasoning`, and `input`. An entry without `pi:` omits those fields.
- `POST /admin/reload` after a YAML edit makes the next list call show the new id. No process restart.
- The README no longer says that `/v1/models` is what Pi's `/model` picker reads.
