# AI provider acceptance fixture

`cmd/fake-ai-provider` is a deterministic, test-only OpenAI-compatible
fixture. It supports the Chat Completions and Responses streaming protocols
used by HappyLearn and listens on port 8090 inside the disposable E2E network.

Requests must use the synthetic credential `Bearer e2e-provider-key`. A prompt
may include one documented `[case:...]` marker to select a deterministic
success or failure. The provider reads a bounded JSON body, never stores or
logs credentials or request content, and exposes only numeric aggregate
protocol/case counts at `GET /test/counts`.

Build it explicitly with `Dockerfile.fake-ai`. The fixture is intentionally
absent from production Dockerfiles and development Compose.
